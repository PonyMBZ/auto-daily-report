# -*- coding: utf-8 -*-
"""
git_log —— 从本地 git 仓库提取"指定时间窗内、指定作者"的提交记录。

设计原则与 decrypt.py 相同(通用, 以后写任何工具都遵守):
1. 参数全部来自命令行, 工具不知道环境, 宿主告诉他什么就是什么
2. 成功 → stdout 一行 JSON; 失败 → 结构化 JSON + 非零退出码
3. 不做聪明事 —— 不找仓库、不猜作者, 这些决策属于 SKILL.md 或宿主

--repo 支持两种取值:
- 本地路径(目录存在): 直接读
- 远程地址(http(s):// 或 git@ 开头): 配合 --cache-dir 克隆成裸库缓存。
  缓存目录位置由调用方决定(工具不知道项目根在哪), 目录名用地址 hash ——
  同一地址永远落到同一目录, 第二次起是 `git fetch` 增量更新, 不是全量重克。
  网络挂了/凭证失效时增量拉取失败不算致命 —— 退回用缓存里的旧数据出日报,
  "今天没网"不该让"整理提交记录"这个来源拖垮全局(调用方决定容忍策略)。

踩坑实录(7.2, 两条都是血的教训):
- 不要信 git 的 --since/--until 日期解析! git for Windows 对"无时区的
  ISO 时间串"按 UTC 解析(本地 +8 时区下 09:00 变成 17:00), 窄窗口静默返回空集;
  且我们观测到同样的命令串在不同时间行为不一致(疑似与运行环境的 TZ 有关)。
  结论: 时间窗不用 git 解析, 由调用方算好 epoch 传进来, 这里只做整数比较 ——
  把不确定的第三方解析从关键路径上拿掉。
- git log 按提交图遍历输出(沿 parent 链), 不是严格的日期倒序 —— rebase/
  改日期都会打乱, 所以必须全量扫(有 SCAN_CAP 兜底)再过滤, 不能提前 break。
"""
import argparse
import hashlib
import json
import subprocess
import sys
import time
from pathlib import Path

# 契约: stdout 恒为 UTF-8(Windows 管道默认 GBK 会炸链, 详见 _shared/config.py 的 Q&A)
sys.stdout.reconfigure(encoding="utf-8")

# %ct = 提交时间的 Unix epoch(秒), 整数比较无歧义; \\t 分列(提交主题里空格常见, tab 几乎没有)
PRETTY = "%h%x09%an%x09%ct%x09%s"

# 单次最多扫描的提交数(防御性上限, 防超大仓库全表扫描; 一天的提交不可能超过它)
SCAN_CAP = 2000


def is_remote(repo: str) -> bool:
    """区分本地路径和远程地址。http(s):// 和 git@host:path 两种克隆语法都认。"""
    return "://" in repo or repo.startswith("git@")


def ensure_local_repo(repo: str, cache_dir: str) -> str:
    """远程地址 -> 本地裸库缓存路径。

    首次 clone --bare(全量), 之后 fetch --all(增量)。
    fetch 失败不抛 —— 缓存里的旧数据照样能用, 调用方决定要不要容忍。
    凭证(私有仓库)用本机 git 的全局配置, 工具不碰。
    """
    key = hashlib.md5(repo.encode("utf-8")).hexdigest()[:12]
    dest = Path(cache_dir) / (key + ".git")
    if (dest / "HEAD").exists():
        subprocess.run(["git", "-C", str(dest), "fetch", "--all", "--quiet"],
                       capture_output=True, timeout=120)
    else:
        dest.parent.mkdir(parents=True, exist_ok=True)
        proc = subprocess.run(["git", "clone", "--bare", "--quiet", repo, str(dest)],
                              capture_output=True, text=True, encoding="utf-8",
                              errors="replace", timeout=300)
        if proc.returncode != 0:
            raise RuntimeError(f"克隆失败: {proc.stderr.strip()[:200]}")
    return str(dest)


def main() -> int:
    parser = argparse.ArgumentParser(description="提取 git 仓库时间窗内的提交记录")
    parser.add_argument("--repo", required=True,
                        help="仓库本地路径, 或远程地址(http(s):// 或 git@ 开头)")
    parser.add_argument("--cache-dir", default="",
                        help="远程地址的裸库缓存目录(本地路径时不需要)")
    parser.add_argument("--since-epoch", required=True, type=int,
                        help="窗口起点(含), Unix 秒, 调用方算好传入")
    parser.add_argument("--until-epoch", required=True, type=int,
                        help="窗口终点(不含), Unix 秒")
    parser.add_argument("--author", default="",
                        help="提交人子串匹配(姓名或邮箱), 空=全部")
    parser.add_argument("--max", type=int, default=100, help="最多返回条数")
    args = parser.parse_args()

    repo = args.repo
    try:
        if is_remote(repo):
            if not args.cache_dir:
                print(json.dumps({"ok": False, "error": "cache_dir_required",
                                  "detail": "远程地址必须配 --cache-dir"}, ensure_ascii=False))
                return 1
            repo = ensure_local_repo(repo, args.cache_dir)
    except RuntimeError as e:
        print(json.dumps({"ok": False, "error": "clone_failed", "detail": str(e),
                          "repo": args.repo}, ensure_ascii=False))
        return 1

    cmd = ["git", "-C", repo, "log",
           "--encoding=UTF-8",
           f"--pretty=format:{PRETTY}",
           f"-n{SCAN_CAP}"]
    if args.author:
        cmd.append(f"--author={args.author}")

    # git 不存在 / 仓库无效 / 命令失败: 错误信息在 stderr, 一并交给调用方
    proc = subprocess.run(cmd, capture_output=True, text=True, encoding="utf-8",
                          errors="replace",  # 极端情况下个别坏字节不炸整批
                          timeout=60)
    if proc.returncode != 0:
        print(json.dumps({"ok": False, "error": "git_log_failed",
                          "detail": proc.stderr.strip()[:300],
                          "repo": args.repo}, ensure_ascii=False))
        return 1

    commits = []
    for line in proc.stdout.splitlines():
        parts = line.split("\t")
        if len(parts) != 4:
            continue  # 格式不符的行(空行/异常输出)跳过, 不炸
        h, author, epoch_s, subject = parts
        epoch = int(epoch_s)
        if not (args.since_epoch <= epoch < args.until_epoch):
            continue  # 窗口外
        commits.append({
            "hash": h,
            "time": time.strftime("%H:%M", time.localtime(epoch)),
            "author": author,
            "subject": subject,
        })
        if len(commits) >= args.max:
            break
    # 不能"遇到第一条早于窗口的提交就停" —— 踩坑实录(7.2): git log 按提交图
    # 遍历输出(沿 parent 链), 日期只保证"通常"单调; rebase/改日期/并行分支都会
    # 破坏。上面全扫过滤才是正确做法, SCAN_CAP 兜住超大仓库的成本。

    print(json.dumps({"ok": True, "commits": commits, "repo": args.repo},
                     ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
