package main

// autostart.go —— Windows 开机自启: 注册表 Run 键。
//
// 原理: HKCU\Software\Microsoft\Windows\CurrentVersion\Run 下的每个字符串值
// = "开机时自动运行的程序"。写值 = 开启自启, 删值 = 关闭。用户级(HKCU),
// 不需要管理员权限, 卸载/关闭只需删一个值, 干干净净。
//
// 疑问: 为什么不用"启动文件夹放快捷方式"?
// 答: 效果相同, 但注册表一条命令就能开关(6.5 配置界面直接调函数),
//    而快捷方式是文件操作(创建 .lnk 还得引入新依赖)。选最简单的依赖。

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

const autostartKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const autostartValueName = "DailyReport"

// exePath 当前可执行文件的绝对路径。注册表里必须写绝对路径,
// 因为开机时没有任何"当前目录"可言。
func exePath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return p, nil
}

// SetAutostart 开/关开机自启。on=true 双写(注册表+启动文件夹), false 双删。
// 为什么是双保险(2026-10-26 实测踩坑): 公司电脑的企业安全软件/组策略可能静默拦截
// 注册表 Run 键的自启项——值明明还在, 开机就是不被执行, 且没有任何报错。
// 启动文件夹走完全不同的 Windows 内部路径, 一个被拦另一个通常还能活。
func SetAutostart(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return fmt.Errorf("打开注册表失败: %w", err)
	}
	defer k.Close()

	if !on {
		// DeleteValue 对不存在的值会报错, 但那正是我们想要的状态 —— 视为成功
		if err := k.DeleteValue(autostartValueName); err != nil && err != registry.ErrNotExist {
			return fmt.Errorf("删除自启项失败: %w", err)
		}
		if vbs, err := startupVbsPath(); err == nil {
			os.Remove(vbs) // 启动文件夹的 VBS 也一起删; 不存在时 Remove 的报错忽略
		}
		fmt.Println("开机自启已关闭")
		return nil
	}

	exe, err := exePath()
	if err != nil {
		return err
	}
	// 保险二: 启动文件夹里的 VBS 启动器。windowStyle=0 让控制台窗口不闪现,
	// VBS 是系统自带解释器, 不需要装任何东西, 也不要求 .lnk 那种新依赖。
	vbs, err := startupVbsPath()
	if err != nil {
		return fmt.Errorf("启动文件夹不可用: %w", err)
	}
	// VBS 字符串引号地狱: """exe"" daemon" —— Go 里每个 " 要写成 \"
	// 注意 exe 后面是两个引号: 第一个和第二个组成 VBS 的转义引号, 字符串在 daemon" 处收尾
	content := "' Auto Daily Report 开机自启(由 daily-report.exe 维护, 请勿手改)\r\n" +
		"CreateObject(\"WScript.Shell\").Run \"\"\"" + exe + "\"\" daemon\", 0, False\r\n"
	// 关键: 必须存成 UTF-16 LE 带 BOM。exe 路径含中文(如 github-蒸馏.skills),
	// 而 VBScript 解释器按系统 ANSI 代码页(GBK)读无 BOM 的文件, UTF-8 的中文路径
	// 会被读成乱码 → "系统找不到指定的文件"(2026-10-26 实测踩坑)。
	// 带 BOM 的 UTF-16 是 VBScript 唯一认的 Unicode 编码。
	utf16 := []byte{0xFF, 0xFE}
	for _, r := range content {
		if r < 0x10000 {
			utf16 = append(utf16, byte(r), byte(r>>8))
		}
	}
	if err := os.WriteFile(vbs, utf16, 0o644); err != nil {
		return fmt.Errorf("写启动文件夹失败: %w", err)
	}
	// 注册表通道不直接拉 exe(会弹控制台窗口, 用户随手关掉服务就死了),
	// 而是调 wscript 跑上面那个 VBS —— 两个通道统一走隐藏启动。
	// 手动从终端跑 daemon 仍然是控制台模式, 日志照常可见, 两不耽误。
	if err := k.SetStringValue(autostartValueName, `wscript.exe "`+vbs+`"`); err != nil {
		return fmt.Errorf("写入自启项失败: %w", err)
	}
	fmt.Printf("开机自启已开启: %s daemon (注册表 + 启动文件夹双保险)\n", exe)
	return nil
}

// startupVbsPath 启动文件夹(shell:startup)里的 VBS 启动器路径。
// 为什么用 APPDATA 拼而不用注册表 Shell Folders 键?
// 答: 两者等价, 但环境变量零成本; 公司策略锁注册表时也不受影响。
func startupVbsPath() (string, error) {
	appdata := os.Getenv("APPDATA")
	if appdata == "" {
		return "", fmt.Errorf("APPDATA 环境变量为空")
	}
	return filepath.Join(appdata, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "daily-report.vbs"), nil
}

// AutostartStatus 查询当前自启状态(配置界面/托盘菜单显示用)。
// 两个机制只要有一个在就算开(双保险, 见 SetAutostart 注释)。
func AutostartStatus() (bool, string) {
	on := false
	detail := ""
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartKey, registry.QUERY_VALUE)
	if err == nil {
		defer k.Close()
		if v, _, err := k.GetStringValue(autostartValueName); err == nil {
			on = true
			detail = v
		}
	}
	if vbs, err := startupVbsPath(); err == nil {
		if _, err := os.Stat(vbs); err == nil {
			on = true
			detail += " +启动文件夹"
		}
	}
	return on, detail
}
