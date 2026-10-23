# -*- coding: utf-8 -*-
"""
工具: verify —— 验证闸门。对一个候选目录做"真实性检验"。

疑问解答区:
Q: 为什么不相信 AI 找到的路径, 还要多此一举验证?
A: 这正是本项目的核心架构原则: AI 负责"提议", 代码负责"裁决"。
   AI 可能看错路径(比如把安装目录当成数据目录), 但"能不能解密出 SQLite 文件头"
   是物理事实, 不会撒谎。验证闸门 = 给 AI 的自信装上刹车。

Q: 验证什么?
A: 三层, 由浅入深:
   1. 结构: 有 user_config 和 DBFiles/dingtalk.db 吗?(像不像)
   2. 数据: user_config 能解出 salt 吗?(是不是)
   3. 解密: 给了 uid 的话, 真解密 64 字节看是不是 SQLite 头?(能不能用)
"""
import argparse
import base64
import hashlib
import json
import sys

# 契约: stdout 恒为 UTF-8(Windows 管道默认 GBK 会炸链, 详见 _shared/config.py 的 Q&A)
sys.stdout.reconfigure(encoding="utf-8")
from pathlib import Path


def read_salt(data_dir: Path) -> str:
    """user_config 是 Base64 编码的 JSON。疑问: 为什么要 Base64?
    答: 不是加密, 只是编码(谁都能解)。大概是为了防止"直接打开就看到明文"的随手偷看。
    这提醒我们: Base64 ≠ 安全, 只是障眼法。"""
    raw = (data_dir / "user_config").read_bytes()
    cfg = json.loads(base64.b64decode(raw))
    return cfg["salt"]


def can_decrypt(db_path: Path, uid: str, salt: str) -> bool:
    """用 uid+salt 推导密钥, 解密前 64 字节验证文件头。
    与 dingtalk-collector/tools/decrypt.py 的密钥公式保持一致(两处重复是刻意的:
    验证工具要自成一体, 不依赖另一个 skill 的内部实现)。"""
    from Crypto.Cipher import AES
    dk = hashlib.pbkdf2_hmac("sha1", (uid + salt).encode(), b"666DingT", 1000, 32)
    key = hashlib.md5(dk).hexdigest()[:16].encode("ascii")
    with open(db_path, "rb") as f:
        head = AES.new(key, AES.MODE_ECB).decrypt(f.read(64))
    return head.startswith(b"SQLite format 3")


def main() -> int:
    parser = argparse.ArgumentParser(description="验证候选目录是否为可用的钉钉数据目录")
    parser.add_argument("--dir", required=True, help="候选数据目录(形如 xxxxxxxxxx_v3)")
    parser.add_argument("--uid", help="可选。提供则额外做真实解密验证")
    args = parser.parse_args()

    data_dir = Path(args.dir)
    try:
        # 第一层: 结构
        db_path = data_dir / "DBFiles" / "dingtalk.db"
        if not (data_dir / "user_config").exists():
            print(json.dumps({"ok": True, "is_dingtalk_dir": False,
                              "reason": "没有 user_config"}, ensure_ascii=False))
            return 0
        if not db_path.exists():
            print(json.dumps({"ok": True, "is_dingtalk_dir": False,
                              "reason": "没有 DBFiles/dingtalk.db"}, ensure_ascii=False))
            return 0

        # 第二层: 数据
        salt = read_salt(data_dir)

        # 第三层: 解密(可选, 需要 uid)
        verified = False
        if args.uid:
            verified = can_decrypt(db_path, args.uid, salt)

        print(json.dumps({
            "ok": True,
            "is_dingtalk_dir": True,
            "verified": verified,          # 疑问: ok 和 verified 什么区别?
            "salt": salt,                  # 答: ok=工具本身没出故障; verified=解密验证通过。
            "db_path": str(db_path),       #      AI 采纳候选时必须看 verified, 不能只看 ok
        }, ensure_ascii=False))
        return 0
    except Exception as e:
        print(json.dumps({"ok": False, "error": "verify_failed", "detail": str(e)},
                         ensure_ascii=False))
        return 1


if __name__ == "__main__":
    sys.exit(main())
