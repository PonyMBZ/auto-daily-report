---
name: dingtalk-locator
description: 在本机自动定位钉钉数据目录并验证解密可用性。当配置缺少钉钉数据路径、或需要校验已有路径是否有效时使用。
version: 0.1.0
---

# 钉钉数据目录定位 Skill

## 职责

在本机找到钉钉桌面版的数据目录（形如 `xxxxxxxxxx_v3`），提取 salt，
并配合 uid 完成一次真实的解密验证（验证闸门）。

## 输入（stdin JSON）

| 字段 | 必填 | 说明 |
|------|------|------|
| llm | 是 | 大脑配置：`{"provider": "mock"}` 或 `{"provider": "deepseek", "api_key": "..."}` |
| uid | 否 | 已知 uid。缺省时由本 skill 想办法获取（调 find_uid） |

## 流程

1. 调 `scan_fs` → 拿到候选目录列表（可能为空）
2. 若 config 中没有 uid → 调 `find_uid` → 拿到 uid
3. 对候选逐个调 `verify`（传 dir 和 uid）
4. 优先采纳 `verify` 返回 `verified=true` 的候选
5. 全部失败 → done 返回 `{"ok": false, "need_manual": true}`，由宿主提示用户手动选择

## 规则

- **绝不臆造路径**：scan_fs 返回空就老实报告空，不允许"猜一个看起来像的"
- **没过验证闸门的候选一律不算数**：目录存在 ≠ 能解密，只有 verify 通过才可采纳
- **失败信息要可行动**：告诉用户"手动选择数据目录"，而不是只说"找不到了"
- 候选有多个时，选 verified=true 的第一个，其余忽略（不追求"最正确的"，追求"第一个能用的"）

## 输出（done 的 result）

成功：`{"dir": "...", "uid": "...", "salt": "...", "db_path": "..."}`
失败：`{"ok": false, "need_manual": true, "reason": "..."}`
