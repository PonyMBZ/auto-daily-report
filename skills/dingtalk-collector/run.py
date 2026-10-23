# -*- coding: utf-8 -*-
"""
dingtalk-collector 的门面 —— 宿主唯一调用的入口。

契约:
  输入: stdin JSON(debug=false) 或 config.yaml(debug=true)
  输出: stdout JSON: {"ok": true, "messages": [...], "truncated": N, ...}
  退出码: 0 = 成功, 非 0 = 失败

流程(代码驱动, 不用 AI —— 见第三章"决策账"): 解密 → 提取 → 交卷。
"""
import datetime
import json
import subprocess
import sys
import tempfile
import time
from pathlib import Path

SKILL_DIR = Path(__file__).parent
sys.path.insert(0, str(SKILL_DIR.parent / "_shared"))

from config import get_dingtalk, load_config, merge_stdin_overrides, project_root, run_guarded  # noqa: E402


def parse_hhmm(since) -> tuple[int, int]:
    """收集起点, 返回 (时, 分)。

    支持两种写法: "9:00"(新, HH:MM) 和 9(旧配置, 纯小时) —— 这是配置格式
    演化的兼容层: 升级格式不能让用户手里的旧配置报错。新代码认旧格式,
    旧代码不认新格式, 所以兼容逻辑永远写在读配置这一侧。"""
    if isinstance(since, str) and ":" in since:
        h, m = since.split(":")
        return int(h), int(m)
    return int(since), 0


def day_range_ms(date_str: str, since) -> tuple[int, int]:
    """把"哪天"翻译成 [当天since起点, 次日since起点) 的毫秒区间。

    疑问: date 参数为什么用字符串 "2026-10-22" 而不是日期对象?
    答: 它来自 YAML 配置, 进来就是字符串; 而且空字符串 "" 表示"今天",
       用字符串能让这个"空=默认"的语义一眼可见。解析失败会抛异常,
       由调用方兜住报配置错误。
    """
    if date_str:
        y, m, d = map(int, date_str.split("-"))
    else:
        now = time.localtime()
        y, m, d = now.tm_year, now.tm_mon, now.tm_mday
    sh, sm = parse_hhmm(since)
    start = int(time.mktime((y, m, d, sh, sm, 0, 0, 0, -1)) * 1000)
    # 结束 = 次日同一时刻。用 time.mktime 算次日而不是 +86400000,
    # 是为了让时区/夏令时由系统时间库兜底, 不自己造轮子
    end = int(time.mktime((y, m, d + 1, sh, sm, 0, 0, 0, -1)) * 1000)
    return start, end


def main() -> int:
    cfg = merge_stdin_overrides(load_config())
    dt = get_dingtalk(cfg)
    cl = cfg.get("collect") or {}

    missing = [k for k in ("db_path", "uid", "salt") if not dt.get(k)]
    if missing:
        hint = ("先运行 dingtalk-locator 定位, 或在 config.yaml 的 dingtalk 段补齐 "
                f"({', '.join(missing)})")
        print(json.dumps({"ok": False, "error": f"missing_config: {missing}", "hint": hint},
                         ensure_ascii=False))
        return 1

    with tempfile.TemporaryDirectory(prefix="dailyreport_") as tmp:
        dec_path = str(Path(tmp) / "dingtalk_dec.db")
        # 第一工位: 解密
        proc = subprocess.run(
            [sys.executable, str(SKILL_DIR / "tools" / "decrypt.py"),
             "--src", dt["db_path"], "--dst", dec_path,
             "--uid", str(dt["uid"]), "--salt", dt["salt"]],
            capture_output=True, text=True, encoding="utf-8",
        )
        dec_result = json.loads(proc.stdout.strip() or "{}")
        if not dec_result.get("ok"):
            print(json.dumps(dec_result, ensure_ascii=False))
            return proc.returncode or 1

        # 第二工位: 提取。date 决定"哪天", 空 = 今天
        convs = cl.get("conversations") or []
        try:
            since, until = day_range_ms(cl.get("date", ""), cl.get("since_hour", "9:00"))
        except ValueError:
            print(json.dumps({"ok": False, "error": "bad_date_format",
                              "hint": f"collect.date 应为 YYYY-MM-DD, 收到: {cl.get('date')}"},
                             ensure_ascii=False))
            return 1
        # action="list_chats": 只列出时间窗内有我发言的会话(前端选会话用),
        # 不传 --conversations, 不做消息提取
        is_list = cl.get("action") == "list_chats"
        cmd2 = [sys.executable, str(SKILL_DIR / "tools" / "extract.py"),
                "--db", dec_path,
                "--since", str(since),
                "--until", str(until),
                "--my-uid", str(dt["uid"]),
                ] + (["--list-chats"] if is_list else [
                    "--conversations", ",".join(convs),
                    "--max", str(int(cl.get("max_messages", 500))),
                ] + ([] if cl.get("only_mine", True) else ["--all-senders"]))
        proc2 = subprocess.run(cmd2, capture_output=True, text=True, encoding="utf-8")
        try:
            result = json.loads(proc2.stdout.strip() or "{}")
        except json.JSONDecodeError:
            result = {"ok": False, "error": "extract_bad_output",
                      "detail": proc2.stdout[:300]}
        if is_list and result.get("ok"):
            # 带上日期回显, 前端知道查的是哪天
            result["date"] = cl.get("date", "") or datetime.date.today().isoformat()

    # 可选: 结果落盘(collect.output_file)。调试链/report-writer 的原料来源;
    # 生产环境由宿主直接消费 stdout, 不落盘
    output_file = cl.get("output_file", "")
    if result.get("ok") and output_file:
        out = project_root() / output_file
        out.parent.mkdir(parents=True, exist_ok=True)
        result["date"] = cl.get("date", "") or datetime.date.today().isoformat()
        out.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")
        result["saved_to"] = str(out)

    print(json.dumps(result, ensure_ascii=False))
    return 0 if result.get("ok") else proc2.returncode or 1


if __name__ == "__main__":
    sys.exit(run_guarded(main))
