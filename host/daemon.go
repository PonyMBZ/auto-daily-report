package main

// daemon.go —— 常驻模式: 托盘图标 + 定时触发 + 状态持久化。
//
// 这是"程序"和"服务"的分水岭。前三节的所有子命令都是"跑完就退出",
// daemon 是第一个"不退出"的: 它住在托盘里, 按时叫人干活。
//
// 结构: systray.Run 占住主线程(Windows 消息循环必须的),
// 其它一切(定时器、pipeline)都住 goroutine 里 —— 谁阻塞都不能堵死托盘。

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"fyne.io/systray"
)

//go:embed icon.ico
var iconData []byte

// daemonState 需要跨重启记住的东西: 今天跑过没有。
// 疑问: 为什么要持久化? 答: daemon 可能在你上班途中重启(崩溃/更新/手滑),
// 没有它, 重启后会在同一个触发窗口里再跑一次, 日报被覆盖。
type daemonState struct {
	LastRunDate string `json:"last_run_date"` // "2026-10-23"
}

const stateFile = "data/daemon_state.json"

// acquireDaemonMutex 系统级单例锁: 保证全机只有一个 daemon 在跑。
// 为什么必须(7.3 实测踩坑): 前台手动 `daemon` 和后台自启各起一个,
// 两个调度器到点都触发 —— 日报生成两遍、通知弹两遍、状态文件互相覆盖。
// 用 Windows 命名 mutex 而不是"PID 文件": 进程被 kill 后内核自动释放, 无残留锁;
// CreateMutex 已存在时返回同一对象句柄, 用 WaitForSingleObject(0) 非阻塞试占。
func acquireDaemonMutex() (release func(), err error) {
	name, _ := syscall.UTF16PtrFromString(`Global\daily-report-daemon`)
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	handle, _, _ := kernel32.NewProc("CreateMutexW").Call(0, 0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return nil, fmt.Errorf("创建单例锁失败")
	}
	// 非阻塞等锁: 0 毫秒, 拿到 WAIT_OBJECT_0 才算占有; WAIT_TIMEOUT(0x102) = 别人占着
	const waitTimeout = 0x102
	ret, _, _ := kernel32.NewProc("WaitForSingleObject").Call(handle, 0)
	if ret == waitTimeout {
		syscall.CloseHandle(syscall.Handle(handle))
		return nil, fmt.Errorf("已有另一个日报 daemon 在运行")
	}
	return func() { syscall.CloseHandle(syscall.Handle(handle)) }, nil
}

// RunDaemon 常驻入口。checkOnly=true 时只打印诊断信息(下次触发时间等)就退出,
// 用于安装后验证, 也是"纯逻辑与常驻循环分离"的测试手法。
func RunDaemon(root string, checkOnly bool) error {
	setupDaemonLog(root)

	cfg, err := LoadHostConfig(root)
	if err != nil {
		return err
	}
	schedule := LoadSchedule(cfg)

	if checkOnly {
		now := time.Now()
		next, err := schedule.NextFire(now)
		if err != nil {
			return err
		}
		fmt.Printf("当前时间: %s\n", now.Format("2006-01-02 15:04:05"))
		fmt.Printf("触发规则: 每天 %s 前 %d 分钟\n", schedule.WorkEnd, schedule.LeadMinutes)
		fmt.Printf("下次触发: %s\n", next.Format("2006-01-02 15:04:05"))
		st, _ := loadState(root)
		fmt.Printf("上次触发日期: %s\n", st.LastRunDate)
		on, v := AutostartStatus()
		fmt.Printf("开机自启: %v (%s)\n", on, v)
		return nil
	}

	release, err := acquireDaemonMutex()
	if err != nil {
		return err
	}
	defer release()

	log.Printf("daemon 启动, 项目根: %s", root)
	// 配置界面服务内嵌在 daemon 里(不开浏览器, 托盘菜单点了才开)
	go func() {
		if err := RunConfigServer(root, false); err != nil {
			log.Printf("配置界面启动失败: %v", err)
		}
	}()
	systray.Run(func() { onTrayReady(root, schedule) }, func() { log.Println("daemon 退出") })
	return nil
}

// openConfigUI 托盘菜单"打开配置界面": 界面服务已在 daemon 内嵌运行, 开浏览器即可。
func openConfigUI() {
	if configURL == "" {
		log.Println("配置界面服务还没就绪, 稍后再试")
		return
	}
	openBrowser(configURL)
}

