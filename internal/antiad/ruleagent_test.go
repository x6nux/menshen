package antiad

// 规则发现 Agent 的测试。假上游回的是 OpenAI 兼容的 chat/completions
// JSON：用 tool_calls 驱动多轮工具循环，全部走真实的 Eino ReAct 图与
// 真实的 HTTP 往返（选路、解析、工具执行都是被测对象）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"menshen/internal/core"
	"menshen/internal/testutil"
)

// ruleAgentTestBot 建一个带 OpenAI 兼容假上游与已启用模型的测试 bot：
// fakeAI 负责写 upstreams 与 antiad_llm_model，这里再补一条 models 记录。
func ruleAgentTestBot(t *testing.T, h http.HandlerFunc) *core.Bot {
	t.Helper()
	b, _ := testutil.NewTestBot(t, 1)
	fakeAI(t, b, h)
	if _, err := b.Store.Write.Exec(`INSERT INTO models
		(name,prompt_price,completion_price,cache_read_price,cache_write_price,enabled)
		VALUES ('llm-model',0,0,0,0,1)`); err != nil {
		t.Fatal(err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	return b
}

// mustJSON 把 map 序列化成字符串，测试里拼假上游响应体用。
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// oaToolCallReply 造一条 OpenAI 形状的「模型请求调用工具」响应。
func oaToolCallReply(id, name string, args map[string]any) string {
	rawArgs, _ := json.Marshal(args)
	call := map[string]any{
		"index": 0, "id": id, "type": "function",
		"function": map[string]any{"name": name, "arguments": string(rawArgs)},
	}
	msg := map[string]any{"role": "assistant", "content": "",
		"tool_calls": []any{call}}
	return mustJSON(map[string]any{
		"choices": []any{map[string]any{"message": msg}}})
}

// oaTextReply 造一条普通的文本回复（无工具调用）。
func oaTextReply(text string) string {
	msg := map[string]any{"role": "assistant", "content": text}
	return mustJSON(map[string]any{
		"choices": []any{map[string]any{"message": msg}}})
}

// insertRuleAgentLog 往 antiad_log 写一条判定流水。
func insertRuleAgentLog(t *testing.T, b *core.Bot, text, verdict, action, kind string) int64 {
	t.Helper()
	res, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,created_at,bot_id)
		VALUES (-100,555,1,?,?,0.9,'so',?,?,?,?,?)`,
		text, verdict, kind, action, "", time.Now().Unix(), b.BotID())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// waitRuleAgentDone 轮询到本轮结束，返回结束时的状态。
func waitRuleAgentDone(t *testing.T, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		st := RuleAgentStatus(nil)
		if st["running"] == false {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("规则发现 %v 内没有结束，当前状态：%v", timeout, st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestRuleAgentHappyPath：假上游依次驱动 find → test_rule → create_rule，
// 规则真正落库为候选（enabled=0 / enforce=0，last_* 写本轮结果），
// 运行态归位且步骤日志能看到三次工具调用。
func TestRuleAgentHappyPath(t *testing.T) {
	var calls atomic.Int32
	b := ruleAgentTestBot(t, func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			w.Write([]byte(oaToolCallReply("c1", "find", map[string]any{
				"pattern": "办理贷款", "scope": "banned"})))
		case 2:
			w.Write([]byte(oaToolCallReply("c2", "test_rule", map[string]any{
				"pattern": "办理贷款"})))
		case 3:
			w.Write([]byte(oaToolCallReply("c3", "create_rule", map[string]any{
				"name": "贷款引流", "category": "promo", "pattern": "办理贷款",
				"note":         "命中已确认广告，未误伤正常消息",
				"evidence_ids": []int64{1}})))
		default:
			// create 成功会直接收尾，这一轮通常不会发生；留作兜底。
			w.Write([]byte(oaTextReply("已完成规则创建。")))
		}
	})
	insertRuleAgentLog(t, b, "办理贷款加微信 vx123", "ad", "deleted", "scam")
	insertRuleAgentLog(t, b, "今天天气不错", "clean", "none", "")

	if err := StartRuleDiscovery(b.Shared, 7); err != nil {
		t.Fatalf("启动规则发现失败: %v", err)
	}
	st := waitRuleAgentDone(t, 15*time.Second)

	if st["running"] != false {
		t.Errorf("结束后 running 应为 false：%v", st)
	}
	if st["error"] != "" {
		t.Errorf("happy path 不应有错误：%v", st["error"])
	}
	result, _ := st["result"].(string)
	if !strings.Contains(result, "候选规则") || !strings.Contains(result, "发现完成") {
		t.Errorf("Result 应说明已创建候选规则，得到 %q", result)
	}

	// 落库检查：enabled/enforce 归零，last_* 写的是本次测试结果。
	var (
		id                                     int64
		name, pattern, source, category, note  string
		enabled, enforce, tp, fp, undone, scan int64
		tested, by, created                    int64
	)
	if err := b.Store.Read.QueryRow(`SELECT id,name,pattern,category,note,source,
		enabled,enforce,last_tp,last_fp,last_undone,last_scanned,
		last_tested_at,created_by,created_at FROM ad_rules`).Scan(
		&id, &name, &pattern, &category, &note, &source, &enabled, &enforce,
		&tp, &fp, &undone, &scan, &tested, &by, &created); err != nil {
		t.Fatalf("候选规则没有落库: %v", err)
	}
	if name != "贷款引流" || pattern != "办理贷款" || source != "ai" {
		t.Errorf("规则内容不对：name=%q pattern=%q source=%q", name, pattern, source)
	}
	if enabled != 0 || enforce != 0 {
		t.Errorf("候选规则必须 enabled=0/enforce=0，得到 %d/%d", enabled, enforce)
	}
	if tp != 1 || fp != 0 || undone != 0 || scan != 2 || tested == 0 {
		t.Errorf("last_* 测试结果不对：tp=%d fp=%d undone=%d scan=%d tested=%d",
			tp, fp, undone, scan, tested)
	}
	if by != 7 {
		t.Errorf("created_by 应记录操作者 7，得到 %d", by)
	}
	// evidence_ids=[1] 指向那条已确认广告，服务端校验后写进 note。
	if !strings.Contains(note, "#1") {
		t.Errorf("note 应写入有效证据 id：%q", note)
	}
	if got, _ := st["created_rule_id"].(int64); got != id {
		t.Errorf("状态里的 created_rule_id=%v，落库 id=%d", st["created_rule_id"], id)
	}
	// 快照里也要能看到候选规则（面板列表走快照）。
	found := false
	for _, r := range b.Cache.Snap().AdRules {
		if r.ID == id {
			found = true
		}
	}
	if !found {
		t.Error("候选规则没有进入配置快照")
	}

	// 步骤日志：三条工具调用各一条，且摘要都不超过 300 字符。
	steps, _ := st["steps"].([]map[string]any)
	toolCalls := map[string]int{}
	for _, s := range steps {
		if sum, _ := s["summary"].(string); len([]rune(sum)) > ruleAgentSummaryMax {
			t.Errorf("步骤摘要超过 %d 字符：%q", ruleAgentSummaryMax, sum)
		}
		if s["kind"] == "tool" {
			toolCalls[s["name"].(string)]++
		}
	}
	for _, name := range []string{"find", "test_rule", "create_rule"} {
		if toolCalls[name] != 1 {
			t.Errorf("步骤日志里 %s 应出现 1 次，得到 %d（全部：%v）",
				name, toolCalls[name], toolCalls)
		}
	}
	if len(steps) == 0 {
		t.Error("步骤日志不应为空")
	}
}

// TestRuleAgentRejectsFPAndStopsAfterThree：create_rule 的 pattern 命中了
// 正常流水时拒绝创建，连续 3 次后本轮提前结束，不留下任何规则。
func TestRuleAgentRejectsFPAndStopsAfterThree(t *testing.T) {
	var calls atomic.Int32
	b := ruleAgentTestBot(t, func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		w.Header().Set("Content-Type", "application/json")
		if n <= 3 {
			w.Write([]byte(oaToolCallReply(
				"c"+itoa(int64(n)), "create_rule", map[string]any{
					"name": "误封规则", "category": "test", "pattern": "正常聊天",
					"note": "证据不足"})))
			return
		}
		w.Write([]byte(oaTextReply("放弃创建。")))
	})
	insertRuleAgentLog(t, b, "正常聊天记录一", "clean", "none", "")
	insertRuleAgentLog(t, b, "正常聊天记录二", "none", "none", "")

	if err := StartRuleDiscovery(b.Shared, 1); err != nil {
		t.Fatalf("启动规则发现失败: %v", err)
	}
	st := waitRuleAgentDone(t, 15*time.Second)

	if st["error"] == "" {
		t.Errorf("连续失败结束应写 error：%v", st)
	}
	result, _ := st["result"].(string)
	if !strings.Contains(result, "连续 3 次") {
		t.Errorf("Result 应说明连续 3 次创建被拒，得到 %q", result)
	}

	var n int
	if err := b.Store.Read.QueryRow(`SELECT COUNT(*) FROM ad_rules`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("创建被拒时不应写库，ad_rules 有 %d 条", n)
	}

	steps, _ := st["steps"].([]map[string]any)
	rejects := 0
	for _, s := range steps {
		if s["kind"] == "tool" && s["name"] == "create_rule" {
			rejects++
			if sum, _ := s["summary"].(string); !strings.Contains(sum, "创建被拒绝") {
				t.Errorf("步骤摘要应含拒绝原因，得到 %q", sum)
			}
		}
	}
	if rejects != 3 {
		t.Errorf("应有 3 次 create_rule 步骤，得到 %d", rejects)
	}
}

// TestRuleAgentSingleFlightAndStop：运行中重复 start 必须被挡下，
// stop 后本轮结束且 Running 归位。
func TestRuleAgentSingleFlightAndStop(t *testing.T) {
	release := make(chan struct{})
	b := ruleAgentTestBot(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Write([]byte(oaTextReply("完成")))
	})
	defer close(release)

	if err := StartRuleDiscovery(b.Shared, 1); err != nil {
		t.Fatalf("第一次启动失败: %v", err)
	}
	if err := StartRuleDiscovery(b.Shared, 1); err == nil ||
		!strings.Contains(err.Error(), "已在运行") {
		t.Fatalf("运行中重复启动应报「已在运行」，得到 %v", err)
	}
	if st := RuleAgentStatus(b.Shared); st["running"] != true {
		t.Fatalf("第一次启动后应处于运行中：%v", st)
	}

	if !StopRuleDiscovery(b.Shared) {
		t.Fatal("运行中 stop 应返回 true")
	}
	st := waitRuleAgentDone(t, 5*time.Second)
	if st["running"] != false {
		t.Errorf("stop 后 running 应归位：%v", st)
	}
	if st["error"] == "" {
		t.Errorf("手动停止应写 error：%v", st)
	}
	if StopRuleDiscovery(b.Shared) {
		t.Error("已经结束后 stop 应返回 false")
	}
}

// TestStartRuleDiscoveryRequiresOpenAICompat：没有可用的 OpenAI 兼容
// 模型时，start 直接返回明确的中文错误，且不进入运行态。
func TestStartRuleDiscoveryRequiresOpenAICompat(t *testing.T) {
	const wantPrefix = "规则发现需要 OpenAI 兼容渠道的上游"

	t.Run("没有配置复判模型", func(t *testing.T) {
		b, _ := testutil.NewTestBot(t, 1)
		err := StartRuleDiscovery(b.Shared, 1)
		if err == nil || !strings.Contains(err.Error(), wantPrefix) {
			t.Fatalf("应报明确错误，得到 %v", err)
		}
		if RuleAgentStatus(nil)["running"] == true {
			t.Error("启动失败不应进入运行态")
		}
	})

	t.Run("只有非兼容渠道", func(t *testing.T) {
		b, _ := testutil.NewTestBot(t, 1)
		if _, err := b.Store.Write.Exec(`INSERT INTO upstreams
			(name,base_url,api_key,weight,status,supports_chat,supports_systemone,kind)
			VALUES ('gem','http://x','k',1,1,1,0,'anthropic')`); err != nil {
			t.Fatal(err)
		}
		if _, err := b.Store.Write.Exec(`INSERT INTO models
			(name,prompt_price,completion_price,cache_read_price,cache_write_price,enabled)
			VALUES ('gem/claude-x',0,0,0,0,1)`); err != nil {
			t.Fatal(err)
		}
		if err := b.PutSetting("antiad_llm_model", "gem/claude-x"); err != nil {
			t.Fatal(err)
		}
		err := StartRuleDiscovery(b.Shared, 1)
		if err == nil || !strings.Contains(err.Error(), wantPrefix) ||
			!strings.Contains(err.Error(), "暂不支持") {
			t.Fatalf("非兼容渠道应报明确错误，得到 %v", err)
		}
	})

	t.Run("模型已停用", func(t *testing.T) {
		b, _ := testutil.NewTestBot(t, 1)
		if _, err := b.Store.Write.Exec(`INSERT INTO upstreams
			(name,base_url,api_key,weight,status,supports_chat,supports_systemone,kind)
			VALUES ('oa','http://x','k',1,1,1,0,'openai')`); err != nil {
			t.Fatal(err)
		}
		if _, err := b.Store.Write.Exec(`INSERT INTO models
			(name,prompt_price,completion_price,cache_read_price,cache_write_price,enabled)
			VALUES ('oa/m1',0,0,0,0,0)`); err != nil {
			t.Fatal(err)
		}
		if err := b.PutSetting("antiad_llm_model", "oa/m1"); err != nil {
			t.Fatal(err)
		}
		err := StartRuleDiscovery(b.Shared, 1)
		if err == nil || !strings.Contains(err.Error(), wantPrefix) {
			t.Fatalf("停用模型应报明确错误，得到 %v", err)
		}
	})
}

// insertRuleTestUpstream 登记一个测试上游（kind 显式给，避免默认值掩盖
// 渠道不兼容的分支）并刷新快照。
func insertRuleTestUpstream(t *testing.T, b *core.Bot, name string,
	status, supportsChat int64, kind string) {
	t.Helper()
	if _, err := b.Store.Write.Exec(`INSERT INTO upstreams
		(name,base_url,api_key,weight,status,supports_chat,supports_systemone,kind)
		VALUES (?,?,?,1,?,?,0,?)`, name, "http://x", "k", status, supportsChat, kind); err != nil {
		t.Fatal(err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
}

// insertRuleTestModel 登记一个测试模型并刷新快照。
func insertRuleTestModel(t *testing.T, b *core.Bot, name string, enabled int64) {
	t.Helper()
	if _, err := b.Store.Write.Exec(`INSERT INTO models (name,prompt_price,
		completion_price,cache_read_price,cache_write_price,enabled)
		VALUES (?,0,0,0,0,?)`, name, enabled); err != nil {
		t.Fatal(err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
}

// TestRuleAgentUsesConfiguredModel：显式配置 antiad_rule_model 时必须走它；
// 未配置时回退逻辑照常工作。两条路径都用假上游断言请求体里的 model 名。
func TestRuleAgentUsesConfiguredModel(t *testing.T) {
	// capture 返回一个只回文本的假上游 handler，把请求体里的 model 记进 got。
	capture := func(got *atomic.Value) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("解析上游请求失败: %v", err)
			}
			got.Store(req.Model)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(oaTextReply("本轮结束，未创建规则。")))
		}
	}

	t.Run("指定模型", func(t *testing.T) {
		var got atomic.Value
		b := ruleAgentTestBot(t, capture(&got))
		insertRuleTestModel(t, b, "fake/rule-model", 1)
		if err := b.PutSetting("antiad_rule_model", "fake/rule-model"); err != nil {
			t.Fatal(err)
		}
		if err := StartRuleDiscovery(b.Shared, 1); err != nil {
			t.Fatalf("启动失败: %v", err)
		}
		st := waitRuleAgentDone(t, 15*time.Second)
		if st["error"] != "" {
			t.Fatalf("本轮不应报错：%v", st)
		}
		if v, _ := got.Load().(string); v != "rule-model" {
			t.Errorf("请求体 model 应为 rule-model，得到 %q", v)
		}
	})

	t.Run("未指定回退复判列表", func(t *testing.T) {
		var got atomic.Value
		b := ruleAgentTestBot(t, capture(&got))
		if err := StartRuleDiscovery(b.Shared, 1); err != nil {
			t.Fatalf("启动失败: %v", err)
		}
		st := waitRuleAgentDone(t, 15*time.Second)
		if st["error"] != "" {
			t.Fatalf("本轮不应报错：%v", st)
		}
		// fakeAI 把 antiad_llm_model 配成 llm-model，回退应选中它。
		if v, _ := got.Load().(string); v != "llm-model" {
			t.Errorf("回退时请求体 model 应为 llm-model，得到 %q", v)
		}
	})
}

// TestStartRuleDiscoveryConfiguredModelInvalid：antiad_rule_model 非空但
// 任一条件不满足时，start 直接返回指向该设置项的中文错误，且不进入运行态。
func TestStartRuleDiscoveryConfiguredModelInvalid(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, b *core.Bot)
		want  string
	}{
		{
			name: "模型未登记",
			setup: func(t *testing.T, b *core.Bot) {
				if err := b.PutSetting("antiad_rule_model", "ghost/m1"); err != nil {
					t.Fatal(err)
				}
			},
			want: "不在「模型定价」里",
		},
		{
			name: "模型已停用",
			setup: func(t *testing.T, b *core.Bot) {
				insertRuleTestModel(t, b, "ghost/m1", 0)
				if err := b.PutSetting("antiad_rule_model", "ghost/m1"); err != nil {
					t.Fatal(err)
				}
			},
			want: "已被停用",
		},
		{
			name: "上游不存在",
			setup: func(t *testing.T, b *core.Bot) {
				insertRuleTestModel(t, b, "ghost/m1", 1)
				if err := b.PutSetting("antiad_rule_model", "ghost/m1"); err != nil {
					t.Fatal(err)
				}
			},
			want: "绑定的上游 ghost 不存在",
		},
		{
			name: "上游已停用",
			setup: func(t *testing.T, b *core.Bot) {
				insertRuleTestUpstream(t, b, "ghost", 0, 1, "openai")
				insertRuleTestModel(t, b, "ghost/m1", 1)
				if err := b.PutSetting("antiad_rule_model", "ghost/m1"); err != nil {
					t.Fatal(err)
				}
			},
			want: "绑定的上游 ghost 已停用",
		},
		{
			name: "上游未开启 chat",
			setup: func(t *testing.T, b *core.Bot) {
				insertRuleTestUpstream(t, b, "ghost", 1, 0, "openai")
				insertRuleTestModel(t, b, "ghost/m1", 1)
				if err := b.PutSetting("antiad_rule_model", "ghost/m1"); err != nil {
					t.Fatal(err)
				}
			},
			want: "未开启 chat 能力",
		},
		{
			name: "非 OpenAI 兼容渠道",
			setup: func(t *testing.T, b *core.Bot) {
				insertRuleTestUpstream(t, b, "ghost", 1, 1, "anthropic")
				insertRuleTestModel(t, b, "ghost/m1", 1)
				if err := b.PutSetting("antiad_rule_model", "ghost/m1"); err != nil {
					t.Fatal(err)
				}
			},
			want: "只支持 OpenAI 兼容渠道",
		},
		{
			name: "旧格式没有上游前缀",
			setup: func(t *testing.T, b *core.Bot) {
				insertRuleTestModel(t, b, "legacy-model", 1)
				if err := b.PutSetting("antiad_rule_model", "legacy-model"); err != nil {
					t.Fatal(err)
				}
			},
			want: "缺少「上游名/」前缀",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := testutil.NewTestBot(t, 1)
			tc.setup(t, b)

			err := StartRuleDiscovery(b.Shared, 1)
			if err == nil {
				// 兜底：错误逻辑若被改坏真的启动了，先收住这一轮再失败。
				StopRuleDiscovery(b.Shared)
				waitRuleAgentDone(t, 10*time.Second)
				t.Fatalf("配置无效时 start 应返回错误")
			}
			for _, want := range []string{"规则发现模型配置无效", tc.want,
				"全局设置 → 默认模型 → 规则发现模型"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("错误应包含 %q，得到 %q", want, err.Error())
				}
			}
			if RuleAgentStatus(nil)["running"] == true {
				t.Error("启动失败不应进入运行态")
			}
		})
	}
}

// TestRuleAgentTimeoutAndStepExhaustion：超时与步数耗尽也必须把
// Running 置回 false 并在 Result/Error 里说明原因（测试用可覆盖的
// 超时/步数变量缩短，不真等 180 秒）。
func TestRuleAgentTimeoutAndStepExhaustion(t *testing.T) {
	t.Run("超时", func(t *testing.T) {
		old := ruleAgentTimeout
		ruleAgentTimeout = 150 * time.Millisecond
		// release 保证假上游在测试结束前退出：httptest.Server.Close()
		// 会等在途请求跑完，卡死的 handler 会让清理挂住。
		release := make(chan struct{})
		defer func() {
			ruleAgentTimeout = old
			close(release)
		}()

		b := ruleAgentTestBot(t, func(w http.ResponseWriter, r *http.Request) {
			// 卡到客户端取消（或测试收尾）：模拟上游无响应。
			select {
			case <-r.Context().Done():
			case <-release:
			}
		})
		if err := StartRuleDiscovery(b.Shared, 1); err != nil {
			t.Fatalf("启动失败: %v", err)
		}
		st := waitRuleAgentDone(t, 5*time.Second)
		if result, _ := st["result"].(string); !strings.Contains(result, "超时") {
			t.Errorf("超时结束的 Result 应说明超时，得到 %q", result)
		}
		if st["error"] == "" {
			t.Error("超时结束应写 error")
		}
	})

	t.Run("步数耗尽", func(t *testing.T) {
		old := ruleAgentMaxStep
		ruleAgentMaxStep = 3
		defer func() { ruleAgentMaxStep = old }()

		b := ruleAgentTestBot(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(oaToolCallReply("c1", "list_banned",
				map[string]any{"limit": 5})))
		})
		if err := StartRuleDiscovery(b.Shared, 1); err != nil {
			t.Fatalf("启动失败: %v", err)
		}
		st := waitRuleAgentDone(t, 10*time.Second)
		if result, _ := st["result"].(string); !strings.Contains(result, "步数耗尽") {
			t.Errorf("步数耗尽的 Result 应说明原因，得到 %q（error=%v）",
				result, st["error"])
		}
		if st["running"] != false {
			t.Errorf("结束后 running 应为 false：%v", st)
		}
	})
}

// TestRuleAgentFindAlignsWithTestPattern：find 是宽松试跑工具，但它的
// 计数口径必须与正式测试（TestRulePattern）一致，否则模型会被两套数字
// 带偏。by_verdict 是命中行的原始 verdict 分布。
func TestRuleAgentFindAlignsWithTestPattern(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	insertRuleAgentLog(t, b, "贷款刷单加微信 aaa", "ad", "deleted", "scam")
	insertRuleAgentLog(t, b, "贷款刷单加微信 bbb", "ad", "undone", "scam")
	insertRuleAgentLog(t, b, "贷款刷单是什么意思", "clean", "none", "")
	insertRuleAgentLog(t, b, "贷款刷单讨论", "none", "none", "")
	insertRuleAgentLog(t, b, "贷款刷单？", "skipped", "none", "")
	insertRuleAgentLog(t, b, "贷款刷单失败", "error", "none", "")
	insertRuleAgentLog(t, b, "今天天气不错", "ad", "deleted", "scam")

	run := &ruleAgentRun{sh: b.Shared}
	raw := run.findMatches(context.Background(),
		findArgs{Pattern: "贷款刷单", Scope: "banned", Limit: 20})

	var got struct {
		Scanned   int64             `json:"scanned"`
		Matched   int64             `json:"matched"`
		TP        int64             `json:"tp"`
		FP        int64             `json:"fp"`
		Undone    int64             `json:"undone"`
		Neutral   int64             `json:"neutral"`
		AdsTotal  int64             `json:"ads_total"`
		Coverage  float64           `json:"coverage"`
		Kinds     []RuleKindStat    `json:"kinds"`
		ByVerdict map[string]int64  `json:"by_verdict"`
		Scope     string            `json:"scope"`
		Samples   []ruleAgentSample `json:"samples"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("find 返回不是 JSON：%v\n%s", err, raw)
	}
	ref, err := TestRulePattern(b.Shared, "贷款刷单")
	if err != nil {
		t.Fatal(err)
	}
	if got.Scanned != ref.Scanned || got.Matched != ref.Matched ||
		got.TP != ref.TP || got.FP != ref.FP || got.Undone != ref.Undone ||
		got.Neutral != ref.Neutral {
		t.Errorf("find 口径与 TestRulePattern 不一致：find=%+v ref=%+v", got, ref)
	}
	// action='undone' 优先归入 undone/FP：那条 verdict=ad 的撤销记录
	// 不算 TP。口径与 TestRulePattern 一致。
	if got.Scanned != 7 || got.Matched != 6 || got.TP != 1 ||
		got.FP != 3 || got.Undone != 1 || got.Neutral != 2 {
		t.Errorf("计数不对：%+v", got)
	}
	// 覆盖率口径与 TestRulePattern 一致：已确认广告 2 条、命中 1 条 → 0.5。
	if got.AdsTotal != ref.AdsTotal || got.Coverage != round4(ref.Coverage()) {
		t.Errorf("find 覆盖率与 TestRulePattern 不一致：find=%d/%v ref=%d/%v",
			got.AdsTotal, got.Coverage, ref.AdsTotal, ref.Coverage())
	}
	if got.AdsTotal != 2 || got.Coverage != 0.5 {
		t.Errorf("覆盖率不对：ads=%d cov=%v", got.AdsTotal, got.Coverage)
	}
	if len(got.Kinds) != len(ref.Kinds) {
		t.Errorf("find kinds 数量不一致：%v vs %+v", got.Kinds, ref.Kinds)
	}
	wantByVer := map[string]int64{
		"ad": 2, "clean": 1, "none": 1, "skipped": 1, "error": 1}
	for k, want := range wantByVer {
		if got.ByVerdict[k] != want {
			t.Errorf("by_verdict[%s]=%d，期望 %d（%v）",
				k, got.ByVerdict[k], want, got.ByVerdict)
		}
	}
	// scope=banned：样本优先给已确认广告（action=undone 的不算正例），
	// 附正常消息作参照。
	if len(got.Samples) == 0 || got.Samples[0].Verdict != "ad" ||
		got.Samples[0].Action == "undone" {
		t.Errorf("banned 样本应先给已确认广告：%+v", got.Samples)
	}
	if len(got.Samples) < 3 {
		t.Errorf("样本里应同时有广告与参照消息：%+v", got.Samples)
	}
	hasRef := false
	for _, s := range got.Samples {
		if s.Verdict != "ad" {
			hasRef = true
		}
	}
	if !hasRef {
		t.Errorf("banned 样本应附正常消息作参照：%+v", got.Samples)
	}

	// 非法 scope 与编译不过的正则都返回可读错误，而不是崩掉工具。
	if s := run.findMatches(context.Background(),
		findArgs{Pattern: "贷款", Scope: "wat"}); !strings.Contains(s, "scope") {
		t.Errorf("非法 scope 应返回错误文本，得到 %q", s)
	}
	if s := run.findMatches(context.Background(),
		findArgs{Pattern: "["}); !strings.Contains(s, "错误") {
		t.Errorf("坏正则应返回错误文本，得到 %q", s)
	}
}

