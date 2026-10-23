---
name: git-collector
description: 从本地 git 仓库提取指定日期内的提交记录，作为日报的数据来源之一。当需要把代码提交纳入日报、或配置里启用了 git 来源时使用。
version: 0.1.0
---

# Git 提交采集 Skill

## 职责

按 `config.yaml` 的 `git.repos` 清单，提取指定日期时间窗内、指定作者的提交记录，
输出与 dingtalk-collector 完全同构的消息列表——**它是"数据来源"接口的第二个实现**。

## 输入（stdin JSON 或 config.yaml 的 git 段）

| 字段 | 必填 | 说明 |
|------|------|------|
| git.date | 否 | 采集哪天，空=今天 |
| git.repos | 是 | 仓库清单：`[{path, name?, author?}]` |
| git.author | 否 | 全局提交人过滤（子串匹配），仓库级 author 优先 |
| git.since_hour | 否 | 时间窗起点，默认 "9:00"（与钉钉来源同一语义） |

## 输出（stdout JSON）

```json
{"ok": true, "messages": [{"time","conversation","sender","text"}], "repo_errors": [...]}
```

- `messages` 结构与 collector 一致：`conversation`=仓库名，`sender`=提交者，`text`=提交主题(短哈希)
- 单仓失败不拖垮全局，错误记录在 `repo_errors`；全部失败才 `ok: false`

## 原则

- **工具不做聪明事**：git_log.py 只执行 git log，仓库怎么找、作者是谁，都是 config 说了算
- **编码钉死**：git 子进程显式 `--encoding=UTF-8`，stdout 显式 reconfigure——6.5.7 事故的同款防御
