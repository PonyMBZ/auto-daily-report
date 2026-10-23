# -*- coding: utf-8 -*-
"""
工具: correct —— 修正层的写入端。

用户(或宿主)对日报不满意时, 把纠正写进修正记录文件。
下次生成日报时, run.py 会把这份记录塞进提示词, AI 被要求不再重犯。

疑问: 为什么不直接改日报? 而要记一笔?
答: 改一次是治标, 记住是治本。distilly 的 Correction Log 思想:
    修正应该影响"以后所有产出", 而不只是当前这一份。
    这就是"演化"(evolve)的最小实现。
"""
import argparse
import datetime
import json
import sys

# 契约: stdout 恒为 UTF-8(Windows 管道默认 GBK 会炸链, 详见 _shared/config.py 的 Q&A)
sys.stdout.reconfigure(encoding="utf-8")
from pathlib import Path


def main() -> int:
    parser = argparse.ArgumentParser(description="追加一条日报修正记录")
    parser.add_argument("--text", required=True, help="修正内容, 如: 不要写具体版本号")
    parser.add_argument("--file", required=True, help="修正记录文件路径")
    args = parser.parse_args()

    try:
        path = Path(args.file)
        path.parent.mkdir(parents=True, exist_ok=True)
        today = datetime.date.today().isoformat()
        # 追加不覆盖 —— 修正历史是累积资产, 永远不许删改(只能追加新的反转)
        with open(path, "a", encoding="utf-8") as f:
            f.write(f"- [{today}] {args.text}\n")
        n = sum(1 for line in open(path, encoding="utf-8") if line.startswith("- ["))
        print(json.dumps({"ok": True, "corrections_count": n}, ensure_ascii=False))
        return 0
    except Exception as e:
        print(json.dumps({"ok": False, "error": "correct_failed", "detail": str(e)},
                         ensure_ascii=False))
        return 1


if __name__ == "__main__":
    sys.exit(main())
