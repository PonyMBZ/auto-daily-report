# -*- coding: utf-8 -*-
"""
工具: extract —— 从解密后的钉钉库里提取"指定时间之后、指定会话"的消息。

侦察结论(写代码前必须做的功课, 都注释在这):
- 消息分散在 tbmsg_000 ~ tbmsg_127 共 128 张分片表(按会话哈希分片), 没法预知
  某个会话在哪张表, 只能全部扫 —— 好消息是总共 8 万多行, 全扫也就 1 秒
- contentType: 1=文本 2=图片 1200=富文本(markdown) 其他=暂不解析
- content 字段本身是 JSON 字符串, 要二次解析
- createdAt 是毫秒时间戳(Unix epoch * 1000)
- 单聊 cid 形如 "uid1:uid2"(含自己), 群聊 cid 是纯数字、会话表 title=群名
- 单聊的 title 是对方 uid, 昵称要经 tbuser_profile_v2 翻译(见 load_profiles)
- recallStatus != 0 表示已撤回(但原文还在库里! 默认要过滤掉)

教训(pony 质疑"单聊真的没昵称吗"后深挖发现):
最早侦察只翻了会话表就下结论"没有昵称表" —— 错误。库里其实有
tbuser_profile_v2(uid→nick, 154 行)。侦察要搜遍 sqlite_master 全表清单
再下结论, 尤其是这种名字不直观(user_profile)的表。
"""
import argparse
import json
import sqlite3
import sys

# 契约: stdout 恒为 UTF-8(Windows 管道默认 GBK 会炸链, 详见 _shared/config.py 的 Q&A)
sys.stdout.reconfigure(encoding="utf-8")
import time
from pathlib import Path

# 消息类型解析表: contentType → (解析方式)
# 设计: 只解析我们看得懂的类型, 看不懂的统一标注 "[类型N]" —— 绝不脑补内容
TYPE_TEXT = 1
TYPE_IMAGE = 2
TYPE_RICHTEXT = 1200


def load_conversations(con: sqlite3.Connection) -> dict:
    """cid → (title, 是否群聊)。这是消息表与会话表之间的翻译字典。"""
    convs = {}
    for cid, title, ctype in con.execute("SELECT cid, title, type FROM tbconversation"):
        convs[cid] = {"title": title or cid, "is_group": ctype == 2}
    return convs


def load_profiles(con: sqlite3.Connection) -> dict:
    """uid → 昵称。单聊的会话表 title 是对方 uid, 人名要从这张用户资料表翻译。

    表不存在(钉钉版本差异)时返回空字典 —— 翻译不了就退回 uid 显示,
    功能不致命。nick 为空串的也跳过(没有昵称资料的机器人账号等)。"""
    try:
        tables = {r[0] for r in con.execute(
            "SELECT name FROM sqlite_master WHERE type='table'")}
        if "tbuser_profile_v2" not in tables:
            return {}
        return {str(uid): nick for uid, nick in con.execute(
            "SELECT uid, nick FROM tbuser_profile_v2") if nick}
    except sqlite3.Error:
        return {}


def humanize_titles(convs: dict, profiles: dict, my_uid: str) -> None:
    """把单聊的 title(对方 uid) 原地翻译成昵称, 翻译不了的保留 uid。

    单聊 cid 形如 "uid1:uid2", 两个 uid 里排除自己就是对方。
    翻译后: 配置可以写"同事小李"命中单聊(不用查 uid), 列表/日报里显示人名。"""
    for cid, info in convs.items():
        if info["is_group"] or ":" not in str(cid):
            continue
        others = [u for u in str(cid).split(":") if u != str(my_uid)]
        if others and others[0] in profiles:
            info["title"] = profiles[others[0]]


def parse_content(content_type: int, raw: str, recall: int) -> str:
    """把钉钉的原始 content 解析成人可读文本。

    原则: 只提取有证据的内容。解析失败/类型未知 → 明确标注, 不编造。
    """
    if recall != 0:
        return "[已撤回]"
    try:
        data = json.loads(raw) if raw else {}
    except json.JSONDecodeError:
        return f"[无法解析的消息 type={content_type}]"

    if content_type == TYPE_TEXT:
        return data.get("text", "")
    if content_type == TYPE_IMAGE:
        return "[图片]"
    if content_type == TYPE_RICHTEXT:
        # 富文本: attachments[0].extension 是另一个 JSON 字符串, 里面有 markdown 字段
        try:
            ext = json.loads(data["attachments"][0]["extension"])
            md = ext.get("markdown", "")
            # 引用消息带 "> " 前缀, 去掉它让日报更干净
            return "\n".join(line.lstrip("> ") for line in md.splitlines()).strip()
        except (KeyError, IndexError, json.JSONDecodeError, TypeError):
            return "[富文本-解析失败]"
    return f"[类型{content_type}]"