// TestRuleAgentTestRuleOutputsCoverage：test_rule 返回 ads_total/coverage/kinds，
// 且低覆盖率不阻断 can_create 与创建（覆盖率只展示）。
func TestRuleAgentTestRuleOutputsCoverage(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	insertRuleAgentLog(t, b, "办理贷款加微信", "ad", "deleted", "scam")
	for i := 0; i < 50; i++ {
		insertRuleAgentLog(t, b, "正常聊天", "ad", "deleted", "promo")
	}
	run := &ruleAgentRun{sh: b.Shared, ctx: context.Background()}

	var out map[string]any
	if err := json.Unmarshal([]byte(run.testRule(context.Background(), "办理贷款")), &out); err != nil {
		t.Fatal(err)
	}
	if out["ads_total"].(float64) != 51 || out["can_create"] != true {
		t.Fatalf("低覆盖也应可创建：%v", out)
	}
	if got := out["coverage"].(float64); got < 0.019 || got > 0.020 {
		t.Errorf("覆盖率应为 1/51≈0.0196，得到 %v", got)
	}
	if len(out["kinds"].([]any)) != 2 {
		t.Errorf("kinds 应含 scam 与 promo：%v", out["kinds"])
	}
	// 创建入口也不看覆盖率：低覆盖 + fp=0 仍能创建。
	if got := run.createRule(context.Background(), createRuleArgs{
		Name: "低覆盖", Pattern: "办理贷款"}); !strings.Contains(got, "创建成功") {
		t.Errorf("低覆盖不应阻断创建：%q", got)
	}
}

