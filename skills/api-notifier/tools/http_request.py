# -*- coding: utf-8 -*-
"""
http_request —— 通用 HTTP 工具, api-notifier 唯一的手脚。

设计意图: 工具只管"把请求发出去", 不管"请求该怎么拼"。
拼请求是 AI 的活(读文档临场决定), 发请求是代码的活(确定性, 不要 AI)。
这正是第二章"决策账"的又一次应用。

输出协议:
  {"ok": true,  "status": 200, "body": "<截断的响应体>"}   # 收到了任何 HTTP 响应
  {"ok": false, "error": "网络层失败原因"}                  # 连响应都没收到
注意: ok=true 只表示"请求-响应往返成功", 业务成败由 AI 按 status/文档判读。
"""
import argparse
import json
import sys
import urllib.error
import urllib.request

# 契约: stdout 恒为 UTF-8(Windows 管道默认 GBK 会炸, 见 _shared/config.py 的 Q&A)
sys.stdout.reconfigure(encoding="utf-8")


def main() -> int:
    ap = argparse.ArgumentParser(description="通用 HTTP 请求工具")
    ap.add_argument("--method", default="POST", choices=["GET", "POST", "PUT", "DELETE"])
    ap.add_argument("--url", required=True)
    ap.add_argument("--headers", default="{}", help="JSON 字符串, 如 '{\"Content-Type\": \"application/json\"}'")
    ap.add_argument("--body", default="", help="请求体原文(通常是 JSON 字符串)")
    ap.add_argument("--timeout", type=int, default=10, help="秒, 默认 10 —— 工具层兜底, 防止永久挂起")
    args = ap.parse_args()

    try:
        headers = json.loads(args.headers or "{}")
    except json.JSONDecodeError as e:
        print(json.dumps({"ok": False, "error": f"headers 不是合法 JSON: {e}"}, ensure_ascii=False))
        return 2

    data = args.body.encode("utf-8") if args.body else None
    req = urllib.request.Request(args.url, data=data, headers=headers, method=args.method)
    try:
        with urllib.request.urlopen(req, timeout=args.timeout) as resp:
            raw = resp.read(2048).decode("utf-8", errors="replace")  # 响应体只留前 2KB, 够了
            print(json.dumps({"ok": True, "status": resp.status, "body": raw}, ensure_ascii=False))
            return 0
    except urllib.error.HTTPError as e:
        # HTTPError 也是"收到了响应"(4xx/5xx), ok=true 交给 AI 按 status 裁决
        raw = e.read(2048).decode("utf-8", errors="replace")
        print(json.dumps({"ok": True, "status": e.code, "body": raw}, ensure_ascii=False))
        return 0
    except Exception as e:
        print(json.dumps({"ok": False, "error": f"{type(e).__name__}: {e}"}, ensure_ascii=False))
        return 1


if __name__ == "__main__":
    import sys
    sys.exit(main())
