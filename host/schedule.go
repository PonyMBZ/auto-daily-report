package main

// schedule.go —— 定时计算: "下班前 30 分钟"是几点?
//
// 教学点: 定时器本身(time.Timer)很简单, 难的是"下一次触发时间怎么算"。
// 把"算时间"和"等时间"分开: nextFireTime 是纯函数(给定 now 算结果, 好测试),
// schedulerLoop 负责等待和触发(不好测试, 但里面没有逻辑)。

import (
	"fmt"
	"time"
)

// Schedule 定时配置(对应 config.yaml 的 schedule 段)。
type Schedule struct {
	WorkEnd     string // "18:30" 下班时间
	LeadMinutes int    // 提前多少分钟触发 pipeline
}

// LoadSchedule 从宿主配置取定时段, 缺省给默认值(配置缺字段不报错原则)。
func LoadSchedule(cfg map[string]any) Schedule {
	s := asMap(cfg["schedule"])
	lead := s["lead_minutes"]
	leadInt, ok := lead.(int)
	if !ok {
		leadInt = 30 // YAML 解析整数是 int, 但防御性写法: 类型不符就用默认
	}
	workEnd := asString(s["work_end"])
	if workEnd == "" {
		workEnd = "18:30"
	}
	return Schedule{WorkEnd: workEnd, LeadMinutes: leadInt}
}

// FireTimeToday 今天触发时刻 = 下班时间 - 提前量。
func (s Schedule) FireTimeToday(now time.Time) (time.Time, error) {
	var h, m int
	if _, err := fmt.Sscanf(s.WorkEnd, "%d:%d", &h, &m); err != nil {
		return time.Time{}, fmt.Errorf("schedule.work_end 应为 HH:MM, 收到: %q", s.WorkEnd)
	}
	end := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	return end.Add(-time.Duration(s.LeadMinutes) * time.Minute), nil
}

// NextFire 距离 now 最近的一次未来触发。
// 疑问: 为什么 daemon 启动时若今天的触发点已过, 不立即补跑?
// 答: 触发点已过通常意味着"人刚开机/刚安装", 此时钉钉库里的今天数据还不全,
//
//	立即跑会产出一份残缺日报。宁可等明天, 也不产出错误的东西;
//	真要补今天的, 托盘菜单有"立即生成"。
func (s Schedule) NextFire(now time.Time) (time.Time, error) {
	fire, err := s.FireTimeToday(now)
	if err != nil {
		return time.Time{}, err
	}
	if !fire.After(now) {
		fire = fire.Add(24 * time.Hour) // 今天的已过, 顺到明天同一时刻
	}
	return fire, nil
}