// TestRuleAgentListKinds：类型计数只算已确认广告，recent_ids 最多 5 个、降序。
func TestRuleAgentListKinds(t *testing.T) {
	b := ruleAgentTestBot(t, func(w http.ResponseWriter, r *http.Request) {})
	var lastScam int64
	for i := 0; i < 6; i++ {
		lastScam = insertRuleAgentLog(t, b, "诈骗广告", "ad", "deleted", "scam")
	}
	insertRuleAgentLog(t, b, "正常", "clean", "none", "scam")
	insertRuleAgentLog(t, b, "被撤销", "ad", "undone", "scam")
	insertRuleAgentLog(t, b, "促销", "ad", "deleted", "promo")

	run := &ruleAgentRun{sh: b.Shared}
	var got map[string]any
	if err := json.Unmarshal([]byte(run.listKinds()), &got); err != nil {
		t.Fatal(err)
	}
	if got["ads_total"].(float64) != 7 {
		t.Errorf("ads_total 应为 7（6 scam + 1 promo），得到 %v", got["ads_total"])
	}
	kinds := got["kinds"].([]any)
	scam := kinds[0].(map[string]any)
	if scam["kind"] != "scam" || scam["total"].(float64) != 6 {
		t.Errorf("scam 统计不对：%v", scam)
	}
	if n := len(scam["recent_ids"].([]any)); n != 5 {
		t.Errorf("recent_ids 应截到 5 个，得到 %d", n)
	}
	// 降序：最新一条在最前。
	if got := scam["recent_ids"].([]any)[0].(float64); int64(got) != lastScam {
		t.Errorf("recent_ids 应最新在前（%d），得到 %v", lastScam, got)
	}
}

