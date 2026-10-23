# -*- coding: utf-8 -*-
"""生成 README 配图: 架构图 + 每日流程图。扁平风格, 统一色板。"""
import os
import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib.patches import FancyBboxPatch, FancyArrowPatch
from matplotlib import font_manager

# 中文渲染: 用运行时自带的 Noto Sans SC
for env in ("DAIMON_CJK_FONT_REGULAR",):
    p = os.environ.get(env)
    if p and os.path.exists(p):
        font_manager.fontManager.addfont(p)
        plt.rcParams["font.family"] = font_manager.FontProperties(fname=p).get_name()
        break
plt.rcParams["axes.unicode_minus"] = False

C_HOST = "#2563eb"   # Go 宿主
C_SKILL = "#10b981"  # Python skills
C_EXT = "#f59e0b"    # 外部世界(数据源/AI)
C_BG = "#ffffff"
C_TEXT = "#1f2937"


def box(ax, x, y, w, h, text, fc, fontsize=11, tc="white", bold=True):
    ax.add_patch(FancyBboxPatch((x, y), w, h,
                                boxstyle="round,pad=0.02,rounding_size=0.035",
                                fc=fc, ec="none"))
    ax.text(x + w / 2, y + h / 2, text, ha="center", va="center",
            fontsize=fontsize, color=tc,
            fontweight="bold" if bold else "normal", linespacing=1.5)


def arrow(ax, p1, p2, color="#9ca3af", style="-|>", lw=1.6, rad=0.0, ls="-"):
    ax.add_patch(FancyArrowPatch(p1, p2, arrowstyle=style, mutation_scale=14,
                                 color=color, lw=lw,
                                 connectionstyle=f"arc3,rad={rad}", linestyle=ls))


# ================= 图 1: 架构 =================
fig, ax = plt.subplots(figsize=(11.5, 6.6), dpi=160)
ax.set_xlim(0, 11.5); ax.set_ylim(0, 6.6); ax.axis("off")

ax.text(0.35, 6.25, "整体架构", fontsize=17, fontweight="bold", color=C_TEXT)
ax.text(0.35, 5.92, "Go 宿主负责编排(定时/合并/重试/降级), Python skills 负责干活(AI 驱动), 契约是 stdin/stdout JSON",
        fontsize=10.5, color="#6b7280")

# 左: 数据源
box(ax, 0.35, 2.9, 2.3, 2.4, "", C_EXT)
ax.text(1.5, 5.05, "数据源", ha="center", fontsize=12, fontweight="bold", color="white")
for i, t in enumerate(["钉钉本地数据库", "git 提交记录", "手动粘贴文本"]):
    box(ax, 0.55, 4.45 - i * 0.55, 1.9, 0.4, t, "#fbbf24", 9.5, C_TEXT, False)

# 中: Go 宿主
box(ax, 3.3, 1.5, 4.6, 4.3, "", "#eff6ff")
ax.text(5.6, 5.45, "Go 宿主 (host/)", ha="center", fontsize=13, fontweight="bold", color=C_HOST)
for i, (t, y) in enumerate([("daemon — 托盘 + 定时触发 + 单例锁", 4.75),
                            ("pipeline — 多源采集/合并/重试/降级", 4.15),
                            ("web — 本机配置界面 (127.0.0.1)", 3.55),
                            ("source — 数据来源接口(开闭原则)", 2.95),
                            ("notify — 可插拔渠道(toast/api)", 2.35)]):
    box(ax, 3.55, y - 0.33, 4.1, 0.52, t, C_HOST, 9.5, "white", False)
ax.text(5.6, 1.75, "编排 = 调度, 不是实现", ha="center", fontsize=9, color="#6b7280", style="italic")

# 右: skills
box(ax, 8.55, 1.5, 2.6, 4.3, "", "#ecfdf5")
ax.text(9.85, 5.45, "Python skills", ha="center", fontsize=13, fontweight="bold", color=C_SKILL)
for i, t in enumerate(["dingtalk-collector", "git-collector", "dingtalk-locator",
                       "report-writer", "api-notifier"]):
    box(ax, 8.75, 4.75 - i * 0.62 - 0.33, 2.2, 0.5, t, C_SKILL, 9.5, "white", False)
ax.text(9.85, 1.75, "_shared: runner/llm/config", ha="center", fontsize=8.5, color="#059669")

# 底部: 外部世界
box(ax, 3.3, 0.35, 4.6, 0.8, "config.yaml (唯一配置)   ·   reports/ (日报归档)", "#374151", 9.5, "white", False)
box(ax, 0.35, 0.35, 2.3, 0.8, "DeepSeek API", C_EXT, 10)
box(ax, 8.55, 0.35, 2.6, 0.8, "通知: 系统弹窗 / 任意机器人", C_EXT, 9.5)

# 箭头
arrow(ax, (2.65, 4.35), (3.3, 4.35))
arrow(ax, (7.9, 4.35), (8.55, 4.35))
arrow(ax, (5.6, 1.5), (5.6, 1.15), color="#9ca3af")
arrow(ax, (9.85, 1.5), (9.85, 1.15), color="#9ca3af")
arrow(ax, (2.65, 0.75), (3.3, 0.75))
ax.text(2.98, 4.5, "解密采集", fontsize=8, color="#6b7280", ha="center")
ax.text(8.22, 4.5, "RunSkill", fontsize=8, color="#6b7280", ha="center")

plt.tight_layout()
os.makedirs("docs/img", exist_ok=True)
plt.savefig("docs/img/architecture.png", bbox_inches="tight", facecolor=C_BG)
plt.close()

# ================= 图 2: 每日流程 =================
fig, ax = plt.subplots(figsize=(11.5, 5.4), dpi=160)
ax.set_xlim(0, 11.5); ax.set_ylim(0, 5.4); ax.axis("off")
ax.text(0.35, 5.0, "一次自动日报的完整旅程 (18:00 触发)", fontsize=17, fontweight="bold", color=C_TEXT)

steps = [
    ("① 定时触发\ndaemon 到点唤醒", C_HOST),
    ("② 多源采集\ndingtalk 21 条 + git 2 条", C_SKILL),
    ("③ 合并排序\n统一 Message 契约", C_HOST),
    ("④ AI 蒸馏\nreport-writer → DeepSeek", C_SKILL),
    ("⑤ 通知送达\ntoast + api 机器人", C_EXT),
]
x = 0.4
for i, (t, c) in enumerate(steps):
    box(ax, x, 3.1, 1.95, 1.15, t, c, 9.5)
    if i < len(steps) - 1:
        arrow(ax, (x + 1.95, 3.68), (x + 2.25, 3.68))
    x += 2.25

# 降级通道
box(ax, 3.2, 1.15, 5.1, 1.0,
    "任何一步失败?  重试 3 次 → 仍失败则降级: 原始消息落盘 + 强提醒通知\n(保证下班必有东西可看)", "#dc2626", 10)
arrow(ax, (8.05, 3.1), (7.2, 2.15), color="#dc2626", rad=-0.25)
arrow(ax, (5.75, 2.15), (9.2, 3.05), color="#dc2626", rad=-0.25)

ax.text(0.4, 0.45, "单源失败不拖垮全局(钉钉挂了 git 照常) · 单例锁防双 daemon · 全程日志带原因, 不靠猜",
        fontsize=10, color="#6b7280")

plt.tight_layout()
plt.savefig("docs/img/flow.png", bbox_inches="tight", facecolor=C_BG)
plt.close()
print("OK docs/img/architecture.png docs/img/flow.png")
