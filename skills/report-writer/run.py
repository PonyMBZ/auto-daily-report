# -*- coding: utf-8 -*-
"""
report-writer 的门面 —— 单次调用型 AI skill。

流程: 读消息(证据) → 读修正层(历史) → 组装提示词 → 调 LLM 一次 → 存盘 → 交卷。
疑问: 为什么这里不用 runner 的 tool/done 协议?
答: 那个协议解决"AI 要调工具"的问题。本 skill 零工具调用, 强行套循环是杀鸡用牛刀。
   判断标准永远是决策账(第三章), 不是"有 AI 就要用循环"。
"""
import datetime
import json
import sys
from pathlib import Path

SKILL_DIR = Path(__file__).parent
sys.path.insert(0, str(SKILL_DIR.parent / "_shared"))

from config import load_config, merge_stdin_overrides, project_root, read_stdin_json, run_guarded  # noqa: E402


def read_corrections(path_str: str) -> str:
    """读修正层。文件不存在 = 零修正, 不报错(首次运行就是这个状态)。"""
    if not path_str:
        return ""
    path = Path(path_str)
    if not path_str or not path.is_absolute():
        path = project_root() / path_str  # 配置里允许写相对项目根的路径
    if not path.exists():
        return ""
    return path.read_text(encoding="utf-8").strip()


def main() -> int:
    cfg = merge_stdin_overrides(load_config())
    rp = cfg.get("report") or {}

    # 消息来源: 生产 = stdin 由宿主传入(经 read_stdin_json, 与配置层共用同一次读取);
    # 调试 = config 里指定采集结果文件。
    # 注意保留整份 payload 而不只取 messages —— 素材自带的 date 字段是标题的依据
    payload = None
    if cfg.get("debug"):
        mf = rp.get("messages_file", "")
        if mf:
            payload = json.loads((project_root() / mf).read_text(encoding="utf-8"))
    else:
        candidate = read_stdin_json()
        if candidate.get("messages"):
            payload = candidate
    if not payload or not payload.get("messages"):
        print(json.dumps({"ok": False, "error": "no_messages",
                          "hint": "config.yaml 的 report.messages_file 指向 collector 的输出文件"},
                         ensure_ascii=False))
        return 1

    corrections = read_corrections(rp.get("corrections", "data/corrections.md"))

    # 日报用哪天: report.date 优先(补写历史) → 素材自带 date(collector 落盘时打的)
    # → 今天。素材日期是"证据的时间", 标题必须忠于证据 —— 这正是你发现的问题的修复原则。
    report_date = rp.get("date", "") or payload.get("date", "") \
        or datetime.date.today().isoformat()

    # 组装提示词: SKILL.md(身份+纪律) + summarize.md(详细规范) + 素材
    skill_md = (SKILL_DIR / "SKILL.md").read_text(encoding="utf-8")
    summarize = (SKILL_DIR / "prompts" / "summarize.md").read_text(encoding="utf-8")

    user_payload = json.dumps({
        "date": report_date,
        "messages": payload["messages"],
        "corrections": corrections or "(暂无历史修正)",
    }, ensure_ascii=False)

    from llm import create_llm
    llm = create_llm(cfg["llm"])
    report = llm.chat([
        {"role": "system", "content": f"{skill_md}\n\n---\n\n{summarize}"},
        {"role": "user", "content": user_payload},
    ]).strip()

    # 存盘: reports/ 目录按"日报所属日期"归档(不是按生成时间),
    # 同一天重复生成会覆盖 —— 最新版为准, 历史版可用 git 找回
    out_dir = project_root() / "reports"
    out_dir.mkdir(exist_ok=True)
    out_path = out_dir / f"{report_date}.md"
    out_path.write_text(report, encoding="utf-8")

    print(json.dumps({"ok": True, "report_path": str(out_path), "report": report,
                      "report_date": report_date,
                      "corrections_applied": bool(corrections)}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    # 兜底在 _shared.run_guarded(统一入口, 7.3 审计收口)
    sys.exit(run_guarded(main))