// TestRuleAgentReadRecords：批量、去重截断、无效 id 容错。
func TestRuleAgentReadRecords(t *testing.T) {
	b := ruleAgentTestBot(t, func(w http.ResponseWriter, r *http.Request) {})
	id1 := insertRuleAgentLog(t, b, "广告一", "ad", "deleted", "scam")
	id2 := insertRuleAgentLog(t, b, "广告二", "ad", "deleted", "scam")
	run := &ruleAgentRun{sh: b.Shared}

	var got map[string]any
	if err := json.Unmarshal([]byte(run.readRecords([]int64{id1, id1, id2, 9999, 0})), &got); err != nil {
		t.Fatal(err)
	}
	if got["ignored"].(float64) != 1 { // 重复的 id1
		t.Errorf("ignored 应只统计重复/截断，得到 %v", got["ignored"])
	}
	recs := got["records"].([]any)
	if len(recs) != 4 { // id1/id2 正常 + 9999/0 各一条 error
		t.Fatalf("records 应有 4 条，得到 %d", len(recs))
	}
	if run.readRecords(nil) != "错误：ids 必须是非空数组" {
		t.Errorf("空 ids 文案不对：%s", run.readRecords(nil))
	}

	// >10 个唯一 id：截断到 10，被截断的 2 个计入 ignored。
	ids := make([]int64, 0, 12)
	for i := 0; i < 12; i++ {
		ids = append(ids, insertRuleAgentLog(t, b, fmt.Sprintf("广告%d", i), "ad", "deleted", "scam"))
	}
	var trunc map[string]any
	if err := json.Unmarshal([]byte(run.readRecords(ids)), &trunc); err != nil {
		t.Fatal(err)
	}
	if trunc["ignored"].(float64) != 2 || len(trunc["records"].([]any)) != 10 {
		t.Errorf("超 10 条应截断并记 ignored=2：%v", trunc)
	}
}

