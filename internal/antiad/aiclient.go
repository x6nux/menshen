package antiad

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"menshen/internal/billing"
	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/upstream"
)

const (
	// aiMaxAttempts 是一次判定允许的总尝试次数（含首次）。
	// 网络抖动、上游 5xx、限流都会消耗一次尝试；每次失败都意味着
	// 这条消息被放行，因此重试的代价低于漏判。
	aiMaxAttempts = 5
	// aiTotalBudget 是全部尝试的总时间预算。
	//
	// 没有它，5 次 × 单次 20 秒超时 = 最坏 100 秒，而每个 bot 的判定
	// worker 只有 32 路：上游整体变慢时，积压会迅速填满判定队列，
	// 后面的消息全部按队列已满放行。预算到了就不再重试。
	//
	// 按 aiAttemptCap（45 秒）切段，150 秒够三路模型各试一次；复判期间
	// 被临时禁言的人是 5 分钟窗口（tempMute），150 秒仍在窗口内。
	aiTotalBudget = 150 * time.Second
	// aiRetryBase 是退避基数，按 2 的幂增长，封顶 2 秒。
	aiRetryBase = 200 * time.Millisecond
)

// aiSessionID 是所有大模型请求共用的会话标识（OpenAI 的 user 字段与兼容
// 网关的 session_id 都填它）。
//
// 固定一个值是为了让上游把同一路判定的 KV 前缀留在同一个会话/副本上：判定
// 用的 system 提示词对所有消息都一样（几 KB 的稳定前缀），固定会话才谈得上
// 命中前缀缓存，也省掉跨副本的冷启动。
//
// 只加在 chat 端点上：systemone 是 TypeSafe 原生形态，多带字段会直接返回
// 400 Invalid request，导致初判整条失败。
const aiSessionID = "menshen-antiad"

// aiAttemptCap 是单次尝试的时间上限（测试可调）。
//
// 只有总预算不够：预算在两次尝试之间才检查，一路慢模型（大提示词下可拖到
// 客户端超时）就能把整个预算耗尽，列表里更快的模型连试都轮不上，复判整条
// 失败。按上限切成多段，慢的一路被切掉之后还能换到下一路。
var aiAttemptCap = 45 * time.Second

// retryDelay 返回第 n 次失败后的等待时长（n 从 0 开始）。
func retryDelay(n int) time.Duration {
	d := aiRetryBase << n
	if d > 2*time.Second {
		return 2 * time.Second
	}
	return d
}

// hedgeKey 是并发模式的统计粒度：同一个模型（列表）在两个端点上的表现
// 互不相干。传进来的是逗号连接后的列表。
func hedgeKey(ep upstream.Endpoint, models string) string {
	return ep.String() + ":" + models
}

// aiReply 是一次成功请求的结果：响应体、用量、成本与实际命中的模型。
type aiReply struct {
	Raw   json.RawMessage
	Usage billing.Usage
	Cost  int64
	// Model 是全名（含上游前缀）。换模型后校准阈值，第一件事就是知道
	// 眼前这条判定出自哪个模型，所以它必须跟着结果回来。
	Model string
}

