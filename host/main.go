package main

// main.go —— 宿主入口。
// 子命令: run <skill名>(手动调一个 skill)、pipeline [日期](完整流水线)。
// 后续小节会在这里加: notify、serve(配置界面)、tray(托盘)。

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("用法: daily-report <命令>")
		fmt.Println("  run <skill名>     手动运行一个 skill 并打印结果")
		fmt.Println("  pipeline [日期]   跑完整流水线(采集→日报), 日期可空=今天")
		fmt.Println("  notify [标题] [内容]  手动测试通知渠道")
		fmt.Println("  daemon [--check]  常驻托盘运行(--check 只诊断不驻留)")
		fmt.Println("  autostart on|off  开关开机自启")
		fmt.Println("  serve             单独启动配置界面(自动开浏览器)")
		os.Exit(1)
	}

	root := projectRoot()

	switch os.Args[1] {
	case "run":
		if len(os.Args) < 3 {
			fmt.Println("用法: daily-report run <skill名>")
			os.Exit(1)
		}
		// 开发期 config.yaml 里 debug=true, skill 自己读配置,
		// 所以 stdin 传个空对象即可。生产模式(debug=false)Go 才需要传完整配置。
		result, code, err := RunSkill(root, os.Args[2], map[string]any{})
		pretty, _ := json.MarshalIndent(result, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n退出码: %d\n", err, code)
			os.Exit(1)
		}
		fmt.Println(string(pretty))
		fmt.Fprintf(os.Stderr, "退出码: %d\n", code)
	case "pipeline":
		// 用法: daily-report pipeline [日期]   日期空 = 今天, 可补写历史如 2026-10-22
		// 注意下标: os.Args[0]=程序本身 [1]="pipeline" [2]=日期 —— 一共才 3 个元素,
		// 这里曾误写成 os.Args[3], 参数永远拿不到(还静默退回"今天", 很难发现)。
		date := ""
		if len(os.Args) >= 3 {
			date = os.Args[2]
		}
		if err := RunPipeline(root, date, nil); err != nil {
			fmt.Fprintln(os.Stderr, "错误:", err)
			os.Exit(1)
		}
	case "notify":
		// 用法: daily-report notify [标题] [内容]   手动测试通知渠道
		title, content := "日报服务", "通知渠道测试"
		if len(os.Args) >= 4 {
			title, content = os.Args[2], os.Args[3]
		}
		cfg, err := LoadHostConfig(root)
		if err != nil {
			fmt.Fprintln(os.Stderr, "错误:", err)
			os.Exit(1)
		}
		errs := NotifyAll(root, cfg, title, content)
		if len(errs) > 0 {
			fmt.Fprintln(os.Stderr, "部分渠道失败:")
			for _, e := range errs {
				fmt.Fprintln(os.Stderr, " -", e)
			}
			os.Exit(1)
		}
	case "daemon":
		// 用法: daily-report daemon [--check]
		// 无参数: 常驻托盘运行; --check: 只打印诊断信息就退出(验证安装用)
		checkOnly := len(os.Args) >= 3 && os.Args[2] == "--check"
		if err := RunDaemon(root, checkOnly); err != nil {
			fmt.Fprintln(os.Stderr, "错误:", err)
			os.Exit(1)
		}
	case "autostart":
		// 用法: daily-report autostart on|off
		if len(os.Args) < 3 || (os.Args[2] != "on" && os.Args[2] != "off") {
			fmt.Println("用法: daily-report autostart on|off")
			os.Exit(1)
		}
		if err := SetAutostart(os.Args[2] == "on"); err != nil {
			fmt.Fprintln(os.Stderr, "错误:", err)
			os.Exit(1)
		}
	case "serve":
		// 用法: daily-report serve   启动配置界面(自动开浏览器, 只监听本机)
		if err := RunConfigServer(root, true); err != nil {
			fmt.Fprintln(os.Stderr, "错误:", err)
			os.Exit(1)
		}
	default:
		fmt.Printf("未知命令: %s\n", os.Args[1])
		os.Exit(1)
	}
}
