package main

// notify.go —— 可插拔通知渠道(设计决策备忘#3)。
//
// 教学点: "可插拔"三个字靠的是接口, 不是 if-else。
// 主流程只认 Channel 接口; 新增渠道 = 多实现一个结构体,
// NotifyAll 一行不用改。这就是"对扩展开放, 对修改封闭"。
//
// 渠道失败的策略: 某个渠道失败不阻塞其它渠道 —
// toast 弹不出来不该耽误 API 通知。通知系统要的是"至少一处送达",
// 不是"所有渠道一致成功"(和 pipeline 降级同一个哲学)。

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Channel 通知渠道接口。两个方法, 一个都不能多:
// Name 用于日志和配置对应; Send 是干活。
type Channel interface {
	Name() string
	Send(title, content string) error
}

// ---- 渠道一: 系统通知(Windows 气球通知) ----

// ToastChannel 不依赖任何第三方库: 调 PowerShell 的 NotifyIcon。
// 通知弹在任务栏, 并留在系统通知中心, 点开面板看详情的体验在 6.4 托盘做。
type ToastChannel struct{}

func (ToastChannel) Name() string { return "toast" }

func (ToastChannel) Send(title, content string) error {
	// 气球通知只适合一眼摘要, 长文要看文件去 —— 截断是渠道自己的职责,
	// 调用方不该知道"toast 最多显示多少字"(信息专家原则)。
	const maxLen = 200
	runes := []rune(content)
	if len(runes) > maxLen {
		content = string(runes[:maxLen]) + "..."
	}
	// PowerShell 单引号字符串里, 单引号要翻倍转义('' = 一个字面单引号)。
	// 用户输入直接拼脚本必须转义, 否则一个引号就能注入任意命令。
	esc := func(s string) string { return strings.ReplaceAll(s, "'", "''") }
	script := fmt.Sprintf(`
Add-Type -AssemblyName System.Windows.Forms
$n = New-Object System.Windows.Forms.NotifyIcon
$n.Icon = [System.Drawing.SystemIcons]::Information
$n.BalloonTipTitle = '%s'
$n.BalloonTipText = '%s'
$n.Visible = $true
$n.ShowBalloonTip(8000)
Start-Sleep -Seconds 1
`, esc(title), esc(content))
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("powershell: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ---- 渠道二: API 通知(委托给 api-notifier skill) ----
//
// 6.3 初版这里是一个写死请求字段的 APIChannel —— 新增通知服务要改 Go 代码。
// 6.3.1 重构为策略的极致: 策略的具体算法不再编码在 Go 里,
// 而是住在 skills/api-notifier 的 SKILL.md + API 文档里, 由 AI 临场拼接。
// Go 只负责"把 title/content 递过去", 和 toast 渠道对待弹窗细节一无所知同理。
//
// 代价要说清楚(决策账): 通知现在依赖 DeepSeek 可用, 且每次慢几秒 —
// 一天一两次的通知完全承受得起; toast 渠道不依赖 AI, 互为兜底。
type SkillAPIChannel struct {
	root string // 项目根, RunSkill 需要
}

func (c SkillAPIChannel) Name() string { return "api" }

func (c SkillAPIChannel) Send(title, content string) error {
	// 门面契约对称的第三次出现: notify.go → api-notifier/run.py 的 stdin,
	// 和 pipeline → collector → report-writer 完全是同一种调用。
	result, code, err := RunSkill(c.root, "api-notifier", map[string]any{
		"title":   title,
		"content": content,
	})
	if err != nil {
		return err
	}
	if code != 0 || !isOK(result) {
		return fmt.Errorf("skill 报告失败: %v", result)
	}
	// AI 汇报的 http_status 打到日志: 网关可能 200 里包业务错误, 留条线索好排查
	if status, ok := result["result"].(map[string]any)["http_status"]; ok {
		fmt.Fprintf(os.Stderr, "[notify] api http_status=%v\n", status)
	}
	return nil
}

// ---- 渠道注册表: 配置名 → 构造渠道 ----

// buildChannels 按 config.yaml 的 notify.channels 列表构造启用的渠道。
// channels 配空 = 静默(通知全部关闭), 服务照常跑 —— 通知是附加值, 不是命脉。
func buildChannels(root string, cfg map[string]any) []Channel {
	n := asMap(cfg["notify"])
	var out []Channel
	for _, name := range asStringSlice(n["channels"]) {
		switch name {
		case "toast":
			out = append(out, ToastChannel{})
		case "api":
			out = append(out, SkillAPIChannel{root: root})
		default:
			fmt.Printf("[notify] 跳过未知渠道: %s(检查 config.yaml 的 notify.channels)\n", name)
		}
	}
	return out
}

// NotifyAll 遍历启用渠道发送, 返回每个失败的渠道错误。
// 调用方(如 pipeline)决定失败要不要中止 —— 通知失败从不该让日报流水线失败。
func NotifyAll(root string, cfg map[string]any, title, content string) []string {
	var errs []string
	for _, ch := range buildChannels(root, cfg) {
		if err := ch.Send(title, content); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", ch.Name(), err))
		} else {
			fmt.Fprintf(os.Stderr, "[notify] %s 已送达\n", ch.Name())
		}
	}
	return errs
}