// TestRuleAgentListRules：返回现有规则、未测试的 last_tested_at=0/coverage=0。
func TestRuleAgentListRules(t *testing.T) {
	b := ruleAgentTestBot(t, func(w http.ResponseWriter, r *http.Request) {})
	id := insertRule(t, b, "旧规则", "旧正则", "promo", true, false)
	run := &ruleAgentRun{sh: b.Shared}
	var got map[string]any
	if err := json.Unmarshal([]byte(run.listRules()), &got); err != nil {
		t.Fatal(err)
	}
	rules := got["rules"].([]any)
	if len(rules) != 1 || rules[0].(map[string]any)["id"].(float64) != float64(id) {
		t.Fatalf("list_rules 不对：%v", got)
	}
	if rules[0].(map[string]any)["coverage"].(float64) != 0 {
		t.Errorf("未测试规则 coverage 应为 0：%v", rules[0])
	}
	if rules[0].(map[string]any)["last_tested_at"].(float64) != 0 {
		t.Errorf("未测试规则 last_tested_at 应为 0：%v", rules[0])
	}

	// 测过的规则要透出覆盖率与按类型细分（落库 JSON 原样解析）。
	if _, err := b.Store.Write.Exec(`UPDATE ad_rules SET last_tp=1,last_ads_total=2,
		last_tested_at=1700000000,last_kinds=? WHERE id=?`,
		`[{"kind":"scam","total":2,"matched":1}]`, id); err != nil {
		t.Fatal(err)
	}
	var tested map[string]any
	if err := json.Unmarshal([]byte(run.listRules()), &tested); err != nil {
		t.Fatal(err)
	}
	row := tested["rules"].([]any)[0].(map[string]any)
	if row["coverage"].(float64) != 0.5 {
		t.Errorf("覆盖率应为 1/2=0.5：%v", row)
	}
	if kinds := row["last_kinds"].([]any); len(kinds) != 1 ||
		kinds[0].(map[string]any)["kind"] != "scam" {
		t.Errorf("last_kinds 应透出：%v", row["last_kinds"])
	}
}

