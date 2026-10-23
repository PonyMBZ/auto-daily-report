---
name: dingtalk-collector
description: 解密钉钉本地数据库并提取指定时间/会话的消息。当需要获取本机钉钉聊天记录作为日报原料时使用。
version: 0.2.0
---

# 钉钉消息采集 Skill

## 职责

从钉钉本地加密数据库中提取消息：解密（decrypt.py）→ 过滤提取（extract.py）→ 返回结构化消息列表。
**代码驱动，不用 AI**——流程完全确定，没有需要现场决策的分支（决策账见课程第三章）。

## 输入（经 config.yaml 或 stdin）

| 配置段 | 字段 | 说明 |
|--------|------|------|
| dingtalk | db_path / uid / salt | 由 data_dir 自动推导（见 _shared/config.py） |
| collect | conversations | 关注的会话名列表，空 = 全部 |
| collect | since_hour | 每天几点开始算"今天" |
| collect | only_mine | true = 只保留我发的消息（合规默认） |
| collect | max_messages | 提取上限，防 LLM 上下文爆炸 |

## 流程

1. `tools/decrypt.py` 解密数据库到临时目录（密钥先验文件头，错密钥秒拒）
2. `tools/extract.py` 按时间 + 会话 + 发送人过滤，解析消息文本
3. 临时目录自动销毁，原始库绝不修改

## 消息文本解析规则（extract.py）

| contentType | 解析结果 |
|-------------|----------|
| 1 | 文本原文 |
| 2 | `[图片]` |
| 1200 | 富文本 markdown（去引用前缀） |
| 其他 | `[类型N]`（诚实标注，不脑补） |
| 已撤回（recallStatus≠0） | `[已撤回]` |

## 输出

```json
{"ok": true, "messages": [{"time": "13:33", "conversation": "...", "sender": "我", "text": "..."}],
 "truncated": 0, "unmatched_conversations": []}
```

## 已知限制（诚实清单）

- 单聊会话名显示为对方 uid 数字（钉钉本地库不存联系人昵称，需另行解析成员表）
- 未知消息类型统一标注 `[类型N]`，等待补充解析

## 错误约定

退出码：0 成功 / 1 缺配置或提取异常 / 2 密钥错 / 3 库文件不存在
