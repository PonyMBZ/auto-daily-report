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

// SetAutostart 开/关开机自启。on=true 写注册表, false 删值。
func SetAutostart(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return fmt.Errorf("打开注册表失败: %w", err)
	}
	defer k.Close()

	if !on {
		// DeleteValue 对不存在的值会报错, 但那正是我们想要的状态 —— 视为成功
		if err := k.DeleteValue(autostartValueName); err != nil {
			if err == registry.ErrNotExist {
				fmt.Println("开机自启本来就是关闭的")
				return nil
			}
			return fmt.Errorf("删除自启项失败: %w", err)
		}
		fmt.Println("开机自启已关闭")
		return nil
	}

	exe, err := exePath()
	if err != nil {
		return err
	}
	// 路径带空格必须加引号, 否则 Windows 会把空格后的内容当参数
	if err := k.SetStringValue(autostartValueName, `"`+exe+`" daemon`); err != nil {
		return fmt.Errorf("写入自启项失败: %w", err)
	}
	fmt.Printf("开机自启已开启: %s daemon\n", exe)
	return nil
}

// AutostartStatus 查询当前自启状态(配置界面/托盘菜单显示用)。
func AutostartStatus() (bool, string) {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartKey, registry.QUERY_VALUE)
	if err != nil {
		return false, ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue(autostartValueName)
	if err != nil {
		return false, ""
	}
	return true, v
}