// TestRuleAgentListRulesKeepsNewest：规则超过 100 条时仍返回最新创建的那条
//（ORDER BY id DESC LIMIT 100 再反转为升序）。
func TestRuleAgentListRulesKeepsNewest(t *testing.T) {
	b := ruleAgentTestBot(t, func(w http.ResponseWriter, r *http.Request) {})
	for i := 0; i < 101; i++ {
		if _, err := b.Store.Write.Exec(`INSERT INTO ad_rules
			(name,pattern,source,created_at,created_by)
			VALUES (?,?,'ai',0,1)`, fmt.Sprintf("规则%d", i), fmt.Sprintf("p%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	run := &ruleAgentRun{sh: b.Shared}
	var got map[string]any
	if err := json.Unmarshal([]byte(run.listRules()), &got); err != nil {
		t.Fatal(err)
	}
	rules := got["rules"].([]any)
	if len(rules) != 100 {
		t.Fatalf("应返回 100 条，得到 %d", len(rules))
	}
	if last := rules[len(rules)-1].(map[string]any); last["name"].(string) != "规则100" {
		t.Errorf("最新规则应保留在返回里：%v", last)
	}
}

// TestRuleAgentToolErrorsDoNotKillRun：参数畸形（limit 传字符串）与调用
// 不存在的工具都只作为工具结果回给模型，运行继续；模型据此收尾，整轮不报错。
func TestRuleAgentToolErrorsDoNotKillRun(t *testing.T) {
	var calls atomic.Int32
	b := ruleAgentTestBot(t, func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			// limit 是字符串：反序列化失败，走错误兜底。
			w.Write([]byte(oaToolCallReply("c1", "list_banned", map[string]any{
				"limit": "十"})))
		case 2:
			// 不存在的工具名：走 UnknownToolsHandler。
			w.Write([]byte(oaToolCallReply("c2", "hack_tool", map[string]any{})))
		default:
			w.Write([]byte(oaTextReply("参数问题已了解，本轮不创建规则。")))
		}
	})

	if err := StartRuleDiscovery(b.Shared, 1); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	st := waitRuleAgentDone(t, 10*time.Second)
	if st["error"] != "" {
		t.Errorf("工具错误不应让整轮失败：error=%v result=%v", st["error"], st["result"])
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("模型收到错误后应继续到第 3 轮，实际请求 %d 次", got)
	}

	var sawBadArgs, sawUnknown bool
	steps, _ := st["steps"].([]map[string]any)
	for _, s := range steps {
		if s["kind"] != "tool" {
			continue
		}
		sum, _ := s["summary"].(string)
		switch s["name"] {
		case "list_banned":
			if strings.Contains(sum, "调用失败") {
				sawBadArgs = true
			}
		case "hack_tool":
			if strings.Contains(sum, "没有名为 hack_tool 的工具") {
				sawUnknown = true
			}
		}
	}
	if !sawBadArgs {
		t.Errorf("步骤日志应记录参数解析失败：%v", steps)
	}
	if !sawUnknown {
		t.Errorf("步骤日志应记录未知工具：%v", steps)
	}
}

// TestRuleAgentScanCancellation：find/test_rule 的扫描在 ctx 取消后
// 尽快返回「已停止」，不必扫完全库；未取消时同一批数据完整扫完。
func TestRuleAgentScanCancellation(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	// 超过 ruleScanCtxCheckEvery 行才会经过取消检查点。
	const rows = ruleScanCtxCheckEvery + 5

	tx, err := b.Store.Write.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,created_at,bot_id)
		VALUES (-100,555,?,'批量广告测试','ad',0.9,'so','scam','deleted','',?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < rows; i++ {
		if _, err := stmt.Exec(i, time.Now().Unix(), b.BotID()); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	run := &ruleAgentRun{sh: b.Shared}
	if got := run.findMatches(ctx, findArgs{
		Pattern: "批量广告", Scope: "all", Limit: 5}); !strings.Contains(got, "已停止") {
		t.Errorf("find 在取消后应返回已停止，得到 %q", got)
	}
	if got := run.testRule(ctx, "批量广告"); !strings.Contains(got, "已停止") {
		t.Errorf("test_rule 在取消后应返回已停止，得到 %q", got)
	}
	if _, err := testRulePatternCtx(ctx, b.Shared, "批量广告"); !errors.Is(err, errRuleScanStopped) {
		t.Errorf("底层扫描取消应返回 errRuleScanStopped，得到 %v", err)
	}

	res, err := testRulePatternCtx(context.Background(), b.Shared, "批量广告")
	if err != nil || res.Scanned != rows {
		t.Errorf("未取消时应扫完 %d 行，得到 scanned=%d err=%v", rows, res.Scanned, err)
	}
}

// TestRuleAgentEvidenceFilter：证据 id 只保留库里 verdict='ad' 的行，
// 去重、限量；无效与超限的都计入 ignored。
func TestRuleAgentEvidenceFilter(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	adID := insertRuleAgentLog(t, b, "广告一", "ad", "deleted", "scam")
	cleanID := insertRuleAgentLog(t, b, "正常聊天", "clean", "none", "")
	undoneID := insertRuleAgentLog(t, b, "被撤销的广告", "ad", "undone", "scam")

	run := &ruleAgentRun{sh: b.Shared}
	valid, ignored := run.filterEvidence(
		[]int64{adID, cleanID, 99999, adID, 0, undoneID, -1})
	want := []int64{adID, undoneID}
	if !slices.Equal(valid, want) {
		t.Errorf("有效证据应为 %v，得到 %v", want, valid)
	}
	if ignored != 5 {
		t.Errorf("应忽略 5 个（正常消息/不存在/重复/0/负数），得到 %d", ignored)
	}

	// 限量 20：26 个候选里只留 20 个，其余计入 ignored。
	ids := []int64{adID}
	for i := 0; i < ruleAgentEvidenceMax+5; i++ {
		ids = append(ids, insertRuleAgentLog(t, b, "批量广告", "ad", "deleted", "scam"))
	}
	valid, ignored = run.filterEvidence(ids)
	if len(valid) != ruleAgentEvidenceMax {
		t.Errorf("最多保留 %d 个证据，得到 %d", ruleAgentEvidenceMax, len(valid))
	}
	if ignored != 6 {
		t.Errorf("26 个候选应忽略 6 个超限，得到 %d", ignored)
	}
}

// TestRuleAgentCreateRuleAllowsMultiplePerRun：同一轮连续创建不同 pattern
// 都写库，createdRuleIDs 累计，成功不增加失败计数。
func TestRuleAgentCreateRuleAllowsMultiplePerRun(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	insertRuleAgentLog(t, b, "广告文案一", "ad", "deleted", "scam")
	insertRuleAgentLog(t, b, "广告文案二", "ad", "deleted", "promo")

	run := &ruleAgentRun{sh: b.Shared, ctx: context.Background()}
	if got := run.createRule(context.Background(), createRuleArgs{
		Name: "规则一", Pattern: "广告文案一"}); !strings.Contains(got, "创建成功") {
		t.Fatalf("第一条应创建成功，得到 %q", got)
	}
	if got := run.createRule(context.Background(), createRuleArgs{
		Name: "规则二", Pattern: "广告文案二"}); !strings.Contains(got, "创建成功") {
		t.Fatalf("第二条也应创建成功，得到 %q", got)
	}
	if len(run.createdRuleIDs) != 2 || run.createFails != 0 {
		t.Errorf("createdRuleIDs=%v createFails=%d", run.createdRuleIDs, run.createFails)
	}
	var n int
	if err := b.Store.Read.QueryRow(`SELECT COUNT(*) FROM ad_rules`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("库里应有 2 条规则，得到 %d", n)
	}
}

// TestRuleAgentCreatesMultipleRules：一轮内可以连续创建多条规则，
// 状态带 created_rule_ids，结果文案列出全部。
func TestRuleAgentCreatesMultipleRules(t *testing.T) {
	var calls atomic.Int32
	b := ruleAgentTestBot(t, func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			w.Write([]byte(oaToolCallReply("c1", "create_rule", map[string]any{
				"name": "规则一", "pattern": "办理贷款", "category": "promo"})))
		case 2:
			w.Write([]byte(oaToolCallReply("c2", "list_rules", map[string]any{})))
		case 3:
			w.Write([]byte(oaToolCallReply("c3", "create_rule", map[string]any{
				"name": "规则二", "pattern": "博彩娱乐", "category": "gambling"})))
		default:
			w.Write([]byte(oaTextReply("本轮完成，共两条。")))
		}
	})
	insertRuleAgentLog(t, b, "办理贷款加微信 vx123", "ad", "deleted", "scam")
	insertRuleAgentLog(t, b, "博彩娱乐平台开户", "ad", "deleted", "gambling")

	if err := StartRuleDiscovery(b.Shared, 7); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	st := waitRuleAgentDone(t, 15*time.Second)
	ids, _ := st["created_rule_ids"].([]int64)
	if len(ids) != 2 {
		t.Fatalf("应创建两条规则，状态 %v", st)
	}
	if st["created_rule_id"] != ids[0] {
		t.Errorf("created_rule_id 应保留为第一条：%v", st)
	}
	var n int64
	if err := b.Store.Read.QueryRow(`SELECT COUNT(*) FROM ad_rules`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("库里应有 2 条规则，得到 %d", n)
	}
	var adsTotal int64
	var kinds string
	if err := b.Store.Read.QueryRow(`SELECT last_ads_total,last_kinds FROM ad_rules
		WHERE id=?`, ids[0]).Scan(&adsTotal, &kinds); err != nil {
		t.Fatal(err)
	}
	if adsTotal != 2 || !strings.Contains(kinds, `"kind":"scam"`) {
		// 两条广告都在开始前入库：第一条规则的扫描窗口里 AdsTotal=2。
		t.Errorf("AI 创建也要落覆盖率：ads=%d kinds=%s", adsTotal, kinds)
	}
	if result, _ := st["result"].(string); !strings.Contains(result, "2 条") {
		t.Errorf("结果应说明两条：%q", result)
	}
}

