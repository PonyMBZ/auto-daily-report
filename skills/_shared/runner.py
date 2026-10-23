# -*- coding: utf-8 -*-
"""
AI 驱动循环引擎 —— 所有 skill 共用的大脑传令兵。

复习区 (前两章的疑问答案, 都注释在这):
Q: tools_dir / "decrypt.py" 里的 "/" 是除法吗?
A: 不是。Path 对象重载了 "/" 运算符表示路径拼接, 等价 Lua 的 .., 且自动处理
   Windows 和 Linux 的斜杠方向差异。

Q: tools_dir.glob("*.py") 是什么?
A: 按模式找文件的迭代器, 等价 Lua 里 lfs.dir + string.match("%.py$")。
   前面包 sorted() 是为了结果按字母序稳定 —— AI 看到的文档顺序固定, 行为更可控。

Q: subprocess.run([...]) 和拼字符串执行有什么区别?
A: 列表形式 ["python", "decrypt.py", "--src", "x"] 不做 shell 解析, 天然免疫
   空格/中文/引号转义问题。我们的路径里有中文, 拼字符串很容易踩坑。

Q: reply.removeprefix("```json") 那串在干嘛?
A: 清洗。真实 LLM 爱把 JSON 包在 markdown 代码块里, json.loads 会报错。
   容忍格式壳(去壳), 不容忍内容错(解析失败直接报错), 这个分寸要守住。

Q: 退出码和 JSON 里的 error 字段不重复吗?
A: 分工不同: 退出码给"程序"(宿主一眼判断成败), error 字段给"人/AI"
   (描述具体原因)。第一章的 0/1/2/3 约定就是这个目的。

用法 (在某 skill 的 run.py 里):
    from _shared.runner import run_skill
    result = run_skill(SKILL_DIR, task="解密数据库", config={...}, llm=llm)

循环协议 (runner 与 LLM 之间的"暗号"):
  LLM 每轮回复必须是一个 JSON 对象, 二选一:
    {"action": "tool", "tool": "<名字>", "args": {...}}   我要调工具
    {"action": "done", "result": {...}}                    我完事了
  其他回复一律视为协议违反, 循环报错退出 —— 绝不猜测 AI 的意图。
"""
import json
import subprocess
import sys
from pathlib import Path

MAX_STEPS = 10  # 保险丝: 防止 AI 陷入死循环把 API 额度烧光


def collect_tool_docs(tools_dir: Path) -> str:
    """收集每个工具的 --help 输出, 拼进给 AI 的说明书里。

    设计意图: 工具的用法由工具自己声明(--help), 而不是写死在提示词里。
    以后你给 decrypt.py 加参数, AI 自动就知道 —— 不用改任何提示词。
    """
    docs = []

    # 遍历 tools/ 下每个 .py
    #   → 跑一遍 "python xxx.py --help"
    #   → 退出码 0(正常) → 把帮助文本收进文档
    #   → 非 0 或超时(异常) → continue 跳过这个工具, 不致命
    # 最后把所有工具的帮助文本拼成一个大字符串 → 这就是 tool_docs

    #     # tools_dir.glob("*.py") —— 找文件
    #     # sorted(...) —— 排序后转成列表 decrypt.py、verify.py
    for tool in sorted(tools_dir.glob("*.py")):
        try:
            # subprocess.run，起一个新进程
            proc = subprocess.run(
                [sys.executable, str(tool), "--help"],
                capture_output=True, text=True, encoding="utf-8", timeout=10,
            )
            if proc.returncode == 0:
                docs.append(f"### {tool.stem}\n{proc.stdout.strip()}")
        except Exception:
            continue  # 某个工具 help 跑挂了不致命, 跳过
    return "\n\n".join(docs)


def build_system_prompt(skill_md: str, tool_docs: str, skill_name: str) -> str:
    return f"""你是 skill "{skill_name}" 的大脑。以下是该 skill 的说明书, 必须严格遵守:

{skill_md}

---

你可以调用以下工具。用法见各工具的 --help:

{tool_docs}

---

每次回复必须是且仅是一个 JSON 对象, 二选一:
  {{"action": "tool", "tool": "<工具名>", "args": {{...}}}}   调用一次工具
  {{"action": "done", "result": {{...}}}}                     任务完成, 给出最终结果

规则:
- 除了这个 JSON, 不要输出任何其他内容(不要解释, 不要 markdown 代码块)
- 工具不够用就直接 done 并说明原因, 不要编造不存在的工具
- 工具返回的结果在下一轮会自动出现在对话里"""


