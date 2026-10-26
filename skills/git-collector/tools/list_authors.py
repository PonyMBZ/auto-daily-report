# -*- coding: utf-8 -*-
"""
list_authors —— 列出一个 git 仓库的全部提交作者(去重), 给配置界面做多选框用。

设计原则与 git_log.py 相同:
1. 参数全部来自命令行, 工具不知道环境
2. 成功 → stdout 一行 JSON; 失败 → 结构化 JSON + 非零退出码
3. 远程仓库的缓存克隆逻辑直接复用 git_log.ensure_local_repo ——
   同一目录里的工具就是模块, import 即可, 不复制粘贴那 20 行
   (工具之间是平级协作, 不绕 run.py —— run.py 是宿主门面, 不是工具间的路由)

去重键用邮箱而不是姓名: 同名不同邮箱是两个人(改名/换邮箱同理),
邮箱才是 git 世界里作者的唯一标识。
"""
import argparse
import json
import subprocess
import sys
from pathlib import Path

# 契约: stdout 恒为 UTF-8(Windows 管道默认 GBK 会炸链)
sys.stdout.reconfigure(encoding="utf-8")

sys.path.insert(0, str(Path(__file__).parent))
from git_log import ensure_local_repo, is_remote  # noqa: E402

# 防御性上限: 作者数超过这个说明仓库异常(或是镜像了全公司历史), 界面也放不下
CAP = 300


def main() -> int:
    parser = argparse.ArgumentParser(description="列出 git 仓库的提交作者(去重)")
    parser.add_argument("--repo", required=True,
                        help="仓库本地路径, 或远程地址(http(s):// 或 git@ 开头)")
    parser.add_argument("--cache-dir", default="",
                        help="远程地址的裸库缓存目录(本地路径时不需要)")
    args = parser.parse_args()

    repo = args.repo
    try:
        if is_remote(repo):
            if not args.cache_dir:
                print(json.dumps({"ok": False, "error": "cache_dir_required"},
                                 ensure_ascii=False))
                return 1
            repo = ensure_local_repo(repo, args.cache_dir)
    except RuntimeError as e:
        print(json.dumps({"ok": False, "error": "clone_failed", "detail": str(e),
                          "repo": args.repo}, ensure_ascii=False))
        return 1

    # %an 姓名 %ae 邮箱, tab 分列; 不带时间窗 —— 作者列表是"有谁", 不是"今天谁提交"
    proc = subprocess.run(
        ["git", "-C", repo, "log", "--encoding=UTF-8",
         "--pretty=format:%an%x09%ae", f"-n{100000}"],
        capture_output=True, text=True, encoding="utf-8", errors="replace",
        timeout=120)
    if proc.returncode != 0:
        print(json.dumps({"ok": False, "error": "git_log_failed",
                          "detail": proc.stderr.strip()[:300],
                          "repo": args.repo}, ensure_ascii=False))
        return 1

    seen: dict[str, str] = {}  # email(lower) -> 显示名 "姓名 <邮箱>"
    for line in proc.stdout.splitlines():
        parts = line.split("\t")
        if len(parts) != 2:
            continue
        name, email = parts
        key = email.strip().lower()
        if key and key not in seen:
            seen[key] = f"{name.strip()} <{email.strip()}>"
        if len(seen) >= CAP:
            break

    authors = sorted(seen.values(), key=str.lower)
    print(json.dumps({"ok": True, "authors": authors, "repo": args.repo},
                     ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
