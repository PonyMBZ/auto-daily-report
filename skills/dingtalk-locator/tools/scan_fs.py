# -*- coding: utf-8 -*-
"""
工具: scan_fs —— 扫描文件系统, 找出疑似钉钉数据目录的候选。

疑问解答区:
Q: 为什么不直接写死路径 "C:\\Users\\admin\\AppData\\Roaming\\DingTalk\\..."?
A: 那是你自己机器上的路径。别的用户可能装在 D 盘、改名、或多用户系统每个
   用户一份。工具的职责是"在不知道的前提下找出来", 写死路径 = 这个工具只对你一个人有用。

Q: 怎么判断一个目录"疑似"钉钉数据目录?
A: 看证据文件。钉钉数据目录的特征: 里面有 user_config(存 salt) 和 DBFiles/。
   我们收集"证据", 让 AI(大脑)根据证据强弱做最终判断 —— 工具只呈堂证供, 不判案。
"""
import argparse
import json
import os
import sys

# 契约: stdout 恒为 UTF-8(Windows 管道默认 GBK 会炸链, 详见 _shared/config.py 的 Q&A)
sys.stdout.reconfigure(encoding="utf-8")
from pathlib import Path

# 每个盘符下的用户目录都可能有(多用户系统、双系统盘等)
# 疑问: "CDEFGHIJKLMNOPQRSTUVWXYZ" 从 C 开始? —— A/B 是软驱时代遗留, 现在几乎没有
DRIVE_LETTERS = "CDEFGHIJKLMNOPQRSTUVWXYZ"


def possible_parents() -> list[Path]:
    """收集所有可能出现 DingTalk 数据目录的父目录。"""
    parents = []
    # 当前登录用户的 AppData(最常见, 命中 99% 的情况)
    appdata = os.environ.get("APPDATA")
    if appdata:
        parents.append(Path(appdata) / "DingTalk")
    # 所有盘符下所有用户的 AppData(防御性: 公司电脑可能用别账号装过)
    for letter in DRIVE_LETTERS:
        users = Path(f"{letter}:/Users")
        if users.exists():
            for user_dir in users.iterdir():
                dt = user_dir / "AppData" / "Roaming" / "DingTalk"
                if dt not in parents:
                    parents.append(dt)
    return parents


def inspect(candidate: Path) -> dict | None:
    """检查一个目录是不是钉钉数据目录。是 → 返回证据详情; 否 → None。"""
    user_config = candidate / "user_config"
    db = candidate / "DBFiles" / "dingtalk.db"
    if not user_config.exists():
        return None  # 没有 user_config 就不是 V3 数据目录, 直接排除
    evidence = ["user_config"]
    if db.exists():
        evidence.append("DBFiles/dingtalk.db")
    return {"dir": str(candidate), "evidence": evidence}


def main() -> int:
    parser = argparse.ArgumentParser(description="扫描文件系统, 找出钉钉数据目录候选")
    args = parser.parse_args()  # 本工具不需要参数; 疑问: 为什么还要写? —— 保持"所有工具都有 --help"的统一契约

    try:
        candidates = []
        for parent in possible_parents():
            if not parent.exists():
                continue
            # glob("*_v3*") 找名字里带 _v3 的子目录(钉钉数据目录的命名规律)
            # 疑问: 为什么是子目录? —— 父目录 DingTalk\ 下可能有多个用户目录, 每个都是 *_v3
            for child in parent.glob("*_v3*"):
                info = inspect(child)
                if info:
                    candidates.append(info)

        print(json.dumps({"ok": True, "candidates": candidates}, ensure_ascii=False))
        return 0
    except Exception as e:
        print(json.dumps({"ok": False, "error": "scan_failed", "detail": str(e)}, ensure_ascii=False))
        return 1


if __name__ == "__main__":
    sys.exit(main())
