# -*- coding: utf-8 -*-
"""
git-collector 的门面 —— 宿主唯一调用的入口。

契约:
  输入: stdin JSON(debug=false) 或 config.yaml(debug=true)
  输出: stdout JSON: {"ok": true, "messages": [...]}
  退出码: 0 = 成功, 非 0 = 失败

流程(代码驱动, 不用 AI —— 决策账: 没有任何一步需要现场决策):
  按 config 的 git.repos 逐个调 git_log 工具 → 合成统一消息 → 交卷。

为什么提交记录也算"消息": 第七章的合并契约是"一筐 Message"。
钉钉消息是 chat, git 提交是 code —— 对日报来说它们都是"我今天产出了什么"
的证据, 放进同一个筐, 让 AI 一起蒸馏。
"""
import datetime
import json
import subprocess
import sys
import time
from pathlib import Path

SKILL_DIR = Path(__file__).parent
sys.path.insert(0, str(SKILL_DIR.parent / "_shared"))

from config import load_config, merge_stdin_overrides, project_root, run_guarded  # noqa: E402


def day_window_epochs(date_str: str, since) -> tuple[int, int]:
    """把"哪天"翻译成 [当天 since 起点, 次日 since 起点) 的 Unix 秒。

    与 dingtalk-collector 的 day_range_ms 同一语义(半开区间, 相邻天不重不漏),
    只是单位是秒。用 time.mktime 让系统时间库处理时区, 不自己造轮子。
    踩坑实录(7.2): 曾把时间串交给 git --since 解析, 被无时区按 UTC 处理 +
    行为不稳定坑死; 现在过滤完全由工具内的整数比较完成, 这里是唯一的算时间处。
    """
    if isinstance(since, str) and ":" in since:
        sh, sm = map(int, since.split(":"))
    else:
        sh, sm = int(since), 0
    if date_str:
        y, m, d = map(int, date_str.split("-"))
    else:
        today = datetime.date.today()
        y, m, d = today.year, today.month, today.day
    start = int(time.mktime((y, m, d, sh, sm, 0, 0, 0, -1)))
    # 结束 = 次日同一时刻, 与 collector 用 mktime 算次日同理
    end = int(time.mktime((y, m, d + 1, sh, sm, 0, 0, 0, -1)))
    return start, end


def main() -> int:
    cfg = merge_stdin_overrides(load_config())
    git = cfg.get("git") or {}
    repos = git.get("repos") or []
    if not repos:
        print(json.dumps({"ok": False, "error": "no_git_repos",
                          "hint": "config.yaml 的 git.repos 还没配仓库路径"},
                         ensure_ascii=False))
        return 1

    date = git.get("date", "")  # 空 = 今天; 宿主传入口令覆盖配置
    since, until = day_window_epochs(date, git.get("since_hour", "9:00"))
    author = git.get("author", "")
    max_per_repo = int(git.get("max_per_repo", 50))

    messages = []
    errors = []
    # 远程仓库的裸库缓存目录 —— 放项目根下, 和 test_output 平级。
    # 目录名是地址 hash, 手工看不知道是哪个仓库没关系, 工具自己认。
    cache_dir = project_root() / "git_cache"
    for repo in repos:
        # 每个仓库自己的 author 覆盖全局 author(仓库可以给不同署名)
        repo_author = repo.get("author") or author
        proc = subprocess.run(
            [sys.executable, str(SKILL_DIR / "tools" / "git_log.py"),
             "--repo", str(repo.get("path", "")),
             "--cache-dir", str(cache_dir),
             "--since-epoch", str(since), "--until-epoch", str(until),
             "--author", str(repo_author),
             "--max", str(max_per_repo)],
            capture_output=True, text=True, encoding="utf-8",
        )
        try:
            result = json.loads(proc.stdout.strip() or "{}")
        except json.JSONDecodeError:
            result = {"ok": False, "error": "tool_bad_output",
                      "detail": proc.stdout[:300]}
        if not result.get("ok"):
            # 单仓失败不拖垮全局(与管道层的多源策略一致), 但错误要带回来
            errors.append(f"{repo.get('path')}: {result.get('error')} "
                          f"{result.get('detail', '')}")
            continue
        # 显示名兜底: 本地取目录名, 远程取 URL 最后一段并去掉 .git 后缀
        name = repo.get("name")
        if not name:
            tail = Path(str(repo.get("path", "")).rstrip("/")).name
            name = tail[:-4] if tail.endswith(".git") else tail
        if not name:
            name = "repo"
        for c in result.get("commits", []):
            messages.append({
                "time": c["time"],
                # conversation 用仓库名 —— AI 蒸馏时知道这条证据来自哪个项目
                "conversation": name,
                "sender": c["author"],
                "text": f"{c['subject']} ({c['hash']})",
            })

    if not messages and errors:
        print(json.dumps({"ok": False, "error": "all_repos_failed",
                          "detail": "; ".join(errors)}, ensure_ascii=False))
        return 1

    messages.sort(key=lambda m: m["time"])  # 与 collector 一致: 按时间排序
    print(json.dumps({"ok": True, "messages": messages,
                      "repo_errors": errors}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(run_guarded(main))
