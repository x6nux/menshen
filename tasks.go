package main

import (
	"time"

	"menshen/internal/antiad"
	"menshen/internal/core"
)

// taskIntervals 是两档定时任务的周期。生产用 defaultTaskIntervals，
// 测试注入毫秒级值以便在秒内观察到副作用。
type taskIntervals struct {
	minute time.Duration
	hourly time.Duration
}

var defaultTaskIntervals = taskIntervals{
	minute: time.Minute,
	hourly: time.Hour,
}

func runBackgroundTasks(stop <-chan struct{}, sh *core.Shared, reg *core.Registry) {
	runBackgroundTasksEvery(stop, sh, reg, defaultTaskIntervals)
}

func runBackgroundTasksEvery(stop <-chan struct{}, sh *core.Shared, reg *core.Registry,
	iv taskIntervals) {

	minute := time.NewTicker(iv.minute)
	hourly := time.NewTicker(iv.hourly)
	defer minute.Stop()
	defer hourly.Stop()

	// 启动即跑一轮：ticker 的首次触发要等满一个周期，冷启动后清理任务
	// 整整一小时不执行。
	tickMinute(sh, reg)
	tickHourly(sh)

	for {
		select {
		case <-stop:
			return
		case <-minute.C:
			tickMinute(sh, reg)
		case <-hourly.C:
			tickHourly(sh)
		}
	}
}

func tickMinute(sh *core.Shared, reg *core.Registry) {
	antiad.GCChatAdminCache(sh) // 群管理员缓存过期条目
	antiad.GCBioCache(sh)       // 个人简介缓存过期条目
	sh.AdLimits.GC()            // 反广告护栏窗口：回收长期无人问津的 key
	sh.Captcha.GC()             // 过期的人机验证题
	antiad.GCUnbanGate(sh)      // 自助解除的重试记录
	if reg != nil {
		// 待输入会话是每个 bot 各自一份的，逐个清。
		reg.Each(func(b *core.Bot) { b.GCPending() })
	}
}

func tickHourly(sh *core.Shared) {
	antiad.CleanupData(sh)
	antiad.RunAdDigest(sh, false) // 形态摘要，样本不够时内部直接返回
}
