# -*- coding: utf-8 -*-
"""
钉钉 V3 数据库解密工具。

设计原则（通用，以后写任何 skill 工具都遵守）:
1. 参数全部来自命令行 —— 工具不知道环境，宿主告诉他什么就是什么
2. 成功 → stdout 输出一行 JSON；失败 → stderr 输出错误 + 退出码非零
   这是为了让宿主程序可以无脑解析: exit code 0 + JSON = 成功
3. 不做任何"聪明"的事 —— 不猜路径、不猜密钥、不自动重试
   这些决策属于 SKILL.md(大脑) 或宿主，工具只执行

用法:
  python decrypt.py --src <加密db路径> --dst <输出路径> --uid <10位uid> --salt <32位hex>

加密方案 (钉钉桌面版 V3, 来自社区逆向研究 dingwave-V3):
  密钥 = MD5( PBKDF2_SHA1( uid + salt, "666DingT", 1000轮, 32字节) ).hex()[:16]
  算法 = AES-128-ECB, 按 4096 字节页加密
"""
import argparse
import hashlib
import json
import sys

# 契约: stdout 恒为 UTF-8(Windows 管道默认 GBK 会炸链, 详见 _shared/config.py 的 Q&A)
sys.stdout.reconfigure(encoding="utf-8")


def derive_key(uid: str, salt: str) -> bytes:
    """从 uid 和 salt 推导 AES-128 密钥。"""
    password = (uid + salt).encode("utf-8")
    dk = hashlib.pbkdf2_hmac("sha1", password, b"666DingT", 1000, 32)
    return hashlib.md5(dk).hexdigest()[:16].encode("ascii")


def decrypt_file(src: str, dst: str, key: bytes) -> None:
    """整库 AES-128-ECB 解密。分块读防止大文件撑爆内存。"""
    from Crypto.Cipher import AES

    cipher = AES.new(key, AES.MODE_ECB)
    with open(src, "rb") as fin, open(dst, "wb") as fout:
        while True:
            chunk = fin.read(1024 * 1024)
            if not chunk:
                break
            # ECB 要求 16 字节对齐；钉钉的库天然 4096 对齐，这里只是防御
            pad = (-len(chunk)) % 16
            fout.write(cipher.decrypt(chunk + b"\x00" * pad))


def main() -> int:
    parser = argparse.ArgumentParser(description="Decrypt DingTalk V3 database")
    parser.add_argument("--src", required=True, help="加密的 dingtalk.db 路径")
    parser.add_argument("--dst", required=True, help="解密输出路径")
    parser.add_argument("--uid", required=True, help="用户 uid (9-10位数字)")
    parser.add_argument("--salt", required=True, help="user_config 里的 salt (32位hex)")
    args = parser.parse_args()

    try:
        key = derive_key(args.uid, args.salt)

        # 解密前先验证密钥对不对: 只解密前 64 字节看文件头
        # 这是工具层的"快速失败" —— 235MB 全解开才发现密钥错就太晚了
        from Crypto.Cipher import AES
        head = AES.new(key, AES.MODE_ECB).decrypt(open(args.src, "rb").read(64))
        if not head.startswith(b"SQLite format 3"):
            print(json.dumps({
                "ok": False,
                "error": "key_invalid",
                "detail": "解密后文件头不是 SQLite 格式, uid 或 salt 有误"
            }, ensure_ascii=False))
            return 2  # 退出码 2 = 密钥错误, 宿主可以区分"密钥错"和"文件错"

        decrypt_file(args.src, args.dst, key)
        print(json.dumps({
            "ok": True,
            "dst": args.dst
        }, ensure_ascii=False))
        return 0

    except FileNotFoundError:
        print(json.dumps({
            "ok": False, "error": "file_not_found", "detail": args.src
        }, ensure_ascii=False))
        return 3
    except Exception as e:  # 工具要兜住所有异常, 绝不能让宿主看到 Python traceback
        print(json.dumps({
            "ok": False, "error": "decrypt_failed", "detail": str(e)
        }, ensure_ascii=False))
        return 1


if __name__ == "__main__":
    sys.exit(main())
