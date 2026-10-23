# -*- coding: utf-8 -*-
"""
api-notifier 的门面 —— 循环决策型 AI skill(复用第二章的 runner)。

契约(与其它 skill 相同的面子):
  stdin  JSON: {"title": "...", "content": "...", "llm": {...可覆盖...}}
  stdout JSON: {"ok": true, "http_status": 200, ...}
  退出码: 0 = 成功, 非 0 = 失败

关键设计 —— "输入"与"配置"分家:
  title/content 是"每次任务的原料", 永远从 stdin 读(宿主每次调用都传),
  不走路 merge_stdin_overrides —— 那层管的是"环境配置", 不该碰任务输入。
  所以本文件自己读 stdin, 这是故意的, 不走路 merge_stdin_overrides。
  但读法必须与 _shared 一致(7.3 修): 用 read_stdin_json 读原始字节 + 钉死 UTF-8。
  以前这里 json.load(sys.stdin) 撞了与 report-writer 同一场编码事故 ——
  环境的 stdin 解码层会把中文变成孤立代理字符, DeepSeek 400 拒收。

  llm 配置仍允许 stdin 覆盖, 目的是测试时能塞 mock 剧本(见 test/mocks/),
  不用动 config.yaml 里的真 key。
"""
import json
import sys
from pathlib import Path

SKILL_DIR = Path(__file__).parent
sys.path.insert(0, str(SKILL_DIR.parent / "_shared"))

from config import deep_merge, load_config, project_root, read_stdin_json, run_guarded  # noqa: E402
from llm import create_llm  # noqa: E402
from runner import run_skill  # noqa: E402


def main() -> int:
    # 输入层: 自己读 stdin(理由见模块 docstring) —— 读法与 _shared 同一入口,
    # 保证编码契约一致, 只是不 merge 进配置。
    incoming = read_stdin_json()

    # 调试模式(debug=true 且 stdin 是空): 给 IDE 手跑一套默认值 + mock 大脑,
    # 不发真请求、不烧真 key。这就是第六章统一配置体系里 debug 段的用途。
    if not incoming and load_config().get("debug"):
        incoming = {
            "title": "调试通知",
            "content": "api-notifier 手跑测试(mock 大脑, 不发真请求)",
            "llm": {"provider": "mock", "mock_script_file": "test/mocks/notifier_default.json"},
        }

    title = incoming.get("title", "")
    content = incoming.get("content", "")
    if not title:
        print(json.dumps({"ok": False, "error": "missing_input: title"}, ensure_ascii=False))
        return 2

    cfg = load_config()
    # API 文档位置: 换通知服务 = 换 config.yaml 里这一个路径, 代码零改动
    doc_file = (cfg.get("notify") or {}).get("api", {}).get("doc_file", "")
    if not doc_file:
        print(json.dumps({"ok": False, "error": "missing_config: notify.api.doc_file",
                          "hint": "config.yaml 的 notify.api.doc_file 指向 API 文档(如 docs/gateway.md)"},
                         ensure_ascii=False))
        return 2
    doc_path = Path(doc_file)
    if not doc_path.is_absolute():
        doc_path = project_root() / doc_file
    if not doc_path.exists():
        print(json.dumps({"ok": False, "error": f"api_doc_not_found: {doc_path}"}, ensure_ascii=False))
        return 2
    api_doc = doc_path.read_text(encoding="utf-8")

    # 大脑: 真 key 走 config, 测试时 stdin 塞 mock 覆盖。
    # mock 剧本是"测试固件", 不属于配置: 配置/输入里只填路径, 这里读成剧本列表
    # (与 dingtalk-locator/run.py 同款处理, create_llm 只认解析后的 script 列表)。
    llm_cfg = deep_merge(cfg["llm"], incoming.get("llm") or {})
    if llm_cfg.get("provider") == "mock":
        script_path = project_root() / llm_cfg.get("mock_script_file", "")
        if not script_path.exists():
            print(json.dumps({"ok": False, "error": f"mock_script_not_found: {script_path}"},
                             ensure_ascii=False))
            return 2
        llm_cfg["script"] = json.loads(script_path.read_text(encoding="utf-8"))["script"]
    llm = create_llm(llm_cfg)

    # 任务 = 输入 + 文档。AI 的全部上下文就这两样, 不给多余信息(少给少错)。
    task = json.dumps({"title": title, "content": content, "api_doc": api_doc},
                      ensure_ascii=False)

    # @人硬约束(7.3): 该网关要求"要@人, atMobiles 里得有号码, 且 content
    # 末尾也必须出现 @+号码"。pony 曾在提示词/文档里写这条规则但 AI 经常不照做
    # —— 提示词是请求, 不是保证。这类确定性规则交给 args_hook 在代码里强制:
    # 无论 AI 拼出什么, 发出去之前必然合规。
    at_mobiles = (cfg.get("notify") or {}).get("api", {}).get("at_mobiles", [])

    def enforce_at_rule(tool, args):
        if tool != "http_request" or not at_mobiles:
            return args
        try:
            body = json.loads(args.get("body", ""))
        except (json.JSONDecodeError, TypeError):
            return args  # body 还不是合法 JSON, 让 AI 自己看到工具报错再改
        body["atMobiles"] = list(at_mobiles)
        content = str(body.get("content", "")).rstrip()
        for m in at_mobiles:
            # 要求的是"末尾"必须有: 中间出现过不算, 结尾没有就补到最后一行
            if not content.endswith("@" + m):
                content += "\n@" + m
        body["content"] = content
        return dict(args, body=json.dumps(body, ensure_ascii=False))

    result = run_skill(SKILL_DIR, task=task, config={}, llm=llm,
                       args_hook=enforce_at_rule)
    print(json.dumps(result, ensure_ascii=False))
    return 0 if result.get("ok") else 1


if __name__ == "__main__":
    sys.exit(run_guarded(main))
