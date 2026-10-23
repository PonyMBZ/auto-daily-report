package main

// skill.go —— Go 宿主与 Python skill 之间唯一的桥。
// 对应课程里反复讲的"门面契约":
//
//	stdin  喂 JSON 配置 → run.py 干活 → stdout 吐 JSON 结果 → 退出码表成败
//
// 本文件是 Go 侧对契约的实现。Go 的其它任何部分不许自己起 python 进程,
// 必须全部经过 RunSkill —— 和 Python 侧"只许从 config.py 读配置"是同一种纪律。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SkillResult 就是 run.py stdout 里的那个 JSON。
// 用 map 而不是定义死结构体, 原因和 Python 侧一样:
// Go 不需要知道 skill 内部细节, 拿到什么转发什么(管道编排时原样传给下一个 skill)。
type SkillResult map[string]any

// projectRoot 找项目根(含 config.yaml 的目录)。
// 疑问: 为什么不用相对路径 "../.."?
// 答: 相对路径依赖"从哪运行", 双击托盘图标时 cwd 可能是任何地方。
//
//	必须从可执行文件自身的位置推导, 才经得起任何启动方式。
//
// 疑问: go run 时 os.Executable 不是 host/ 下啊?
// 答: 对, go run 会把 exe 编译到临时目录, 所以再加一道保险:
//
//	exe 旁边没找到 config.yaml 就退到 cwd(go run 时 cwd 就是 host/)。
func projectRoot() string {
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		if _, statErr := os.Stat(filepath.Join(dir, "config.yaml")); statErr == nil {
			return dir
		}
	}
	cwd, err := os.Getwd()
	if err == nil {
		if _, statErr := os.Stat(filepath.Join(cwd, "..", "config.yaml")); statErr == nil {
			abs, _ := filepath.Abs(filepath.Join(cwd, ".."))
			return abs
		}
		if _, statErr := os.Stat(filepath.Join(cwd, "config.yaml")); statErr == nil {
			return cwd
		}
	}
	return "."
}

// findPython 找 Python 解释器。优先项目自带的 .venv(版本和依赖可控),
// 找不到才退到系统 PATH 的 python。
// 教学点: "给同事的交付物"必须自带运行时或明确依赖, 不能假设对方环境。
func findPython(root string) string {
	venv := filepath.Join(root, ".venv", "Scripts", "python.exe")
	if _, err := os.Stat(venv); err == nil {
		return venv
	}
	return "python" // PATH 里找, 找不到就报错(见 RunSkill 的错误处理)
}

// RunSkill 调一个 skill 的门面, 返回结果 + 退出码。
// input 会序列化成 JSON 喂给 run.py 的 stdin。
// 疑问: 为什么返回 exitCode 而不是只看 error?
// 答: 和 Python 侧约定一致: 退出码 2 = 密钥错、3 = 文件不存在,
//
//	调用方(如通知模块)要根据不同退出码给用户不同提示。
func RunSkill(root, skillName string, input map[string]any) (SkillResult, int, error) {
	runPy := filepath.Join(root, "skills", skillName, "run.py")
	if _, err := os.Stat(runPy); err != nil {
		return nil, -1, fmt.Errorf("skill 不存在: %s", runPy)
	}

	stdinJSON, err := json.Marshal(input)
	if err != nil {
		return nil, -1, fmt.Errorf("配置序列化失败: %w", err)
	}

	cmd := exec.Command(findPython(root), runPy)
	cmd.Stdin = bytes.NewReader(stdinJSON)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	exitCode := 0
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		exitCode = exitErr.ExitCode()
	} else if runErr != nil {
		// 连进程都没起来(比如 python 不在 PATH)
		return nil, -1, fmt.Errorf("启动失败: %w\n%s", runErr, stderr.String())
	}

	// 容忍 stdout 首尾空白; 空输出按 {} 处理(和 Python 侧 or "{}" 对称)
	var result SkillResult
	trimmed := bytes.TrimSpace(stdout.Bytes())
	if len(trimmed) == 0 {
		trimmed = []byte("{}")
	}
	if err := json.Unmarshal(trimmed, &result); err != nil {
		return nil, exitCode, fmt.Errorf("skill 输出不是 JSON: %v\n原始输出: %s",
			err, stdout.String())
	}
	// 非零退出且 skill 没自述原因时, 把 stderr 摘要塞进结果 —— python 崩溃时
	// traceback 只走 stderr, 不上报就只剩一个干巴巴的退出码, 排障全靠猜。
	if exitCode != 0 {
		if _, hasErr := result["error"]; !hasErr {
			if se := strings.TrimSpace(stderr.String()); se != "" {
				if len(se) > 1200 {
					se = se[:1200]
				}
				result["_stderr"] = se
			}
		}
	}
	return result, exitCode, nil
}