// onTrayReady 托盘初始化(图标/菜单), 然后启动定时循环。
func onTrayReady(root string, schedule Schedule) {
	systray.SetIcon(iconData)
	systray.SetTitle("日报服务")
	systray.SetTooltip("日报服务运行中")

	mRunNow := systray.AddMenuItem("立即生成日报", "马上跑一遍流水线")
	mConfig := systray.AddMenuItem("打开配置界面", "浏览器里修改配置")
	mOpenReports := systray.AddMenuItem("打开报告目录", "")
	mAutoStart := systray.AddMenuItem("开机自启: 关", "点击切换")
	mCheck := systray.AddMenuItem("查看状态", "")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出", "退出日报服务")

	refreshAutoStartMenu(mAutoStart)

	// 菜单事件循环。注意: 点"立即生成"可能跑几分钟(解密+AI),
	// 必须放 goroutine —— 托盘主线程被卡住, 图标会假死。
	go func() {
		for {
			select {
			case <-mRunNow.ClickedCh:
				go triggerPipeline(root, true)
			case <-mConfig.ClickedCh:
				// 界面服务由 daemon 内嵌启动(见 RunDaemon); 这里直接开浏览器
				go openConfigUI()
			case <-mOpenReports.ClickedCh:
				exec.Command("explorer", filepath.Join(root, "reports")).Start()
			case <-mAutoStart.ClickedCh:
				on, _ := AutostartStatus()
				if err := SetAutostart(!on); err != nil {
					log.Printf("切换自启失败: %v", err)
				}
				refreshAutoStartMenu(mAutoStart)
			case <-mCheck.ClickedCh:
				go showStatus(root, schedule)
			case <-mQuit.ClickedCh:
				systray.Quit()
			}
		}
	}()

	// 定时触发循环(另一个 goroutine)
	go schedulerLoop(root, schedule)
}

// schedulerLoop 每天到点触发一次。
// 循环体: 算下次触发 → 睡到点 → 检查今天跑过没 → 跑。
func schedulerLoop(root string, schedule Schedule) {
	for {
		now := time.Now()
		next, err := schedule.NextFire(now)
		if err != nil {
			log.Printf("定时配置错误: %v", err)
			time.Sleep(time.Hour) // 配置错了别空转刷屏, 一小时后再试
			continue
		}
		log.Printf("下次触发: %s", next.Format("2006-01-02 15:04:05"))
		time.Sleep(time.Until(next))

		// 睡醒了先重新算"今天" —— 跨天时 next 还是昨天算的日期
		today := time.Now().Format("2006-01-02")
		st, _ := loadState(root)
		if st.LastRunDate == today {
			log.Printf("今天(%s)已跑过, 跳过", today)
			continue
		}
		triggerPipeline(root, false)
	}
}

// triggerPipeline 跑今天的流水线并记录状态。
// manual=true 表示用户手动点菜单触发的 —— 即使今天跑过也照跑(人是最高优先级)。
func triggerPipeline(root string, manual bool) {
	today := time.Now().Format("2006-01-02")
	st, _ := loadState(root)
	if !manual && st.LastRunDate == today {
		return
	}
	log.Printf("开始生成日报 (%s, 手动=%v)", today, manual)
	if err := RunPipeline(root, "", nil); err != nil {
		log.Printf("流水线失败: %v", err)
		return
	}
	st.LastRunDate = today
	if err := saveState(root, st); err != nil {
		log.Printf("状态保存失败(下次可能重复跑): %v", err)
	}
}

// showStatus 托盘上的"查看状态": 借道 toast 渠道弹汇总。
// 疑问: systray 自己不能弹气泡吗? 答: fyne.io/systray 不提供通知能力,
// 但我们的 toast 渠道就是干这个的 —— 复用已有渠道, 不重复造轮子。
func showStatus(root string, schedule Schedule) {
	next, err := schedule.NextFire(time.Now())
	if err != nil {
		next = time.Time{}
	}
	auto := "关"
	if on, _ := AutostartStatus(); on {
		auto = "开"
	}
	cfg, err := LoadHostConfig(root)
	if err != nil {
		log.Printf("读配置失败: %v", err)
		return
	}
	NotifyAll(root, cfg, "日报服务状态",
		fmt.Sprintf("下次触发: %s\n开机自启: %s", next.Format("01-02 15:04"), auto))
}

func refreshAutoStartMenu(item *systray.MenuItem) {
	on, _ := AutostartStatus()
	label := "开机自启: 关"
	if on {
		label = "开机自启: 开"
	}
	item.SetTitle(label)
}

// ---- 状态文件读写(带锁: 手动触发和定时触发可能并发) ----

var stateMu sync.Mutex

func loadState(root string) (daemonState, error) {
	stateMu.Lock()
	defer stateMu.Unlock()
	var st daemonState
	data, err := os.ReadFile(filepath.Join(root, stateFile))
	if err != nil {
		return st, err // 文件不存在 = 首次运行, 零值即可
	}
	json.Unmarshal(data, &st)
	return st, nil
}

func saveState(root string, st daemonState) error {
	stateMu.Lock()
	defer stateMu.Unlock()
	path := filepath.Join(root, stateFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// setupDaemonLog 守护进程没有控制台, 日志必须落盘, 否则出了问题只能猜。
// 同时保留 stderr(开发期从终端启动时照样能看到)。
// io.MultiWriter = "一个数据, 多个出口", 标准库现成的。
func setupDaemonLog(root string) {
	logPath := filepath.Join(root, "logs", "daemon.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err == nil {
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			log.SetOutput(io.MultiWriter(f, os.Stderr))
			return
		}
	}
	log.SetOutput(os.Stderr)
}
