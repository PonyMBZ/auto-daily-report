# -*- coding: utf-8 -*-
"""
AI 驱动循环的测试 —— 用 Mock 大脑指挥真实工具, 不调 API、不花钱。

运行: python skills/_shared/test_runner.py

注意观察:
- MockLLM 的剧本里写了两句"台词": 先调工具, 再宣布完成
- runner 在两句台词之间真的去执行了 decrypt.py (真解密!)
- 整个循环的"对话"通过 stderr 打印出来, 你能看到完整的思考链
"""
import json
import sys
import tempfile
from pathlib import Path

SHARED = Path(__file__).parent
sys.path.insert(0, str(SHARED))  # 让 import 能找到 _shared 里的模块

from llm import MockLLM          # noqa: E402
from runner import run_skill     # noqa: E402

SKILL_DIR = SHARED.parent / "dingtalk-collector"
CONFIG = json.load(open(SKILL_DIR / "debug_input.json", encoding="utf-8"))

# 解密输出到一个真实文件(测试完手动看), 而不是 TempDirectory 里被清掉
DST = str(SHARED.parent.parent / "test_output" / "dingtalk_dec.db")
Path(DST).parent.mkdir(exist_ok=True)

# Mock 大脑的剧本: 两句台词
script = [
    # 第一句: 决定调用解密工具(参数从配置里抄)
    json.dumps({
        "action": "tool", "tool": "decrypt",
        "args": {
            "src": CONFIG["dingtalk_db"], "dst": DST,
            "uid": CONFIG["uid"], "salt": CONFIG["salt"],
        }
    }, ensure_ascii=False),
    # 第二句: 收到工具结果后, 宣布完成
    json.dumps({
        "action": "done",
        "result": {"decrypted_to": DST, "note": "解密成功, 任务完成"}
    }, ensure_ascii=False),
]

llm = MockLLM(script)
result = run_skill(SKILL_DIR, task="解密配置指定的钉钉数据库", config=CONFIG, llm=llm)

print("\n=== 最终输出 ===")
print(json.dumps(result, ensure_ascii=False, indent=2))

# 验证解密产物真的存在且是有效 SQLite
import sqlite3
con = sqlite3.connect(DST)
n = con.execute("SELECT COUNT(*) FROM tbconversation").fetchone()[0]
con.close()
print(f"\n验证: 解密库打开成功, tbconversation 有 {n} 个会话 ✓")
