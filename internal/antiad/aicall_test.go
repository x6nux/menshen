package antiad

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/upstream"
)

// ---- 请求提速：单次时限、流式首字、并发模式 ----
//
// 假上游一律走真实的 HTTP 往返：超时、流式解析、并发取消这几段恰恰是最容易
// 静默出错的地方，替换掉 aiCall 就什么都没测到。时限在测试里调到百毫秒级，
// 免得拖慢全量测试。

// stall 模拟卡住的上游：直到客户端放弃（兜底 2 秒）才返回，而且返回失败 ——
// 没有按时放弃的实现拿不到成功结果。
//
// 先把请求体读完：Go 的 HTTP 服务端读完请求体之后，才在后台探测客户端是否
// 已经断开，不读的话客户端早就放弃了，这里还要空等到兜底时限。
func stall(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	select {
	case <-r.Context().Done():
	case <-time.After(2 * time.Second):
	}
	w.WriteHeader(http.StatusServiceUnavailable)
}

func sseStart(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
}

func sseChunk(w http.ResponseWriter, data string) {
	fmt.Fprintf(w, "data: %s\n\n", data)
	w.(http.Flusher).Flush()
}

func setGlobal(t *testing.T, b *core.Bot, k, v string) {
	t.Helper()
	if err := b.PutSetting(k, v); err != nil {
		t.Fatalf("putSetting %s: %v", k, err)
	}
}

// trackPeak 记录服务端同时在处理的请求数的峰值，返回的函数在请求结束时调用。
func trackPeak(inflight, peak *atomic.Int32) func() {
	cur := inflight.Add(1)
	for {
		p := peak.Load()
		if cur <= p || peak.CompareAndSwap(p, cur) {
			break
		}
	}
	return func() { inflight.Add(-1) }
}

// TestSystemOneStuckRetriesImmediately：systemone 平均不到 1 秒出结果，超过
// 时限还没回就是卡住了 —— 换一个立即重来，不做退避。退避只会让这条消息的
// 判定再晚几百毫秒，而卡住不是「上游过载需要喘口气」。
func TestSystemOneStuckRetriesImmediately(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	var hits atomic.Int32
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			stall(w, r)
			return
		}
		fakeAIOK(w, r)
	})
	setGlobal(t, b, "antiad_so_timeout_ms", "100")

	start := time.Now()
	v, err := judgeSystemOne(b, b.Cache.Snap(), adState{}, soInstructions)
	elapsed := time.Since(start)

	if err != nil || !v.IsAd {
		t.Fatalf("卡住一次后应重试成功: v=%+v err=%v", v, err)
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("请求了 %d 次，期望 2 次（卡住的一次 + 立即重试）", n)
	}
	// 时限 100ms + 立即重试；若走退避，还要再等 aiRetryBase（200ms）。
	if elapsed >= 100*time.Millisecond+aiRetryBase {
		t.Errorf("耗时 %v：超时后应立即重试，不该退避", elapsed)
	}
}

// TestZeroTimeoutFallsBackToDefault：时限被配成 0（绕过面板直接改库、或将来
// 改动了校验）时不能当真 —— 否则每个请求一发出就被当成卡住，整条判定链路
// 静默失效，只剩日志里一串「没有响应」。
func TestZeroTimeoutFallsBackToDefault(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	fakeAI(t, b, nil)
	setGlobal(t, b, "antiad_so_timeout_ms", "0")

	if _, err := judgeSystemOne(b, b.Cache.Snap(), adState{}, soInstructions); err != nil {
		t.Fatalf("时限为 0 时应回落到默认值、照常判定: %v", err)
	}
}

// TestLLMFirstTokenDefaultIs15s：复判是「等大模型」，不是同步判定 ——
// 推理模型的首字可以到十几秒。默认卡在 5 秒时，上游稍有抖动就会把正常
// 请求当成卡住、立即重试，在上游已经吃紧时反而放大它的负载。
func TestLLMFirstTokenDefaultIs15s(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	if got := b.Cache.Snap().SettingInt("antiad_llm_ttft_ms", 0); got != 15000 {
		t.Errorf("复判首字默认上限应为 15000ms，得到 %d", got)
	}
}

// TestAIClientTimeoutCoversAttemptCap：客户端总超时（单次 HTTP 请求的上界）
// 必须大于单次尝试的上限。复判是流式，出了首字之后还要把话说完；客户端
// 超时先到的话，一个还在尝试窗口内的正常复判会被当成失败重试，白花一次
// 上游开销，日志里也只剩一句 context deadline exceeded。
//
// 总预算那一层不用它兜：每次尝试自带 context（剩余预算与 aiAttemptCap
// 取小），单次请求最长就是 aiAttemptCap。
func TestAIClientTimeoutCoversAttemptCap(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	if b.AIClient.Timeout <= aiAttemptCap {
		t.Errorf("AIClient 总超时 %v 不大于单次尝试上限 %v", b.AIClient.Timeout, aiAttemptCap)
	}
	if aiTotalBudget < aiAttemptCap {
		t.Errorf("总预算 %v 小于单次上限 %v，第一路就会被预算切掉", aiTotalBudget, aiAttemptCap)
	}
}

