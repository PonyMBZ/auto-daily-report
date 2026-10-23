# -*- coding: utf-8 -*-
"""
list_chats —— 列出钉钉本地库的全部会话清单。

用途: 回答"我想只采集某群/某人的聊天, 该在 conversations 里写什么?"
- 群聊: title 就是群名, 抄进配置即可(支持包含匹配, 写一部分就行)
- 单聊: title 优先显示对方昵称(经 tbuser_profile_v2 翻译); 翻译不了的
  (对方资料不在本地库)才退回 uid; cid 形如 "uid1:uid2", 也能从中认出双方

用法:
  python tools/list_chats.py --src <加密库> --uid <我的uid> --salt <salt>
  [--kind all|single|group]   # 默认 all; single=只看单聊(找 uid 用)

输出 JSON: {"ok": true, "chats": [{"title", "is_group", "cid"}]}
"""
import argparse
import json
import sqlite3
import subprocess
import sys

# 契约: stdout 恒为 UTF-8(Windows 管道默认 GBK 会炸链, 详见 _shared/config.py 的 Q&A)
sys.stdout.reconfigure(encoding="utf-8")
import tempfile
from pathlib import Path


def main() -> int:
    ap = argparse.ArgumentParser(description="列出钉钉本地库会话清单")
    ap.add_argument("--src", required=True, help="加密的数据库路径")
    ap.add_argument("--uid", required=True, help="我的钉钉内部 uid(解密用)")
    ap.add_argument("--salt", required=True, help="解密盐")
    ap.add_argument("--kind", default="all", choices=["all", "single", "group"],
                    help="all=全部; single=只看单聊(找对方 uid 用); group=只看群")
    args = ap.parse_args()

    # 借 decrypt.py 的门面临时解密一份, 查完即删(tempfile 自动清理)
    with tempfile.TemporaryDirectory(prefix="listchats_") as tmp:
        dec = str(Path(tmp) / "dingtalk_dec.db")
        proc = subprocess.run(
            [sys.executable, str(Path(__file__).parent / "decrypt.py"),
             "--src", args.src, "--dst", dec, "--uid", args.uid, "--salt", args.salt],
            capture_output=True, text=True, encoding="utf-8",
        )
        if proc.returncode != 0:
            print(json.dumps({"ok": False, "error": "decrypt_failed",
                              "detail": proc.stdout[:300]}, ensure_ascii=False))
            return 1

        con = sqlite3.connect(f"file:{dec}?mode=ro", uri=True)  # 只读, 绝不写库
        # 单聊 title(uid) → 昵称翻译, 与 extract.py 同一套逻辑(表缺失则跳过)
        profiles = {}
        try:
            tables = {r[0] for r in con.execute(
                "SELECT name FROM sqlite_master WHERE type='table'")}
            if "tbuser_profile_v2" in tables:
                profiles = {str(u): n for u, n in con.execute(
                    "SELECT uid, nick FROM tbuser_profile_v2") if n}
        except sqlite3.Error:
            pass
        chats = []
        for cid, title, ctype in con.execute("SELECT cid, title, type FROM tbconversation"):
            is_group = ctype == 2
            if args.kind == "single" and is_group:
                continue
            if args.kind == "group" and not is_group:
                continue
            title = title or cid
            if not is_group and ":" in str(cid):
                others = [u for u in str(cid).split(":") if u != str(args.uid)]
                if others and others[0] in profiles:
                    title = profiles[others[0]]  # 显示人名, uid 仍在 cid 字段里可查
            chats.append({"title": title, "is_group": is_group, "cid": cid})
        con.close()

    print(json.dumps({"ok": True, "count": len(chats), "chats": chats},
                     ensure_ascii=False, indent=1))
    return 0


if __name__ == "__main__":
    sys.exit(main())
