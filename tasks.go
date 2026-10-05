package main

import (
	"log/slog"
	"sync/atomic"
	"time"

	"menshen/internal/antiad"
	"menshen/internal/core"
	"menshen/internal/panel"
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
	runHourly(sh)

	for {
		select {
		case <-stop:
			return
		case <-minute.C:
			tickMinute(sh, reg)
		case <-hourly.C:
			runHourly(sh)
		}
	}
}

// hourlyBusy 保证小时任务不重入。RunAdDigest 要调大模型（预算 45 秒）、
// CleanupData 要分批删库，正常远短于一小时，但一次卡住不该叠着跑第二轮。
var hourlyBusy atomic.Bool

// runHourly 把小时任务挪到独立 goroutine：形态总结要调大模型、保留期清理
// 要删库，同步跑会把分钟 tick 一起卡住（告警撤回、私聊汇总、全部内存 GC
// 都在那一轮里）。
func runHourly(sh *core.Shared) {
	if !hourlyBusy.CompareAndSwap(false, true) {
		slog.Warn("小时任务仍在运行，跳过这一轮")
		return
	}
	go func() {
		defer hourlyBusy.Store(false)
		tickHourly(sh)
	}()
}

func tickMinute(sh *core.Shared, reg *core.Registry) {
	antiad.GCCaches(sh)         // 反广告的全部内存缓存与节流记录（见 antiad/caches.go）
	panel.GCChatHealthCache(sh) // 面板的群权限自检结果
	sh.AdLimits.GC()            // 反广告护栏窗口：回收长期无人问津的 key
	sh.GCShard(time.Now())      // 同群多 bot 的发言人认领
	if reg == nil {
		return
	}

	now := time.Now()
	reg.Each(func(b *core.Bot) {
		b.GCPending() // 待输入会话是每个 bot 各自一份的，纯内存
		// 撤回与汇总要发 TG：放进各自的 goroutine，且同一个 bot 不重入。
		// 逐个串行时，一个慢 bot（TG 超时 40 秒）会把其他所有 bot 的
		// 清理一起拖住；下一轮 tick 到点时若这一轮还没跑完，跳过即可。
		if !b.MinuteBusy.CompareAndSwap(false, true) {
			return
		}
		go func() {
			defer b.MinuteBusy.Store(false)
			antiad.SweepAlertCleanup(b, now) // 到点撤回群内告警：只有发它的 bot 删得掉
			// 私聊汇总。启动即跑的这一轮顺带把游标就位：升级后第一次只记位置，
			// 拖到第一个整分钟的话，这一分钟里的命中会被当成「历史」跳过。
			antiad.FlushAdSummary(b, now)
			// 空标题补一次：先加配置、后入群的群，长期没人发言时消息路径
			// 等不到，只有这里能自愈。有标题的群不查 TG。
			refreshChatTitles(b, true)
		}()
	})
}

func tickHourly(sh *core.Shared) {
	antiad.CleanupData(sh)
	antiad.PrewarmSweep(sh)
	// 规则发现自动运行只负责「发车」：同步扫一遍游标后就起后台 goroutine。
	// 放在小时任务最前面，避免被下面可能长达几十分钟的禁言复查/形态总结堵住
	// ——它一旦在下一小时 tick 时还没跑完，整轮小时任务都会被跳过。
	antiad.AutoRuleDiscovery(sh)
	antiad.ReassertActiveMutes(sh) // 进群限制被外部解除时补一次（见 reassert.go）
	antiad.RunAdDigest(sh, false)  // 形态摘要，样本不够时内部直接返回
	// 全量刷一遍群标题：群改名后跟上面板，也让一直没查到的空标题再试。
	// 启动时 runHourly 会立刻跑一轮，等于启动回填。
	if sh.Reg != nil {
		sh.Reg.Each(func(b *core.Bot) { refreshChatTitles(b, false) })
	}
}

// refreshChatTitles 用这个 bot 的 token 把名下的群标题刷一遍。
//
// 标题只在添加群那一刻抓过：先加配置、后入群的群会一直空着，群改名也不会
// 跟着变。onlyEmpty 为真只查空标题（分钟级快速自愈）；否则全量（小时级，
// 跟进群改名）。按 bot 限量、逐条间隔，别把 TG 速率限制打满。
func refreshChatTitles(b *core.Bot, onlyEmpty bool) {
	q := `SELECT chat_id FROM bot_chats WHERE bot_id=?`
	if onlyEmpty {
		q += ` AND title=''`
	}
	rows, err := b.Store.Read.Query(q+` LIMIT 200`, b.BotID())
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		b.RefreshChatTitle(id)
		time.Sleep(50 * time.Millisecond)
	}
}