// TestLLMStreamAssemblesContentAndUsage：复判走流式。内容要按块拼回完整的
// JSON；用量要从最后一块里取到，否则面板上的开销永远是 0。请求里必须
// 开 stream、并要求上游在流末附上用量。
func TestLLMStreamAssemblesContentAndUsage(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	var reqBody atomic.Value
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		reqBody.Store(string(raw))
		sseStart(w)
		sseChunk(w, `{"choices":[{"delta":{"role":"assistant"}}]}`)
		sseChunk(w, `{"choices":[{"delta":{"content":"{\"is_ad\":true,"}}]}`)
		sseChunk(w, `{"choices":[{"delta":{"content":"\"confidence\":0.88,\"kind\":\"scam\",\"reason\":\"流式\"}"}}]}`)
		sseChunk(w, `{"choices":[],"usage":{"prompt_tokens":120,"completion_tokens":30}}`)
		sseChunk(w, `[DONE]`)
	})

	v, err := judgeLLM(b, b.Cache.Snap(), adState{}, adVerdict{}, llmSystemPrompt)
	if err != nil {
		t.Fatalf("judgeLLM: %v", err)
	}
	if !v.IsAd || v.Confidence != 0.88 || v.Kind != "scam" || v.Reason != "流式" {
		t.Errorf("拼接后的结论不对: %+v", v)
	}
	if v.Usage.PromptTokens != 120 || v.Usage.CompletionTokens != 30 {
		t.Errorf("用量没从最后一块取到: %+v", v.Usage)
	}
	req, _ := reqBody.Load().(string)
	if !strings.Contains(req, `"stream":true`) || !strings.Contains(req, `"include_usage":true`) {
		t.Errorf("请求没开流式，或没要求附上用量: %s", req)
	}
}

// TestLLMFirstTokenTimeoutRetries：首字超过时限就换一个立即重来。只回一个
// role 空块不算首字 —— 有的网关会立刻回它，算进去的话首字检测形同虚设。
func TestLLMFirstTokenTimeoutRetries(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	var hits atomic.Int32
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // 理由同 stall：让服务端能察觉客户端放弃
		sseStart(w)
		if hits.Add(1) == 1 {
			sseChunk(w, `{"choices":[{"delta":{"role":"assistant"}}]}`)
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
			return // 流就此断掉，一个字都没有
		}
		sseChunk(w, `{"choices":[{"delta":{"content":"{\"is_ad\":false,\"confidence\":0.9}"}}]}`)
		sseChunk(w, `[DONE]`)
	})
	setGlobal(t, b, "antiad_llm_ttft_ms", "100")

	start := time.Now()
	v, err := judgeLLM(b, b.Cache.Snap(), adState{}, adVerdict{}, llmSystemPrompt)
	elapsed := time.Since(start)

	if err != nil || v.IsAd {
		t.Fatalf("首字超时后应重试成功: v=%+v err=%v", v, err)
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("请求了 %d 次，期望 2 次", n)
	}
	if elapsed >= 100*time.Millisecond+aiRetryBase {
		t.Errorf("耗时 %v：首字超时后应立即重试，不该退避", elapsed)
	}
}

// TestLLMReasoningCountsAsFirstToken：推理模型先吐思考内容，正文要等一会儿。
// 思考内容必须算首字，否则这类模型每次都会被当成卡住，永远判不出结果。
func TestLLMReasoningCountsAsFirstToken(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	var hits atomic.Int32
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		sseStart(w)
		sseChunk(w, `{"choices":[{"delta":{"reasoning_content":"先看资料……"}}]}`)
		time.Sleep(250 * time.Millisecond) // 正文晚于首字时限，但思考内容已经到了
		sseChunk(w, `{"choices":[{"delta":{"content":"{\"is_ad\":false,\"confidence\":0.9}"}}]}`)
		sseChunk(w, `[DONE]`)
	})
	setGlobal(t, b, "antiad_llm_ttft_ms", "100")

	v, err := judgeLLM(b, b.Cache.Snap(), adState{}, adVerdict{}, llmSystemPrompt)
	if err != nil || v.IsAd {
		t.Fatalf("推理模型应正常出结论: v=%+v err=%v", v, err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("思考内容应算首字，不该重试；实际请求 %d 次", n)
	}
}

