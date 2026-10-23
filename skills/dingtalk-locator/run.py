# -*- coding: utf-8 -*-
"""
dingtalk-locator 的门面 —— AI 驱动型 skill。

配置统一从 _shared/config.py 读:
  - llm 段: 用哪个大脑(mock/deepseek)、key 在哪、mock 剧本文件
  - dingtalk 段: 已知 uid / data_dir(留空则全靠 AI + 工具现场找)
"""
import json
import sys
from pathlib import Path

SKILL_DIR = Path(__file__).parent
SHARED = SKILL_DIR.parent / "_shared"
sys.path.insert(0, str(SHARED))

from config import get_dingtalk, load_config, merge_stdin_overrides, project_root, run_guarded  # noqa: E402


def main() -> int:
    cfg = merge_stdin_overrides(load_config())

    from llm import create_llm, MockLLM
    llm_spec = dict(cfg.get("llm") or {})
    provider = llm_spec.get("provider", "mock")

    # mock 剧本是"测试固件", 不属于配置, 单独放 test/mocks/ 下, 配置里只填路径
    if provider == "mock":
        script_path = project_root() / llm_spec.get("mock_script_file", "")
        if not script_path.exists():
            print(json.dumps({"ok": False, "error": f"mock_script_not_found: {script_path}"},
                             ensure_ascii=False))
            return 1
        llm = MockLLM(json.loads(script_path.read_text(encoding="utf-8"))["script"])
    else:
        try:
            llm = create_llm(llm_spec)
        except (KeyError, ValueError) as e:
            print(json.dumps({"ok": False, "error": f"bad_llm_config: {e}"}, ensure_ascii=False))
            return 1

    from runner import run_skill
    result = run_skill(
        SKILL_DIR,
        task="定位本机钉钉数据目录, 验证解密可用性, 返回 dir/uid/salt/db_path",
        config={"uid": cfg.get("dingtalk", {}).get("uid", ""), "dingtalk": get_dingtalk(cfg)},
        llm=llm,
    )
    print(json.dumps(result, ensure_ascii=False))
    return 0 if result.get("ok") else 1


if __name__ == "__main__":
    sys.exit(run_guarded(main))