def extract(db_path: str, since_ms: int, until_ms: int, wanted_titles: set, my_uid: str,
            only_mine: bool, max_messages: int) -> dict:
    con = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)  # 只读打开, 绝不写库
    convs = load_conversations(con)
    humanize_titles(convs, load_profiles(con), my_uid)

    # 把"会话名清单"翻译成"cid 集合"。匹配规则: 包含匹配 —— 配置写"同事小李"
    # 能命中标题含"同事小李"的任何会话。单聊 title 已翻译成人名(见 humanize_titles),
    # 写昵称即可; 资料表里没有的人(翻译失败)才需要写 uid。
    # 翻译不出来的名字记录在 unmatched 里报给用户, 不静默吞掉。
    wanted_cids, unmatched = set(), []
    if wanted_titles:
        for cid, info in convs.items():
            if any(t in info["title"] for t in wanted_titles):
                wanted_cids.add(cid)
        unmatched = [t for t in wanted_titles
                     if not any(t in i["title"] for i in convs.values())]

    # 找出所有消息分片表
    tables = [r[0] for r in con.execute(
        "SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'tbmsg_%'")]

    messages = []
    for t in tables:
        # 疑问: 为什么是 createdAt >= since AND createdAt < until 而不是 <=?
        # 答: 半开区间 [since, until) 是时间范围的标准做法 —— 相邻两天的区间首尾相接
        #    不重叠也不漏: 昨天 [9:00, 今天9:00) + 今天 [9:00, 明天9:00), 一条消息不会
        #    同时落进两天, 也不会两天都落空。这是处理连续区间的通用技巧, 不止时间。
        rows = con.execute(
            f'SELECT cid, senderId, contentType, content, createdAt, recallStatus '
            f'FROM "{t}" WHERE createdAt >= ? AND createdAt < ?',
            (since_ms, until_ms)).fetchall()
        for cid, sender, ctype, content, created_at, recall in rows:
            if wanted_cids and cid not in wanted_cids:
                continue
            if only_mine and str(sender) != str(my_uid):
                continue  # 合规默认: 日报只基于"我自己说过的话"
            conv = convs.get(cid, {"title": cid, "is_group": False})
            messages.append({
                "time": time.strftime("%H:%M", time.localtime(created_at / 1000)),
                "conversation": conv["title"],
                "sender": "我" if str(sender) == str(my_uid) else str(sender),
                "text": parse_content(ctype, content or "", recall or 0),
            })

    con.close()
    # 按时间排序; 超长截断并留痕(防止某天群聊爆炸把 LLM 上下文撑爆)
    messages.sort(key=lambda m: m["time"])
    truncated = max(0, len(messages) - max_messages)
    return {"messages": messages[:max_messages], "truncated": truncated,
            "unmatched_conversations": unmatched}


def chats_in_range(db_path: str, since_ms: int, until_ms: int, my_uid: str) -> list:
    """按会话汇总时间窗内"我"的消息 —— 前端"选会话"功能的数据源。

    为什么只统计我的消息: 与采集语义一致(日报只基于自己说过的话);
    我在某天某会话里一条没说, 那个会话对日报就没有贡献, 不必列出。
    单聊 title 原本是对方 uid, 经 tbuser_profile_v2 翻译成人名后展示;
    翻译不了的(对方资料不在库里)退回 uid, preview(我最后一条消息摘要)
    是第二辨认手段。
    """
    con = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)  # 只读, 绝不写库
    convs = load_conversations(con)
    humanize_titles(convs, load_profiles(con), my_uid)
    tables = [r[0] for r in con.execute(
        "SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'tbmsg_%'")]
    stats = {}  # cid -> {"count", "preview", "last"}
    for t in tables:
        rows = con.execute(
            f'SELECT cid, senderId, contentType, content, createdAt '
            f'FROM "{t}" WHERE createdAt >= ? AND createdAt < ?',
            (since_ms, until_ms)).fetchall()
        for cid, sender, ctype, content, created_at in rows:
            if str(sender) != str(my_uid):
                continue
            text = parse_content(ctype, content or "", 0)
            s = stats.setdefault(cid, {"count": 0, "preview": "", "last": 0})
            s["count"] += 1
            if created_at >= s["last"]:
                s["last"] = created_at
                s["preview"] = text
    con.close()

    out = []
    for cid, s in stats.items():
        info = convs.get(cid, {"title": cid, "is_group": False})
        preview = s["preview"]
        if len(preview) > 24:
            preview = preview[:24] + "…"
        kind = "群聊" if info["is_group"] else "单聊"
        out.append({
            "key": info["title"],  # 存入 conversations 的值(包含匹配用)
            "is_group": info["is_group"],
            "count": s["count"],
            "label": f"{kind} · {info['title'][:20]} · 我发了 {s['count']} 条"
                     f" · 最后: {preview}",
        })
    out.sort(key=lambda c: -c["count"])  # 发言多的排前面, 常用的好找
    return out


def main() -> int:
    parser = argparse.ArgumentParser(description="从解密库提取消息")
    parser.add_argument("--db", required=True, help="解密后的数据库路径")
    parser.add_argument("--since", required=True, type=int, help="起始毫秒时间戳(含)")
    parser.add_argument("--until", required=True, type=int, help="结束毫秒时间戳(不含)")
    parser.add_argument("--conversations", default="", help="逗号分隔的会话名, 空=全部")
    parser.add_argument("--my-uid", required=True, help="我的 uid")
    parser.add_argument("--all-senders", action="store_true",
                        help="保留所有人的消息(默认只保留我的, 合规)")
    parser.add_argument("--max", type=int, default=500, help="最多返回条数")
    parser.add_argument("--list-chats", action="store_true",
                        help="列出时间窗内有我发言的会话(选会话功能), 不提取消息")
    args = parser.parse_args()

    try:
        if args.list_chats:
            chats = chats_in_range(args.db, args.since, args.until, args.my_uid)
            print(json.dumps({"ok": True, "chats": chats}, ensure_ascii=False))
            return 0
        result = extract(
            db_path=args.db, since_ms=args.since, until_ms=args.until,
            wanted_titles={s.strip() for s in args.conversations.split(",") if s.strip()},
            my_uid=args.my_uid, only_mine=not args.all_senders,
            max_messages=args.max,
        )
        print(json.dumps({"ok": True, **result}, ensure_ascii=False))
        return 0
    except Exception as e:
        print(json.dumps({"ok": False, "error": "extract_failed", "detail": str(e)},
                         ensure_ascii=False))
        return 1


if __name__ == "__main__":
    sys.exit(main())