// TestHedgeKicksInAfterRepeatedRetries：一分钟内重试超过阈值，说明这个模型
// 眼下很不稳定 —— 之后每一轮同时发多路，谁先回来用谁。
func TestHedgeKicksInAfterRepeatedRetries(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	var hits, inflight, peak atomic.Int32
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= 3 {
			stall(w, r) // 前三次卡住，逼出三次重试
			return
		}
		// 只统计卡住阶段之后的请求：被放弃的请求在服务端收尾有先后，
		// 算进来会把峰值抬高，与客户端是否并发无关。
		defer trackPeak(&inflight, &peak)()
		time.Sleep(50 * time.Millisecond) // 让并发的两路在服务端重叠
		fakeAIOK(w, r)
	})
	setGlobal(t, b, "antiad_so_timeout_ms", "100")
	setGlobal(t, b, "antiad_hedge_retries", "2")
	setGlobal(t, b, "antiad_hedge_fanout", "2")

	if _, err := judgeSystemOne(b, b.Cache.Snap(), adState{}, soInstructions); err != nil {
		t.Fatalf("judgeSystemOne: %v", err)
	}
	// 前三次是串行的单发；第三次重试超过阈值 2，第四轮起同时发 2 路。
	if p := peak.Load(); p != 2 {
		t.Errorf("重试超过阈值后应同时发 2 路，实际最大并发 %d", p)
	}
}

// TestHedgeFirstSuccessWins：并发模式下先成功的那一路生效，不等慢的那一路。
func TestHedgeFirstSuccessWins(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	var hits atomic.Int32
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			stall(w, r)
			return
		}
		fakeAIOK(w, r)
	})
	setGlobal(t, b, "antiad_hedge_fanout", "2")
	b.AIHedge.Store(hedgeKey(upstream.EPSystemOne, "so-model"), time.Now().Add(time.Minute))

	start := time.Now()
	v, err := judgeSystemOne(b, b.Cache.Snap(), adState{}, soInstructions)
	if err != nil || !v.IsAd {
		t.Fatalf("快的那一路应当生效: v=%+v err=%v", v, err)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Errorf("耗时 %v：先成功的一路回来就该结束，不该等慢的那一路", elapsed)
	}
}

// TestHedgeAll4xxFailsFast：各路都是 4xx（模型名错、鉴权错）时，再来一轮
// 同样会错 —— 立即失败，只花这一轮的钱。
func TestHedgeAll4xxFailsFast(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	var hits atomic.Int32
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	})
	setGlobal(t, b, "antiad_hedge_fanout", "2")
	b.AIHedge.Store(hedgeKey(upstream.EPSystemOne, "so-model"), time.Now().Add(time.Minute))

	if _, err := judgeSystemOne(b, b.Cache.Snap(), adState{}, soInstructions); err == nil {
		t.Fatal("各路都是 400 时应当失败")
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("应只发一轮 2 路，实际请求 %d 次", n)
	}
}

// TestHedgeExpires：并发模式到期后回到单发，否则开销会一直翻倍下去。
func TestHedgeExpires(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	var hits atomic.Int32
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fakeAIOK(w, r)
	})
	setGlobal(t, b, "antiad_hedge_fanout", "2")
	b.AIHedge.Store(hedgeKey(upstream.EPSystemOne, "so-model"), time.Now().Add(-time.Second))

	if _, err := judgeSystemOne(b, b.Cache.Snap(), adState{}, soInstructions); err != nil {
		t.Fatalf("judgeSystemOne: %v", err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("并发模式已到期，应当单发；实际请求 %d 次", n)
	}
}

// TestLLMStreamIdleTimeout：上游「吐一个字就挂住」时要在空闲超时内失败，
// 而不是把 45 秒的客户端超时耗光（那会把重试预算一起烧掉，日志里只剩
// 一句 context deadline exceeded）。空闲计时从首字之后才开始。
func TestLLMStreamIdleTimeout(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	setGlobal(t, b, "antiad_llm_idle_ms", "300")
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		sseStart(w)
		sseChunk(w, `{"choices":[{"delta":{"content":"{\"is_ad\":"}}]}`)
		// 之后什么都不发：等客户端断开。
		<-r.Context().Done()
	})

	start := time.Now()
	_, err := judgeLLM(b, b.Cache.Snap(), adState{}, adVerdict{}, llmSystemPrompt)
	if err == nil {
		t.Fatal("卡住的流应当失败")
	}
	if !strings.Contains(err.Error(), "流中断") {
		t.Errorf("错误里应说明是流中断：%v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("应在空闲超时内失败，实际用了 %v", took)
	}
}