def call_tool(tools_dir: Path, name: str, args: dict) -> dict:
    """执行工具。args 字典转成命令行参数传给 tools/<name>.py。"""
    script = tools_dir / f"{name}.py"
    if not script.exists():
        return {"ok": False, "error": f"tool_not_found: {name}"}
    cmd = [sys.executable, str(script)]
    for k, v in args.items():  # {"src": "x"} -> --src x
        cmd += [f"--{k}", str(v)]
    proc = subprocess.run(cmd, capture_output=True, text=True, encoding="utf-8", timeout=300)
    try:
        return json.loads(proc.stdout.strip() or "{}")
    except json.JSONDecodeError:
        return {"ok": False, "error": "tool_bad_output",
                "detail": proc.stdout[:300], "stderr": proc.stderr[:300]}


def run_skill(skill_dir: Path, task: str, config: dict, llm, args_hook=None) -> dict:
    """AI 驱动循环主体。返回最终 result 字典。

    args_hook(7.3): 调工具前的硬约束执行点 (tool_name, args) -> args。
    分工原则: AI 负责"需要判断的部分"(怎么拼请求), 代码负责"必须恒真的部分"
    (如"要@人则 content 末尾必须有 @手机号"这种确定性规则)。提示词是请求,
    代码是法律 —— 硬约束交给提示词靠概率, 交给 args_hook 是 100%。"""
    skill_dir = Path(skill_dir)
    skill_md = (skill_dir / "SKILL.md").read_text(encoding="utf-8")
    name = skill_md.split("name:")[1].splitlines()[0].strip() if "name:" in skill_md else skill_dir.name
    tools_dir = skill_dir / "tools"
    tool_docs = collect_tool_docs(tools_dir)

    # 对话历史。第一条: 系统提示词(说明书); 第二条: 任务+配置
    messages = [
        {"role": "system", "content": build_system_prompt(skill_md, tool_docs, name)},
        {"role": "user", "content": json.dumps(
            {"task": task, "config": config}, ensure_ascii=False)},
    ]

    for step in range(MAX_STEPS):
        # 7.3 修: llm.chat 的异常(HTTP 错误/网络断/400)以前直接炸穿整个循环,
        # 宿主只看到裸 traceback。循环层接住, 转成结构化错误 —— AI 失败是
        # 预期内事件, 不是程序 bug, 不该以崩溃的形式出现。
        try:
            reply = llm.chat(messages).strip()
        except Exception as e:  # noqa: BLE001 —— 循环层是 AI 调用的边界, 边界兜一切
            return {"ok": False, "error": f"llm_call_failed: {type(e).__name__}",
                    "detail": str(e)[:300]}
        # 容忍 AI 把 JSON 包在 ```json ... ``` 里 —— 常见且无害的偏差
        reply = reply.removeprefix("```json").removeprefix("```").removesuffix("```").strip()
        try:
            action = json.loads(reply)
        except json.JSONDecodeError:
            return {"ok": False, "error": "llm_bad_json", "detail": reply[:300],
                    "hint": "LLM 没有遵守协议, 输出不是 JSON"}

        if action.get("action") == "tool":
            tool_name = action.get("tool", "")
            tool_args = action.get("args", {})
            if args_hook:
                tool_args = args_hook(tool_name, tool_args)
            result = call_tool(tools_dir, tool_name, tool_args)
            # 关键: 把 AI 的决定和工具结果都记入历史, AI 下一轮才能"看见"结果
            messages.append({"role": "assistant", "content": reply})
            messages.append({"role": "user", "content": json.dumps(
                {"tool_result": result}, ensure_ascii=False)})
            print(f"[runner] step {step + 1}: 工具 {action.get('tool')} -> "
                  f"{'ok' if result.get('ok') else result.get('error')}", file=sys.stderr)
            continue

        if action.get("action") == "done":
            print(f"[runner] step {step + 1}: done", file=sys.stderr)
            return {"ok": True, "result": action.get("result", {})}

        return {"ok": False, "error": "llm_bad_action", "detail": reply[:300],
                "hint": "action 必须是 tool 或 done"}

    # 保险丝烧断
    return {"ok": False, "error": "max_steps_exceeded",
            "hint": f"AI 循环超过 {MAX_STEPS} 步未宣布完成"}