// aiCall 按配置顺序尝试模型列表，向上游发请求。
//
// 模型名形如 <上游名>/<模型ID>：带前缀的只走它绑定的那一个上游；
// 无前缀的任选一个可用上游。发给上游的 model 字段剥掉前缀
// —— 上游认的是模型 ID，不认我们的命名前缀。
//
// 可恢复的失败（网络错误、5xx、429、卡住）会重试：第 n 次尝试用
// models[n % len] —— 列表本身就是重试顺序，上游轮换由模型列表承担
// （同一个上游想多试几次，把它写进列表多次即可）。4xx（除 429）立即失败。
// 重试过多时进入并发模式：同时发相邻的多个模型。
//
// notify 非空时，重试全部耗尽会累计连续失败，达到阈值且过了冷却就调用它
// （上游异常告警，见 alertUpstreamTrouble）。
func aiCall(sh *core.Shared, ep upstream.Endpoint, models []string, payload map[string]any,
	notify func(string)) (aiReply, error) {

	snap := sh.Cache.Snap()
	models = cleanModels(models)
	if len(models) == 0 {
		return aiReply{}, fmt.Errorf("反广告：没有配置 %s 可用的模型", ep)
	}

	key := hedgeKey(ep, strings.Join(models, ","))
	deadline := time.Now().Add(aiTotalBudget)
	// 列表里的每个模型至少试一次；列表比 5 长时按列表长度放宽。
	attempts := aiMaxAttempts
	if len(models) > attempts {
		attempts = len(models)
	}

	var lastErr error
	lastStage := ""
	var lastElapsed time.Duration
	lastModel := models[0]
	actual := 0
	for attempt := 0; attempt < attempts; attempt++ {
		model := models[attempt%len(models)]
		lastModel = model
		fan := 1
		if hedging(sh, key) {
			fan = int(snap.SettingInt("antiad_hedge_fanout", 2))
		}
		// 单次尝试也要有上界：剩余预算与单次上限取小，切出来的这一段
		// 用完就换下一路，而不是一路把预算拖到见底。
		cap := time.Until(deadline)
		if cap > aiAttemptCap {
			cap = aiAttemptCap
		}
		attemptStart := time.Now()
		actual = attempt + 1
		r := aiRound(sh, snap, ep, models, attempt, fan, payload, cap)
		if r.err == nil {
			sh.AIFailStreak.Store(0) // 成功一次就清零
			if attempt > 0 {
				slog.Info("反广告：重试后成功", "模型", r.reply.Model,
					"第几次", attempt+1, "耗时", time.Since(attemptStart).Round(time.Millisecond))
			}
			return r.reply, nil
		}
		lastErr, lastStage, lastElapsed = r.err, r.stage, r.elapsed
		if !r.retryable {
			// 4xx 是配置问题（模型名错、鉴权错、余额不足），同一个模型
			// 再试多少次都一样。但列表里还有没试过的模型时，换一个也许
			// 就认（模型名在别的上游存在、鉴权不同）—— 每个模型只试一次。
			if attempt+1 >= len(models) || attempt+1 >= attempts {
				alertUpstreamTrouble(sh, snap, notify, ep, model, r.err)
				return aiReply{}, r.err
			}
			continue
		}
		noteRetry(sh, snap, key)
		if attempt == attempts-1 {
			break
		}
		// 卡住说明这一路走不通，换一个立即重来；退避只留给 5xx、429 与网络
		// 错误——那些是上游过载的信号，退避后才可能恢复。
		var wait time.Duration
		if !r.noBackoff {
			wait = retryDelay(attempt)
		}
		if time.Now().Add(wait).After(deadline) {
			lastErr = fmt.Errorf("%v（已用尽 %s 重试预算）", lastErr, aiTotalBudget)
			break
		}
		// 这条日志不能省：没有它，运维只看到最终的判定失败，
		// 不知道实际已经重试了多次。
		slog.Warn("反广告：上游调用失败，重试",
			"模型", model, "第几次", attempt+1, "并发", fan,
			"阶段", r.stage, "耗时", r.elapsed.Round(time.Millisecond), "err", r.err)
		time.Sleep(wait)
	}
	alertUpstreamTrouble(sh, snap, notify, ep, lastModel, lastErr)
	slog.Warn("反广告：上游调用失败，放弃",
		"模型", lastModel, "已试", actual, "计划", attempts, "阶段", lastStage,
		"耗时", lastElapsed.Round(time.Millisecond), "err", lastErr)
	return aiReply{}, fmt.Errorf("反广告：上游调用失败（已试 %d 次，最后卡在%s）: %v",
		actual, lastStage, lastErr)
}