// TestLLMStreamHeartbeatDoesNotHideStall：心跳注释（": ping"）不算进度 ——
// 很多网关会一直发它，按字节计空闲的话上游一个字都不吐也能拖到 45 秒
// 客户端超时（实测形态总结就是这么失败的：日志「最后卡在流中」）。
func TestLLMStreamHeartbeatDoesNotHideStall(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	setGlobal(t, b, "antiad_llm_idle_ms", "300")
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		sseStart(w)
		sseChunk(w, `{"choices":[{"delta":{"content":"{\"is_ad\":true"}}]}`)
		// 之后只发心跳，不再有内容。
		for i := 0; i < 100; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
				fmt.Fprint(w, ": ping\n\n")
				w.(http.Flusher).Flush()
			}
		}
	})

	start := time.Now()
	_, err := judgeLLM(b, b.Cache.Snap(), adState{}, adVerdict{}, llmSystemPrompt)
	if err == nil {
		t.Fatal("只有心跳的流应当失败")
	}
	if !strings.Contains(err.Error(), "流中断") {
		t.Errorf("错误里应说明是流中断：%v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("心跳不该算进度，应在空闲超时内失败，实际用了 %v", took)
	}
}

// TestSlowModelGetsCutForTheFastRetry：单次尝试必须有上界。实测的失败形态是
// 「一路慢模型把整个预算吃光」—— 列表里 mimo-v2.5 拖满客户超时，而
// big-pickle 3 秒就能出结果，却连试都轮不上，复判整条失败。
func TestSlowModelGetsCutForTheFastRetry(t *testing.T) {
	oldCap := aiAttemptCap
	aiAttemptCap = 300 * time.Millisecond
	defer func() { aiAttemptCap = oldCap }()

	b, _ := testutil.NewTestBot(t, 1)
	var n atomic.Int32
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if n.Add(1) == 1 {
			// 第一路：吐一个字就挂住（慢模型），等客户端把它切掉。
			sseStart(w)
			sseChunk(w, `{"choices":[{"delta":{"content":"{"}}]}`)
			<-r.Context().Done()
			return
		}
		// 第二路：立刻给出结论。
		sseStart(w)
		sseChunk(w, `{"choices":[{"delta":{"content":"{\"is_ad\":true,\"confidence\":0.9,\"kind\":\"scam\",\"reason\":\"快模型\"}"}}]}`)
		sseChunk(w, `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
		sseChunk(w, `[DONE]`)
	})
	// 重试顺序：慢的在前、快的在后（与线上 [mimo-v2.5, big-pickle] 同形）。
	setGlobal(t, b, "antiad_llm_models", `["fake/slow","fake/fast"]`)

	start := time.Now()
	v, err := judgeLLM(b, b.Cache.Snap(), adState{}, adVerdict{}, llmSystemPrompt)
	if err != nil {
		t.Fatalf("慢模型被切掉后应换到快模型并成功：%v", err)
	}
	if v.Reason != "快模型" || !v.IsAd {
		t.Errorf("应采信快模型的结论，得到 %+v", v)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("整个重试应在秒级完成，实际用了 %v", took)
	}
}

// TestLLMRequestCarriesStableSession：大模型请求要带固定的会话标识（user +
// session_id），让上游把同一路判定的前缀留在同一个会话/副本上；systemone
// 是 TypeSafe 原生形态，多带字段会 400，绝不能加。
func TestLLMRequestCarriesStableSession(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	var chatBody, soBody atomic.Value
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/systemone") {
			soBody.Store(string(raw))
			w.Write([]byte(`{"answers":{"is_ad":{"choice":"clean","confidence":0.9}}}`))
			return
		}
		chatBody.Store(string(raw))
		w.Write([]byte(`{"choices":[{"message":{"content":` +
			`"{\"is_ad\":false,\"confidence\":0.9,\"reason\":\"正常\"}"}}]}`))
	})

	if _, err := judgeLLM(b, b.Cache.Snap(), adState{}, adVerdict{}, llmSystemPrompt); err != nil {
		t.Fatalf("judgeLLM: %v", err)
	}
	body, _ := chatBody.Load().(string)
	if !strings.Contains(body, `"user":"`+aiSessionID+`"`) ||
		!strings.Contains(body, `"session_id":"`+aiSessionID+`"`) {
		t.Errorf("chat 请求应带固定会话标识：%s", body)
	}

	if _, err := judgeSystemOne(b, b.Cache.Snap(), adState{}, soInstructions); err != nil {
		t.Fatalf("judgeSystemOne: %v", err)
	}
	so, _ := soBody.Load().(string)
	if strings.Contains(so, "session_id") || strings.Contains(so, `"user"`) {
		t.Errorf("systemone 请求不该带这些字段（实测会 400）：%s", so)
	}
}