// TestRuleAgentCreatedRuleIDsEmpty：未创建/失败时是 [] 而不是 null。
func TestRuleAgentCreatedRuleIDsEmpty(t *testing.T) {
	b := ruleAgentTestBot(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(oaTextReply("没有发现。")))
	})
	if err := StartRuleDiscovery(b.Shared, 7); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	st := waitRuleAgentDone(t, 15*time.Second)
	ids, ok := st["created_rule_ids"].([]int64)
	if !ok || ids == nil || len(ids) != 0 {
		t.Errorf("created_rule_ids 应为空切片，得到 %#v", st["created_rule_ids"])
	}
}

// TestRuleAgentCreateFailsResetOnSuccess：连续失败计数在成功创建后清零。
// 观察点：模型调用次数——若成功后不清零，第 4 次失败就累计到 3，
// SetReturnDirectly 会提前收尾，第 6 次模型调用不会发生。
func TestRuleAgentCreateFailsResetOnSuccess(t *testing.T) {
	var calls atomic.Int32
	b := ruleAgentTestBot(t, func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1, 2, 4, 5:
			// 坏 pattern `[` 走严格编译失败，每次都算一次创建失败。
			w.Write([]byte(oaToolCallReply(fmt.Sprintf("c%d", n), "create_rule",
				map[string]any{"name": "坏规则", "pattern": "["})))
		case 3:
			w.Write([]byte(oaToolCallReply("c3", "create_rule", map[string]any{
				"name": "好规则", "pattern": "办理贷款"})))
		default:
			w.Write([]byte(oaTextReply("收尾。")))
		}
	})
	insertRuleAgentLog(t, b, "办理贷款加微信", "ad", "deleted", "scam")

	if err := StartRuleDiscovery(b.Shared, 7); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	st := waitRuleAgentDone(t, 15*time.Second)
	if got := calls.Load(); got < 6 {
		t.Errorf("成功后失败计数应清零、跑满 6 次模型调用，实际 %d 次（状态 %v）", got, st)
	}
	ids, _ := st["created_rule_ids"].([]int64)
	if len(ids) != 1 {
		t.Errorf("应只成功创建 1 条，得到 %v（状态 %v）", ids, st)
	}
	if errText, _ := st["error"].(string); strings.Contains(errText, "连续") {
		t.Errorf("不该触发连续失败收尾：%q", errText)
	}
}