// cleanModels 去掉空白项与重复项，顺序保持不变。
func cleanModels(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, m := range in {
		if m = strings.TrimSpace(m); m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// upstreamFor 返回这个模型该走的上游候选：带前缀的只走它绑定的那一个
// （不存在、停用或不支持该端点时返回空）；无前缀的走 Pick 的 sticky 结果。
func upstreamFor(snap *store.Snapshot, ep upstream.Endpoint, model string) []*upstream.Upstream {
	name, _ := upstream.SplitModelName(model)
	if name == "" {
		return upstream.Pick(snap.Upstreams, ep, "antiad")
	}
	for _, u := range snap.Upstreams {
		if u.Name == name && u.Status == 1 && u.Supports(ep) {
			return []*upstream.Upstream{u}
		}
	}
	return nil
}

// upstreamNotifier 返回把上游异常告警发给 bot 归属人与全部主管理员的函数。
// 上游是主管理员配置的，只有他能修；归属人则要知道自己的群正在漏判。
// 没有可发的对象时返回 nil，调用方据此跳过告警。
func upstreamNotifier(b *core.Bot) func(string) {
	if b == nil {
		return nil
	}
	targets := make([]int64, 0, len(b.Cfg.AdminIDs)+1)
	if o := b.Owner(); o != 0 {
		targets = append(targets, o)
	}
	for _, id := range b.Cfg.AdminIDs {
		if !slices.Contains(targets, id) {
			targets = append(targets, id)
		}
	}
	if len(targets) == 0 {
		return nil
	}
	return func(text string) {
		for _, id := range targets {
			b.Send(id, text, nil)
		}
	}
}

// alertUpstreamTrouble 累计连续失败，达到阈值且过了冷却就告警一次。
//
// 计数与冷却都是进程级：上游是全局资源，同一个上游出问题时每个群
// 各告一次会让管理员收到重复私聊。阈值 0 = 关闭。
func alertUpstreamTrouble(sh *core.Shared, snap *store.Snapshot, notify func(string),
	ep upstream.Endpoint, model string, lastErr error) {

	if notify == nil {
		return
	}
	threshold := snap.SettingInt("antiad_upstream_alert_after", 5)
	if threshold <= 0 {
		return
	}
	if sh.AIFailStreak.Add(1) < threshold {
		return
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("未知错误")
	}
	cooldown := time.Duration(snap.SettingInt("antiad_upstream_alert_minutes", 30)) * time.Minute
	now := time.Now()
	for {
		last := sh.AIAlertAt.Load()
		if last != 0 && now.Sub(time.Unix(last, 0)) < cooldown {
			return
		}
		if sh.AIAlertAt.CompareAndSwap(last, now.Unix()) {
			break
		}
	}
	upLabel := "（未绑定）"
	if n, _ := upstream.SplitModelName(model); n != "" {
		upLabel = n
	}
	slog.Warn("反广告：上游连续失败，已告警", "次数", sh.AIFailStreak.Load(),
		"端点", ep, "模型", model)
	notify(fmt.Sprintf("⚠️ <b>上游可能有问题</b>\n\n"+
		"判定请求已连续失败 %d 次。最后一次：\n"+
		"端点: %s\n模型: <code>%s</code>\n上游: <code>%s</code>\n"+
		"错误: %s\n\n"+
		"判定失败期间的消息一律放行。请检查该上游渠道是否可用、密钥与余额。",
		sh.AIFailStreak.Load(), ep, html.EscapeString(model), html.EscapeString(upLabel),
		html.EscapeString(core.TruncateRunes(lastErr.Error(), 300))))
}

// aiResult 是一次请求（或一轮并发请求）的结果。
type aiResult struct {
	reply aiReply
	err   error
	// stage 说明这次失败卡在哪一步：连接 / 首字超时 / 流中断 / HTTP 状态 /
	// 解析。日志里带着它，运维能区分是上游连不上、卡在首字，还是流中途
	// 中断 —— 三种情况的处置不同。
	stage string
	// elapsed 是这一次尝试实际花掉的时间。
	elapsed   time.Duration
	retryable bool
	// noBackoff 表示这一路不该退避，立即换下一个：上游卡住（systemone
	// 超出整次时限、复判超出首字时限），或这一路配置上就走不通
	// （模型绑定的上游不存在/停用/不支持该端点）。
	noBackoff bool
}

// aiRound 发一轮请求。fan 为 1 时就是单发；大于 1 时同时发 fan 路：
// 带前缀的模型各自走各自绑定的上游，所以并发试的是相邻的多个模型；
// 无前缀的模型没有绑定上游，按原样轮换上游。先成功的生效，其余立即取消。
//
// 全部失败时：只要有一路可重试，这一轮就可重试（某个上游不认这个模型，
// 换一个也许就认）；只有每一路都不可退避，才按立即换处理。
func aiRound(sh *core.Shared, snap *store.Snapshot, ep upstream.Endpoint,
	models []string, attempt, fan int, payload map[string]any, cap time.Duration) aiResult {

	if cap <= 0 {
		cap = aiAttemptCap
	}
	if fan <= 1 {
		model := models[attempt%len(models)]
		ups := upstreamFor(snap, ep, model)
		if len(ups) == 0 {
			return aiResult{err: modelUnavailable(model), retryable: true, noBackoff: true}
		}
		ctx, cancel := context.WithTimeout(context.Background(), cap)
		defer cancel()
		return aiAttempt(ctx, sh, snap, ep, model, ups[attempt%len(ups)], payload)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cap)
	defer cancel() // 先成功的一路返回后，其余各路随之取消
	// 带缓冲：提前返回之后，其余各路写结果时不会阻塞，goroutine 不会泄漏。
	results := make(chan aiResult, fan)
	for i := range fan {
		model := models[(attempt+i)%len(models)]
		ups := upstreamFor(snap, ep, model)
		if len(ups) == 0 {
			results <- aiResult{err: modelUnavailable(model), retryable: true, noBackoff: true}
			continue
		}
		up := ups[(attempt+i)%len(ups)]
		go func() { results <- aiAttempt(ctx, sh, snap, ep, model, up, payload) }()
	}

	agg := aiResult{noBackoff: true}
	errs := make([]string, 0, fan)
	for range fan {
		r := <-results
		if r.err == nil {
			return r
		}
		errs = append(errs, r.err.Error())
		agg.retryable = agg.retryable || r.retryable
		agg.noBackoff = agg.noBackoff && r.noBackoff
	}
	agg.err = fmt.Errorf("并发 %d 路全部失败: %s", fan, strings.Join(errs, "; "))
	return agg
}

// modelUnavailable 是模型绑定的上游不存在、停用或不支持该端点的错误。
// 可重试且不退避：让 aiCall 立刻切到列表里的下一个模型。
func modelUnavailable(model string) error {
	return fmt.Errorf("模型 %s 绑定的上游不存在、未启用或不支持该端点", model)
}

// msSetting 读一个毫秒数设置。非法值（0、负数）回落到默认值：时限被配成 0
// 的话每个请求一发出就被当成卡住，整条判定链路静默失效。
func msSetting(snap *store.Snapshot, key string, def int64) time.Duration {
	ms := snap.SettingInt(key, def)
	if ms <= 0 {
		ms = def
	}
	return time.Duration(ms) * time.Millisecond
}

// aiAttempt 发一次请求。
//
// 两个端点各有各的卡住判据：systemone 平均不到 1 秒出结果，整次请求限时
// antiad_so_timeout_ms；复判是流式的，只限首字 antiad_llm_ttft_ms ——首字之后
// 输出慢不等于卡住，整次仍受 AIClient 的总超时约束。
//
// payload 里的 model 由这里按当前模型覆写（剥掉上游前缀），所以并发试多个
// 模型时每路各拷一份 map，互不影响。
func aiAttempt(parent context.Context, sh *core.Shared, snap *store.Snapshot,
	ep upstream.Endpoint, model string, up *upstream.Upstream,
	payload map[string]any) aiResult {

	p := make(map[string]any, len(payload)+2)
	for k, v := range payload {
		p[k] = v
	}
	// 稳定会话标识（见 aiSessionID）。systemone 不加：会返回 400。
	// 只有 OpenAI Completions 渠道会原样带上它；其余渠道的协议适配层
	// 只挑自己认识的字段，多余字段不会传给上游。
	if ep == upstream.EPChat {
		p["user"] = aiSessionID
		p["session_id"] = aiSessionID
	}
	body, err := json.Marshal(up.BuildBody(ep, model, p))
	if err != nil {
		return aiResult{err: err} // 序列化都失败，重试多少次都一样
	}

	limit := msSetting(snap, "antiad_llm_ttft_ms", 15000)
	if ep == upstream.EPSystemOne {
		limit = msSetting(snap, "antiad_so_timeout_ms", 2000)
	}
	start := time.Now()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var stuck atomic.Bool
	watchdog := time.AfterFunc(limit, func() { stuck.Store(true); cancel() })
	defer watchdog.Stop()

	// 阶段：连接 → 首字 → 流中。失败时带着它，日志里能分清是连不上、
	// 卡在首字，还是流中途中断。
	stage := "连接"
	fail := func(err error) aiResult {
		if stuck.Load() {
			return aiResult{err: fmt.Errorf("上游 %s 超过 %v 没有响应", upName(up), limit),
				stage: "首字超时", elapsed: time.Since(start),
				retryable: true, noBackoff: true}
		}
		// 连不上、连接中断——都是值得再试一次的瞬时故障。
		return aiResult{err: err, stage: stage, elapsed: time.Since(start), retryable: true}
	}

	reqURL, uerr := up.URL(ep, model)
	if uerr != nil {
		// 渠道配置问题（base_url 缺失、端点不支持）：停留在这个模型上
		// 没有意义，交给 aiCall 立即换列表里的下一个模型。
		return aiResult{err: uerr, stage: "配置"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		reqURL, bytes.NewReader(body))
	if err != nil {
		// 请求都构造不出来（URL 非法），重试多少次都一样。
		return aiResult{err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	// 鉴权头按渠道类型设置：Anthropic 用 x-api-key，Gemini 用
	// x-goog-api-key，其余用 Bearer。
	up.SetAuth(req)

	resp, err := sh.AIClient.Do(req)
	if err != nil {
		return fail(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		st := fmt.Sprintf("HTTP %d", resp.StatusCode)
		switch {
		case resp.StatusCode >= 500:
			return aiResult{err: fmt.Errorf("上游 %s 返回 %d", upName(up), resp.StatusCode),
				stage: st, elapsed: time.Since(start), retryable: true}
		case resp.StatusCode == http.StatusTooManyRequests:
			// 限流是典型的瞬时状态，退避后重来往往就过了。
			return aiResult{err: fmt.Errorf("上游 %s 限流 (429)", upName(up)),
				stage: st, elapsed: time.Since(start), retryable: true}
		}
		// Cloudflare 的错误正文也是信封，errors[0].message 比整段
		// JSON 可读得多；取不到就退回原文。
		detail := core.TruncateRunes(string(msg), 200)
		if up.EffectiveKind() == upstream.KindCloudflare {
			if m := upstream.CFErrorMessage(msg); m != "" {
				detail = core.TruncateRunes(m, 200)
			}
		}
		// 模型不接受 temperature：记下它（见 upstream/temperature.go），
		// 立即重发。判定要可复现所以固定 temperature=0，但有的模型直接
		// 拒绝该参数，不让步就是整条判定失败放行。下一次尝试会经 BuildBody
		// 自动去掉它。这不算网络故障，不退避。
		if upstream.TemperatureRejected(resp.StatusCode, detail) {
			_, id := upstream.SplitModelName(model)
			upstream.MarkTemperatureUnsupported(id)
			slog.Warn("反广告：模型不接受 temperature 参数，去掉后重试",
				"模型", model, "上游", upName(up), "状态", resp.StatusCode)
			return aiResult{
				err: fmt.Errorf("上游 %s 返回 %d：模型不接受 temperature 参数，已去掉该参数重试",
					upName(up), resp.StatusCode),
				stage: st, elapsed: time.Since(start),
				retryable: true, noBackoff: true}
		}
		return aiResult{err: fmt.Errorf("上游 %s 返回 %d: %s",
			upName(up), resp.StatusCode, detail),
			stage: st, elapsed: time.Since(start)}
	}

	var raw []byte
	stage = "流中"
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		// 上游忽略 stream、直接返回整包 JSON 时同样可用：此时看门狗限的是整次请求。
		raw, err = io.ReadAll(resp.Body)
		if err != nil {
			return fail(err)
		}
		// 非流式响应在这里翻译回统一形状：chat → OpenAI 形状（供复判与
		// 计费），systemone → SystemOne 答案形状（供主判定解析）。
		// 流式分支由 StreamAdapter 边读边转，出来已经是同一形状，不能再翻一次。
		if norm, nerr := up.NormalizeResponse(ep, raw); nerr != nil {
			return aiResult{err: fmt.Errorf("上游 %s 响应异常: %w", upName(up), nerr),
				stage: "解析", elapsed: time.Since(start)}
		} else {
			raw = norm
		}
	} else {
		// 首字看门狗在首字到达时停掉，之后只剩两种兜底：客户端的整体超时
		// （45 秒）与这里的流空闲超时。上游只输出少量内容后挂住时只有
		// 空闲超时能终止它 —— 否则一次判定长时间等待，重试预算也一并
		// 耗尽，日志里只剩一句 context deadline exceeded。
		idle := msSetting(snap, "antiad_llm_idle_ms", 10000)
		var idleTripped, firstSeen atomic.Bool
		// 空闲计时由有效内容驱动，不看字节：很多网关会持续发心跳注释
		// （": ping"），按字节计则永远不空闲，上游不输出任何内容也能拖到
		// 45 秒客户端超时。首字之前归首字看门狗管，所以这里只在首字之后生效。
		idleTimer := time.AfterFunc(idle, func() {
			if !firstSeen.Load() {
				return
			}
			idleTripped.Store(true)
			cancel()
		})
		defer idleTimer.Stop()
		// 各家的流式帧在这里统一转成 OpenAI 的 delta 形状；OpenAI
		// Completions 渠道原样透传。提前放弃读取时 Close 读端，
		// 转换 goroutine 不会卡在写上。
		stream := up.StreamAdapter(resp.Body)
		defer stream.Close()
		raw, err = readChatStream(stream,
			func() { firstSeen.Store(true); watchdog.Stop(); idleTimer.Reset(idle) },
			func() { idleTimer.Reset(idle) })
		if idleTripped.Load() {
			return aiResult{
				err:   fmt.Errorf("上游 %s 流中断：%v 没有新数据", upName(up), idle),
				stage: "流中断", elapsed: time.Since(start),
				retryable: true, noBackoff: true}
		}
		if err != nil {
			return fail(err)
		}
	}

	// 各渠道的响应翻译（流式在适配器里完成、非流式在上一段完成）。
	usage := billing.ExtractUsage(ep, raw)
	cost := int64(0)
	// 按全名查价：模型表的主键就是 <上游名>/<模型ID>。
	if m := snap.Models[model]; m != nil {
		cost = billing.ComputeCost(usage, m)
	}
	return aiResult{reply: aiReply{Raw: raw, Usage: usage, Cost: cost, Model: model}}
}

// upName 是日志与错误里的上游称呼：名字优先，没有名字才退回 ID。
func upName(u *upstream.Upstream) string {
	if u.Name != "" {
		return u.Name
	}
	return strconv.FormatInt(u.ID, 10)
}

// readChatStream 读 chat/completions 的 SSE 流，拼回非流式的响应形状
// （choices[0].message.content + usage），调用方与 ExtractUsage 都照常解析。
//
// onFirst 在收到首字时调用一次。首字指第一段非空的正文或思考内容：只带
// role 的空块不算（有的网关会立刻返回它，算进去首字检测就形同虚设）；思考
// 内容要算（推理模型先输出思考内容，不算的话每次都会被当成卡住）。
func readChatStream(r io.Reader, onFirst, onData func()) (json.RawMessage, error) {
	var content, thinking strings.Builder
	var usage json.RawMessage
	first := false
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue // 空行、心跳注释（": ping"）、event: 行
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"`
				} `json:"delta"`
			} `json:"choices"`
			Usage json.RawMessage `json:"usage"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue // 个别网关会夹带非 JSON 的行，跳过即可
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return nil, fmt.Errorf("上游在流中报错: %s",
				core.TruncateRunes(string(chunk.Error), 200))
		}
		for _, c := range chunk.Choices {
			d := c.Delta
			if d.Content != "" || d.ReasoningContent != "" || d.Reasoning != "" {
				if !first {
					first = true
					if onFirst != nil {
						onFirst()
					}
				}
				if onData != nil {
					onData()
				}
			}
			content.WriteString(d.Content)
			// 思考内容单独攒着：识图那条路上，推理模型偶发把全部输出放进
			// 思考、正文为空；调用方可以把思考当描述兜底。
			thinking.WriteString(d.ReasoningContent)
			thinking.WriteString(d.Reasoning)
		}
		// 用量在流末那一块（stream_options.include_usage），它的 choices 为空。
		if len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
			usage = chunk.Usage
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"choices": []map[string]any{{"message": map[string]string{
			"content": content.String(), "reasoning_content": thinking.String()}}},
		"usage": usage,
	})
}

// hedging 报告该端点:模型键当前是否处于并发模式；到期时退出并记一行日志。
func hedging(sh *core.Shared, key string) bool {
	v, ok := sh.AIHedge.Load(key)
	if !ok {
		return false
	}
	if time.Now().Before(v.(time.Time)) {
		return true
	}
	if sh.AIHedge.CompareAndDelete(key, v) {
		slog.Info("反广告：并发模式到期，恢复单发", "模型", key)
	}
	return false
}

// noteRetry 记一次重试。1 分钟内的重试超过 antiad_hedge_retries 次，说明
// 这个模型眼下很不稳定：进入并发模式，持续 antiad_hedge_minutes 分钟，
// 期间再次超过会顺延。
//
// 计数借用 AdLimits 的 1 分钟滑动窗口：Allow 在窗口未满时记一笔并放行，
// 满了返回 false——返回 false 的那一次恰好就是超过阈值。阈值为 0 时
// Allow 一律放行，并发模式永不触发，所以 0 即关闭。
func noteRetry(sh *core.Shared, snap *store.Snapshot, key string) {
	if sh.AdLimits.Allow("ai:retry:"+key, snap.SettingInt("antiad_hedge_retries", 5)) {
		return
	}
	until := time.Now().Add(time.Duration(snap.SettingInt("antiad_hedge_minutes", 5)) * time.Minute)
	if prev, had := sh.AIHedge.Swap(key, until); !had || time.Now().After(prev.(time.Time)) {
		// 只在进入时记一次，顺延不刷日志。
		slog.Warn("反广告：模型重试过多，启用并发请求", "模型", key,
			"路数", snap.SettingInt("antiad_hedge_fanout", 2),
			"截止", until.Format("15:04:05"))
	}
}

// publicError 把判定失败的原因收敛成给用户看的笼统文案。
//
// aiCall 的错误里带着上游名称与最多 200 字节的供应商响应体（见 aiAttempt
// 的错误拼装），它只该进日志。群内 /check 回复与申诉网页都是用户可见面
// —— 群成员能自己触发 /check、被封的人本来就在看申诉页，把内部基础设施
// 信息回给他们既是信息泄露，也没有任何可操作性。
func publicError(err error) string {
	if err == nil {
		return ""
	}
	return "判定服务暂时不可用，请稍后重试"
}
