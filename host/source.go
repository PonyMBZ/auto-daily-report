package main

// source.go —— 第七章: 数据源插件层。
//
// 设计决策备忘#2 的落地: "数据源抽象成接口"。
// 核心思想一句话: report-writer(下游) 只认 []Message,
// 消息来自钉钉 / git / 手动粘贴, 它一概不知、也不用知。
// 加新数据源 = 实现 Source 接口 + 注册一个 case, 管道代码一行不改。
//
// 契约为什么带 Source 字段: 多源合并后要能追溯"这条消息哪来的",
// 否则 AI 蒸馏出问题时, 人无法回查证据出自哪个来源。

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Message 统一消息格式 —— 所有数据源的原料都必须长这样。
// 字段与 collector 的输出一致(契约对称), 只多一个 Source 标记。
type Message struct {
	Time         string `json:"time"`
	Conversation string `json:"conversation"`
	Sender       string `json:"sender"`
	Text         string `json:"text"`
	Source       string `json:"source"` // dingtalk / paste / git(7.2)
}

// Source 数据源接口。
// 疑问: 就一个方法, 也值得定义接口?
// 答: 接口的价值不在方法数量, 在于把"有哪些来源"从管道里挪出来。
//
//	没有接口时, RunPipeline 里写着 if 钉钉 else if git —— 每加一个
//	来源就要改管道(违反开闭原则); 有了接口, 管道只面对"一组 Source",
//	不关心每个 Source 内部怎么干活。
type Source interface {
	Name() string
	// Fetch 采集 date(YYYY-MM-DD, 空=今天) 当天该来源的消息。
	Fetch(root, date string) ([]Message, error)
}

// DingtalkSource 钉钉来源: 调 collector skill, 把它的输出翻译成本文件的契约格式。
// 翻译层很薄 —— skill 的输出格式就是照 Message 设计的(契约对称), 这里只做
// 字段搬运 + 打上 source 标记。翻译失败(格式漂移)是显式错误, 不静默吞。
type DingtalkSource struct{}

func (DingtalkSource) Name() string { return "dingtalk" }

func (DingtalkSource) Fetch(root, date string) ([]Message, error) {
	payload, code, err := RunSkill(root, "dingtalk-collector", map[string]any{
		"debug":   false,
		"collect": map[string]any{"date": date},
	})
	if err != nil || code != 0 || !isOK(payload) {
		return nil, fmt.Errorf("collector 失败 (err=%v, 退出码=%d, 输出=%v)", err, code, payload)
	}
	msgs, err := messagesFromPayload(payload, "dingtalk")
	if err != nil {
		return nil, fmt.Errorf("collector 输出格式不符契约: %w", err)
	}
	return msgs, nil
}

// GitSource git 提交来源(7.2): 调 git-collector skill。
// 注意这里的"翻译层"与 DingtalkSource 结构一模一样 —— 这正是 7.1 接口投资的
// 回报: 管道代码对来源的访问收敛成"RunSkill + messagesFromPayload"一个模式,
// 新增来源时宿主只多一个 20 行的 case。
type GitSource struct{}

func (GitSource) Name() string { return "git" }

func (GitSource) Fetch(root, date string) ([]Message, error) {
	payload, code, err := RunSkill(root, "git-collector", map[string]any{
		"debug": false,
		"git":   map[string]any{"date": date},
	})
	if err != nil || code != 0 || !isOK(payload) {
		return nil, fmt.Errorf("git-collector 失败 (err=%v, 退出码=%d, 输出=%v)", err, code, payload)
	}
	msgs, err := messagesFromPayload(payload, "git")
	if err != nil {
		return nil, fmt.Errorf("git-collector 输出格式不符契约: %w", err)
	}
	return msgs, nil
}

// PasteSource 手动粘贴来源。
// 注意它**不实现 Source 接口**: 它的内容不是"按日期采集"来的,
// 而是用户当场在界面上贴进来的 —— 生命周期不同(一次手动触发, 不是定时),
// 硬套 Fetch(root, date) 反而要伪造参数。接口是工具不是教条:
// 长得像但生命周期不同的东西, 不该共用一个抽象。
// (这正是第二章"决策账"的思维: 先算这笔抽象的账, 再决定写不写。)
func pasteMessages(text string) []Message {
	if text = strings.TrimSpace(text); text == "" {
		return nil
	}
	return []Message{{
		// 粘贴内容不分行不切割 —— 原文整段交给你 AI, 它有能力自己分条
		Conversation: "手动粘贴",
		Sender:       "我",
		Source:       "paste",
		Text:         text,
	}}
}

// messagesFromPayload 把 skill 的 stdout(payload["messages"])翻译成 []Message。
// 用 JSON 往返而不是手工逐个断言: 两边结构同源, 往返一次搞定,
// 且格式漂移时错误从 Unmarshal 自然冒出, 不用自己写校验。
func messagesFromPayload(payload SkillResult, source string) ([]Message, error) {
	raw, err := json.Marshal(payload["messages"])
	if err != nil {
		return nil, err
	}
	var msgs []Message
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return nil, err
	}
	for i := range msgs {
		msgs[i].Source = source
	}
	return msgs, nil
}

// asStringList 读配置里的字符串列表(如 sources: ["dingtalk", "git"])。
// 缺字段/类型不符返回 nil —— 配置的容错原则: 缺了用默认值, 不报错。
func asStringList(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// enabledSources 按 config.yaml 的 sources 列表组装启用的数据源。
// 配置演化兼容: 旧配置没有 sources 字段 → 默认只有钉钉(行为与第六章一致)。
// 未知来源名: 显式报错 —— 配置写错要当场喊出来, 不静默跳过
// (跳过的代价是用户以为 git 来源生效了, 实际从没跑过)。
func enabledSources(cfg map[string]any) ([]Source, error) {
	names := asStringList(cfg["sources"])
	if len(names) == 0 {
		names = []string{"dingtalk"}
	}
	var out []Source
	for _, n := range names {
		switch n {
		case "dingtalk":
			out = append(out, DingtalkSource{})
		case "git":
			out = append(out, GitSource{})
		default:
			return nil, fmt.Errorf("未知的数据来源: %q (config.yaml 的 sources 目前支持 dingtalk / git)", n)
		}
	}
	return out, nil
}
