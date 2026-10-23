package main

// config.go —— Go 宿主读 config.yaml 的唯一入口(对应 Python 侧 config.py 的纪律:
// 不许 scattered 地到处读配置文件)。
//
// 疑问: Go 侧为什么不用 config.py 那套三层优先级(DEFAULTS < yaml < stdin)?
// 答: 那是 skill 的门面契约。宿主自己读文件, 读的是"宿主关心的段"
//    (notify / schedule...); skill 业务的配置(collect/report/dingtalk/llm)
//    宿主一律不经手, 全部经 stdin 传给 skill —— 各管一段, 不越界。

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// LoadHostConfig 读 config.yaml 为 map。宿主不定义死结构体,
// 和 SkillResult 同理: 配置项会随版本演化, map 不怕多字段少字段。
func LoadHostConfig(root string) (map[string]any, error) {
	data, err := os.ReadFile(filepath.Join(root, "config.yaml"))
	if err != nil {
		return nil, fmt.Errorf("读 config.yaml 失败: %w", err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config.yaml 不是合法 YAML: %w", err)
	}
	return cfg, nil
}

// ---- 取值助手: 配置缺字段时给默认值而不是报错 ----
// 疑问: 为什么缺字段不报错?
// 答: 用户从旧版本升级、还没加新配置段时, 服务不能因此起不来。
//    "缺了就用默认值"是配置的容错原则, 和 Python 侧 DEFAULTS 同理。
// 疑问: 为什么需要这一堆 asXXX, 不能直接用 cfg["notify"].(map[string]any)?
// 答: 能, 但每个调用点都写类型断言会很啰嗦且容易 panic(断言失败)。
//    收口成助手函数: 断言只写一次, 失败返回零值。

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func asStringSlice(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
