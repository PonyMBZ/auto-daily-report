package main

// pipeline.go —— 流水线编排: collector → report-writer (+ 降级)。
//
// 编排层是宿主里"最薄"的一层: 它不做任何业务(解密/总结都在 skill 里),
// 只做三件事 —— 排序(谁先谁后)、重试(网络抖动)、降级(保证下班必有产出)。
//
// 教学点: 编排 = 调度, 不是实现。
// 就像你在 Lua 里用一个主协程把几个函数按顺序串起来 ——
// 串流程的代码自己不知道"怎么解密", 它只知道"先调 A, A 成了再调 B"。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	reportRetries    = 2               // DeepSeek 失败后的重试次数(设计决策备忘#4: 重试 2 次)
	retryBackoffBase = 5 * time.Second // 退避基数: 第 1 次等 5s, 第 2 次等 10s(线性退避)
)

// isOK 从 skill 结果里取 ok 字段。
// 疑问: 为什么不能只看退出码?
// 答: 两种失败形态要分开看 ——
//  1. 进程直接崩溃(traceback): stdout 为空, 只有退出码非 0;
//  2. skill 捕获了错误, 结构化返回 {"ok": false, "error": "..."}, 退出码也可能非 0。
//     判断"成功"必须双保险: 退出码 == 0 且 ok == true。
func isOK(result SkillResult) bool {
	ok, _ := result["ok"].(bool)
	return ok
}

// RunPipeline 跑完整条日报流水线。date 为空 = 今天(格式 YYYY-MM-DD)。
// extra 是调用方附带的额外消息(如界面上手动粘贴的内容), 可为 nil。
func RunPipeline(root, date string, extra []Message) error {
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}

	// ---- 第一工位: 多源采集(第七章) ----
	// 每个启用的来源各采一筐, 全部合成一筐 []Message 再进生成工位。
	// 单个来源失败不拖垮全局(日志留痕、继续用其它来源); 全部失败才中止 ——
	// 降级也只降"生成"这一环, 采集端不能无中生有。
	cfg, err := LoadHostConfig(root)
	if err != nil {
		return fmt.Errorf("读配置失败, 流水线中止: %w", err)
	}
	sources, err := enabledSources(cfg)
	if err != nil {
		return err
	}
	var msgs []Message
	failures := 0
	for _, src := range sources {
		got, ferr := src.Fetch(root, date)
		if ferr != nil {
			failures++
			fmt.Fprintf(os.Stderr, "[pipeline] 来源 %s 采集失败: %v\n", src.Name(), ferr)
			continue
		}
		fmt.Fprintf(os.Stderr, "[pipeline] 来源 %s: %d 条消息\n", src.Name(), len(got))
		msgs = append(msgs, got...)
	}
	if failures == len(sources) && len(extra) == 0 {
		return fmt.Errorf("全部 %d 个数据来源采集失败, 流水线中止", failures)
	}
	msgs = append(msgs, extra...)

	fmt.Fprintf(os.Stderr, "[pipeline] 采集完成: %d 条消息 (%s)\n", len(msgs), date)
	if len(msgs) == 0 {
		// 零消息日直接收工: 没原料就不烧 DeepSeek 额度, 也不生成空壳日报。
		// 实测踩坑: 不拦这一道, 空消息会一路传下去, 最后产出一份"降级的空日报"。
		fmt.Fprintln(os.Stderr, "[pipeline] 今日无消息, 无需生成日报")
		return nil
	}

	// ---- 第二工位: 生成日报(带重试) ----
	// 与上一站的契约仍是"payload 原样作 stdin", 只是 payload 改由我们组装:
	// {debug, date, messages} —— report-writer 生产模式需要的就这三样。
	rwInput := map[string]any{
		"debug":    false,
		"date":     date,
		"messages": msgs,
	}
	// DR_BREAK_AI=1 是教学用的故障注入开关: 塞一个假 key 覆盖真 key,
	// 用来演练"DeepSeek 挂了"时的降级路径, 不用真的去拔网线。
	if os.Getenv("DR_BREAK_AI") == "1" {
		rwInput["llm"] = map[string]any{"api_key": "broken-key-for-degrade-test"}
	}
	var report SkillResult
	var code int
	success := false
	for attempt := 0; attempt <= reportRetries; attempt++ {
		if attempt > 0 {
			wait := retryBackoffBase * time.Duration(attempt)
			fmt.Fprintf(os.Stderr, "[pipeline] 生成失败, %v 后第 %d 次重试...\n", wait, attempt)
			time.Sleep(wait)
		}
		report, code, err = RunSkill(root, "report-writer", rwInput)
		if err == nil && code == 0 && isOK(report) {
			success = true
			break
		}
		// 失败日志必须带 skill 自己说的原因(7.3 修): 以前只打 err/退出码,
		// 排障时看不到 report-writer 在错误 JSON 里写的 llm_error/detail,
		// 一次真实故障(代理环境导致 API 不通)只能靠猜。
		detail := ""
		if report != nil {
			if e, ok := report["error"].(string); ok {
				detail = " " + e
			}
			if d, ok := report["detail"].(string); ok && d != "" {
				detail += ": " + d
			}
			if se, ok := report["_stderr"].(string); ok && se != "" {
				detail += " [stderr] " + se
			}
		}
		fmt.Fprintf(os.Stderr, "[pipeline] 第 %d 次生成失败 (err=%v, 退出码=%d)%s\n",
			attempt+1, err, code, detail)
	}

	// ---- 降级通道(设计决策备忘#4) ----
	// DeepSeek 连续失败也不能让今天白干: 把原始消息排好版落盘,
	// 至少下班时有一份"能看的东西", 人再手动整理。
	rawPath := ""
	if !success {
		var rawErr error
		rawPath, rawErr = writeRawReport(root, date, msgs, reportRetries+1)
		if rawErr != nil {
			return fmt.Errorf("生成失败 %d 次, 且降级落盘也失败: %v", reportRetries+1, rawErr)
		}
		fmt.Fprintf(os.Stderr, "[pipeline] ⚠ 已降级: AI 生成 %d 次全部失败, 原始消息存到 %s\n",
			reportRetries+1, rawPath)
		// 降级时更要主动通知人来看: 此时没有 AI 整理的成品,
		// 不通知就等于没采集。
		notifyReport(root, fmt.Sprintf("⚠ 日报降级 (%s)", date),
			"AI 生成失败, 原始消息已存:\n"+rawPath)
		return nil
	}

	path, _ := report["report_path"].(string)
	fmt.Fprintf(os.Stderr, "[pipeline] 日报已生成: %s\n", path)
	fmt.Println(report["report"])
	// 通知内容 = 日报全文(7.3 修): 旧版只发文件路径, 手机上看路径还要回电脑开文件,
	// 通知的价值在于"一眼看到内容"。截断是各渠道自己的职责(toast 截 200 字,
	// api 渠道发全文 markdown), 编排层不做取舍。
	reportText, _ := report["report"].(string)
	notifyReport(root, fmt.Sprintf("日报已生成 (%s)", date), reportText)
	return nil
}