// TestRuleAgentCreateRuleNotesOnlyValidEvidence：create_rule 只把有效的
// 广告证据写进 note，并在工具结果里注明忽略数量。
func TestRuleAgentCreateRuleNotesOnlyValidEvidence(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	adID := insertRuleAgentLog(t, b, "有效广告证据", "ad", "deleted", "scam")
	cleanID := insertRuleAgentLog(t, b, "正常消息", "clean", "none", "")

	run := &ruleAgentRun{sh: b.Shared, ctx: context.Background()}
	got := run.createRule(context.Background(), createRuleArgs{
		Name: "证据校验", Category: "promo", Pattern: "有效广告证据",
		Note:        "理由：测试",
		EvidenceIDs: []int64{adID, cleanID, 99999},
	})
	if !strings.Contains(got, "创建成功") {
		t.Fatalf("应创建成功，得到 %q", got)
	}
	if !strings.Contains(got, "已忽略 2 个") {
		t.Errorf("工具结果应注明忽略数量，得到 %q", got)
	}

	var note string
	if err := b.Store.Read.QueryRow(`SELECT note FROM ad_rules`).Scan(&note); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "#"+itoa(adID)) {
		t.Errorf("note 应列出有效证据 #%d：%q", adID, note)
	}
	if strings.Contains(note, "#"+itoa(cleanID)) || strings.Contains(note, "#99999") {
		t.Errorf("note 不应列出无效/非广告证据：%q", note)
	}
}

// TestRuleAgentTestRuleBlocksFP：test_rule 对命中正常消息的正则必须给出
// 明确的「不可创建」结论与误封样本。
func TestRuleAgentTestRuleBlocksFP(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	insertRuleAgentLog(t, b, "正常聊天记录", "clean", "none", "")

	run := &ruleAgentRun{sh: b.Shared}
	raw := run.testRule(context.Background(), "正常聊天")
	var got struct {
		CanCreate bool              `json:"can_create"`
		Message   string            `json:"message"`
		FPSamples []ruleAgentSample `json:"fp_samples"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("test_rule 返回不是 JSON：%v\n%s", err, raw)
	}
	if got.CanCreate || !strings.Contains(got.Message, "不可创建") {
		t.Errorf("命中正常消息应不可创建：%+v", got)
	}
	if len(got.FPSamples) != 1 || got.FPSamples[0].Verdict != "clean" {
		t.Errorf("应带误封样本：%+v", got.FPSamples)
	}
}