// notifyReport 通知所有启用的渠道。通知失败只打日志、绝不中止流水线 —
// 日报已经生成落盘了, 弹窗失败不该让流程报错(通知是附加值)。
func notifyReport(root, title, content string) {
	cfg, err := LoadHostConfig(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[notify] 读配置失败, 跳过通知: %v\n", err)
		return
	}
	for _, e := range NotifyAll(root, cfg, title, content) {
		fmt.Fprintf(os.Stderr, "[notify] 渠道失败: %s\n", e)
	}
}

// writeRawReport 降级落盘: 把原始消息按时间排成最朴素的 Markdown。
// 命名用 "-raw" 后缀而不是覆盖正式日报 —— 两者并存, 人看到 raw 就知道是降级产物。
func writeRawReport(root, date string, msgs []Message, failedAttempts int) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# 工作日报 (%s) — 原始消息(未整理)\n\n", date)
	fmt.Fprintf(&b, "> ⚠ 降级通道生成: AI 总结连续 %d 次失败, 以下为原始消息, 请人工整理。\n\n", failedAttempts)

	for _, m := range msgs {
		fmt.Fprintf(&b, "- **%s** [%s|%s] %s: %s\n", m.Time, m.Source, m.Conversation, m.Sender, m.Text)
	}

	outDir := filepath.Join(root, "reports")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	outPath := filepath.Join(outDir, date+"-raw.md")
	if err := os.WriteFile(outPath, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return outPath, nil
}
