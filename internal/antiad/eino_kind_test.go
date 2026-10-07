package antiad

// 规则发现多渠道传输层翻译的测试：请求翻译（system / tool_calls / tool
// 结果 / 工具定义的搬运）、响应翻译（tool_calls 与 finish_reason 的还原）、
// RT 的鉴权头与错误体改写，以及模型选择对翻译渠道的放开。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"menshen/internal/core"
	"menshen/internal/upstream"

	"menshen/internal/testutil"
)

// sampleWireReq 造一个有代表性的 Eino 请求：system + user + assistant
// （带工具调用）+ tool 结果 + 工具定义。
func sampleWireReq(model string) *einoWireReq {
	zero := float32(0)
	return &einoWireReq{
		Model: model,
		Messages: []einoWireMsg{
			{Role: "system", Content: "你是规则发现工程师"},
			{Role: "user", Content: "请开始一轮规则发现"},
			{Role: "assistant", Content: "我先查一下",
				ToolCalls: []einoWireCall{{
					ID: "call_find_0", Type: "function",
					Function: einoWireFunc{Name: "find", Arguments: `{"pattern":"加微信","limit":5}`},
				}}},
			{Role: "tool", ToolCallID: "call_find_0", Content: `{"matched":3}`},
		},
		Tools: []einoWireTool{{
			Type: "function",
			Function: einoWireToolFunc{
				Name: "find", Description: "用正则扫描流水",
				Parameters: json.RawMessage(`{"type":"object","properties":{` +
					`"pattern":{"type":"string","description":"正则"}},` +
					`"required":["pattern"],"additionalProperties":false}`),
			},
		}},
		Temperature: &zero,
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decodeJSON(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("响应不是合法 JSON：%v\n%s", err, b)
	}
	return m
}

// TestResponsesRequestTranslation：Responses 请求的形状。
func TestResponsesRequestTranslation(t *testing.T) {
	body := responsesRequest(sampleWireReq("gpt-6-luna"))

	if body["instructions"] != "你是规则发现工程师" {
		t.Errorf("system 应进 instructions：%v", body["instructions"])
	}
	if body["stream"] != false {
		t.Errorf("翻译渠道固定非流式：%v", body["stream"])
	}
	if body["temperature"] != float32(0) {
		t.Errorf("temperature 应透传：%v", body["temperature"])
	}
	input, _ := body["input"].([]map[string]any)
	if len(input) != 4 {
		t.Fatalf("input 应有 4 项（user/assistant/function_call/function_call_output）：%d", len(input))
	}
	if input[0]["role"] != "user" || input[1]["role"] != "assistant" {
		t.Errorf("前两项应是 user 与 assistant 消息：%v / %v", input[0], input[1])
	}
	fc := input[2]
	if fc["type"] != "function_call" || fc["call_id"] != "call_find_0" ||
		fc["name"] != "find" || fc["arguments"] != `{"pattern":"加微信","limit":5}` {
		t.Errorf("function_call 项形状不对：%v", fc)
	}
	out := input[3]
	if out["type"] != "function_call_output" || out["call_id"] != "call_find_0" ||
		out["output"] != `{"matched":3}` {
		t.Errorf("function_call_output 项形状不对：%v", out)
	}
	tools, _ := body["tools"].([]map[string]any)
	if len(tools) != 1 || tools[0]["type"] != "function" ||
		tools[0]["name"] != "find" || tools[0]["description"] != "用正则扫描流水" {
		t.Errorf("工具定义应是扁平形状：%v", tools)
	}
	if _, ok := tools[0]["parameters"]; !ok {
		t.Error("工具定义应带 parameters")
	}
}

// TestAnthropicRequestTranslation：Messages 请求的形状。
func TestAnthropicRequestTranslation(t *testing.T) {
	body := anthropicRequest(sampleWireReq("claude-x"))

	if body["system"] != "你是规则发现工程师" {
		t.Errorf("system 应进顶层 system：%v", body["system"])
	}
	if body["stream"] != false {
		t.Errorf("翻译渠道固定非流式：%v", body["stream"])
	}
	if body["max_tokens"] != ruleAgentAnthropicMaxTokens {
		t.Errorf("未传 max_tokens 时应给默认值：%v", body["max_tokens"])
	}
	msgs, _ := body["messages"].([]map[string]any)
	// system 被抽出后剩 3 条：user / assistant(tool_use) / user(tool_result)。
	if len(msgs) != 3 {
		t.Fatalf("messages 应有 3 条：%d\n%s", len(msgs), body["messages"])
	}
	if msgs[0]["role"] != "user" {
		t.Errorf("第 1 条应是 user：%v", msgs[0])
	}
	if msgs[1]["role"] != "assistant" {
		t.Errorf("第 2 条应是 assistant：%v", msgs[1])
	}
	blocks, _ := msgs[1]["content"].([]map[string]any)
	if len(blocks) != 2 {
		t.Fatalf("assistant 应有 text + tool_use 两个块：%v", blocks)
	}
	if blocks[0]["type"] != "text" || blocks[0]["text"] != "我先查一下" {
		t.Errorf("text 块不对：%v", blocks[0])
	}
	if blocks[1]["type"] != "tool_use" || blocks[1]["id"] != "call_find_0" ||
		blocks[1]["name"] != "find" {
		t.Errorf("tool_use 块不对：%v", blocks[1])
	}
	inputArgs, _ := blocks[1]["input"].(map[string]any)
	if inputArgs["pattern"] != "加微信" {
		t.Errorf("arguments 应解析成 input 对象：%v", blocks[1]["input"])
	}
	if msgs[2]["role"] != "user" {
		t.Errorf("tool 结果应合并成一条 user 消息：%v", msgs[2])
	}
	trBlocks, _ := msgs[2]["content"].([]map[string]any)
	if len(trBlocks) != 1 || trBlocks[0]["type"] != "tool_result" ||
		trBlocks[0]["tool_use_id"] != "call_find_0" || trBlocks[0]["content"] != `{"matched":3}` {
		t.Errorf("tool_result 块不对：%v", trBlocks)
	}
	tools, _ := body["tools"].([]map[string]any)
	if len(tools) != 1 || tools[0]["name"] != "find" {
		t.Errorf("工具定义应映射为 name/description/input_schema：%v", tools)
	}
	if _, ok := tools[0]["input_schema"]; !ok {
		t.Error("工具定义应带 input_schema")
	}
}

// TestGeminiRequestTranslation：generateContent 请求的形状。
func TestGeminiRequestTranslation(t *testing.T) {
	body := geminiRequest(sampleWireReq("gemini-2.5"))

	if body["systemInstruction"] == nil {
		t.Error("system 应进 systemInstruction")
	}
	contents, _ := body["contents"].([]map[string]any)
	if len(contents) != 3 {
		t.Fatalf("contents 应有 3 条（user/model/functionResponse）：%d", len(contents))
	}
	if contents[0]["role"] != "user" || contents[1]["role"] != "model" {
		t.Errorf("角色映射不对：%v / %v", contents[0], contents[1])
	}
	parts, _ := contents[1]["parts"].([]map[string]any)
	if len(parts) != 2 {
		t.Fatalf("model 轮应有 text + functionCall 两个 part：%v", parts)
	}
	call, _ := parts[1]["functionCall"].(map[string]any)
	if call["name"] != "find" {
		t.Errorf("functionCall 名字不对：%v", parts[1])
	}
	args, _ := call["args"].(map[string]any)
	if args["pattern"] != "加微信" {
		t.Errorf("arguments 应解析成 args 对象：%v", call["args"])
	}
	respParts, _ := contents[2]["parts"].([]map[string]any)
	fr, _ := respParts[0]["functionResponse"].(map[string]any)
	if fr["name"] != "find" {
		t.Errorf("functionResponse 应带从 call id 还原的名字：%v", fr)
	}
	frr, _ := fr["response"].(map[string]any)
	if frr["result"] != `{"matched":3}` {
		t.Errorf("functionResponse.result 应是工具结果串：%v", fr)
	}
	tools, _ := body["tools"].([]map[string]any)
	decls, _ := tools[0]["functionDeclarations"].([]map[string]any)
	if len(decls) != 1 || decls[0]["name"] != "find" {
		t.Fatalf("functionDeclarations 形状不对：%v", tools)
	}
	params, _ := decls[0]["parameters"].(map[string]any)
	if _, bad := params["additionalProperties"]; bad {
		t.Error("Gemini schema 应剔除 additionalProperties")
	}
	props, _ := params["properties"].(map[string]any)
	pattern, _ := props["pattern"].(map[string]any)
	if pattern["type"] != "string" {
		t.Errorf("properties 应原样保留 type/description：%v", pattern)
	}
}

// TestGeminiSchemaNullable：可空类型数组摊平成 type + nullable。
func TestGeminiSchemaNullable(t *testing.T) {
	out := geminiSchema(json.RawMessage(`{"type":["string","null"],"description":"x"}`))
	if out["type"] != "string" || out["nullable"] != true || out["description"] != "x" {
		t.Errorf("可空类型应摊平：%v", out)
	}
}

// TestGeminiFuncNameOfCallID：合成 id 与还原互为逆操作。
func TestGeminiFuncNameOfCallID(t *testing.T) {
	for _, name := range []string{"find", "list_kinds", "test_rule", "read_records"} {
		id := fmt.Sprintf("call_%s_0", name)
		if got := geminiFuncNameOfCallID(id); got != name {
			t.Errorf("id %q 应还原成 %q，得到 %q", id, name, got)
		}
	}
}

// captureRT 捕获翻译后的出站请求，返回预置的渠道响应。
type captureRT struct {
	req  *http.Request
	body []byte
	resp *http.Response
}

func (c *captureRT) RoundTrip(r *http.Request) (*http.Response, error) {
	c.req = r
	c.body, _ = io.ReadAll(r.Body)
	return c.resp, nil
}

func cannedResponse(status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Header:        http.Header{"Content-Type": []string{contentType}},
	}
}

// buildKindRT 构造一个指向 captureRT 的翻译 RT。
func buildKindRT(t *testing.T, kind upstream.Kind, target string, cap *captureRT) *einoKindRT {
	t.Helper()
	return &einoKindRT{kind: kind, target: target, apiKey: "sk-test", base: cap}
}

// roundTrip 通过 RT 发一个 Eino 请求，返回翻译后的响应体。
func roundTrip(t *testing.T, rt *einoKindRT, cap *captureRT) map[string]any {
	t.Helper()
	wire := mustMarshal(t, sampleWireReq("m1"))
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"http://eino.local/v1/chat/completions", bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip 失败：%v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	out := decodeJSON(t, b)
	if got := out["choices"].([]any)[0].(map[string]any)["message"]; got == nil {
		t.Fatalf("翻译响应缺少 message：%s", b)
	}
	return out
}

// TestEinoKindRTResponses：Responses 渠道的端到端翻译（RT 层）。
func TestEinoKindRTResponses(t *testing.T) {
	cap := &captureRT{resp: cannedResponse(200, "application/json", `{
		"status":"completed",
		"output":[
			{"type":"reasoning","summary":[{"type":"summary_text","text":"想一想"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"好的"}]},
			{"type":"function_call","call_id":"call_9","name":"test_rule","arguments":"{\"pattern\":\"a+\"}"}
		],
		"usage":{"input_tokens":10,"output_tokens":5}}`)}
	rt := buildKindRT(t, upstream.KindOpenAIResp, "https://gw.example/v1/responses", cap)
	out := roundTrip(t, rt, cap)

	if cap.req.URL.String() != "https://gw.example/v1/responses" {
		t.Errorf("应改写到 /v1/responses：%s", cap.req.URL)
	}
	if auth := cap.req.Header.Get("Authorization"); auth != "Bearer sk-test" {
		t.Errorf("Responses 渠道沿用 Bearer：%q", auth)
	}
	var sent map[string]any
	if err := json.Unmarshal(cap.body, &sent); err != nil {
		t.Fatalf("出站请求不是 JSON：%v", err)
	}
	if sent["stream"] != false {
		t.Errorf("出站请求应固定 stream=false：%v", sent["stream"])
	}
	choice := out["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Errorf("有 function_call 时 finish_reason 应是 tool_calls：%v", choice["finish_reason"])
	}
	msg := choice["message"].(map[string]any)
	if msg["content"] != "好的" || msg["reasoning_content"] != "想一想" {
		t.Errorf("content/reasoning 还原不对：%v", msg)
	}
	calls, _ := msg["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("应还原 1 个 tool_call：%v", msg["tool_calls"])
	}
	tc := calls[0].(map[string]any)
	if tc["id"] != "call_9" {
		t.Errorf("tool_call id 应来自 call_id：%v", tc)
	}
	fn := tc["function"].(map[string]any)
	if fn["name"] != "test_rule" || fn["arguments"] != `{"pattern":"a+"}` {
		t.Errorf("tool_call 函数形状不对：%v", fn)
	}
}

// TestEinoKindRTAnthropic：Anthropic 渠道的端到端翻译（RT 层）。
func TestEinoKindRTAnthropic(t *testing.T) {
	cap := &captureRT{resp: cannedResponse(200, "application/json", `{
		"content":[
			{"type":"text","text":"我看看"},
			{"type":"tool_use","id":"toolu_1","name":"list_kinds","input":{"x":1}}
		],
		"stop_reason":"tool_use",
		"usage":{"input_tokens":12,"output_tokens":7}}`)}
	rt := buildKindRT(t, upstream.KindAnthropic, "https://api.example/v1/messages", cap)
	out := roundTrip(t, rt, cap)

	if cap.req.URL.String() != "https://api.example/v1/messages" {
		t.Errorf("应改写到 /v1/messages：%s", cap.req.URL)
	}
	if key := cap.req.Header.Get("x-api-key"); key != "sk-test" {
		t.Errorf("Anthropic 应用 x-api-key：%q", key)
	}
	if cap.req.Header.Get("anthropic-version") == "" {
		t.Error("Anthropic 应带 anthropic-version 头")
	}
	if cap.req.Header.Get("Authorization") != "" {
		t.Error("Anthropic 不应带 Authorization 头")
	}
	var sent map[string]any
	if err := json.Unmarshal(cap.body, &sent); err != nil {
		t.Fatalf("出站请求不是 JSON：%v", err)
	}
	if sent["max_tokens"] != float64(ruleAgentAnthropicMaxTokens) {
		t.Errorf("出站请求应带默认 max_tokens：%v", sent["max_tokens"])
	}
	choice := out["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Errorf("stop_reason=tool_use 应映射为 tool_calls：%v", choice["finish_reason"])
	}
	msg := choice["message"].(map[string]any)
	if msg["content"] != "我看看" {
		t.Errorf("content 还原不对：%v", msg)
	}
	calls, _ := msg["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("应还原 1 个 tool_call：%v", msg["tool_calls"])
	}
	fn := calls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "list_kinds" {
		t.Errorf("tool_call 名字不对：%v", fn)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(fn["arguments"].(string)), &args); err != nil ||
		args["x"] != float64(1) {
		t.Errorf("tool_use.input 应序列化成 arguments 串：%v", fn["arguments"])
	}
}

// TestEinoKindRTGemini：Gemini 渠道的端到端翻译（RT 层）。
func TestEinoKindRTGemini(t *testing.T) {
	cap := &captureRT{resp: cannedResponse(200, "application/json", `{
		"candidates":[{"content":{"parts":[
			{"text":"查一下"},
			{"functionCall":{"name":"create_rule","args":{"name":"测试","pattern":"x"}}}
		]},"finishReason":"STOP"}],
		"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":4}}`)}
	rt := buildKindRT(t, upstream.KindGemini,
		"https://generativelanguage.googleapis.com/v1beta/models/m:generateContent", cap)
	out := roundTrip(t, rt, cap)

	if cap.req.Header.Get("x-goog-api-key") != "sk-test" {
		t.Error("Gemini 应用 x-goog-api-key 鉴权")
	}
	var sent map[string]any
	if err := json.Unmarshal(cap.body, &sent); err != nil {
		t.Fatalf("出站请求不是 JSON：%v", err)
	}
	if _, ok := sent["contents"]; !ok {
		t.Error("出站请求应有 contents")
	}
	choice := out["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Errorf("有 functionCall 时 finish_reason 应是 tool_calls：%v", choice["finish_reason"])
	}
	msg := choice["message"].(map[string]any)
	calls, _ := msg["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("应还原 1 个 tool_call：%v", msg["tool_calls"])
	}
	tc := calls[0].(map[string]any)
	fn := tc["function"].(map[string]any)
	if fn["name"] != "create_rule" {
		t.Errorf("tool_call 名字不对：%v", fn)
	}
	// 合成 id 必须能在下一轮请求翻译时还原出 function 名。
	if got := geminiFuncNameOfCallID(tc["id"].(string)); got != "create_rule" {
		t.Errorf("合成 id %v 应能还原名字： got %q", tc["id"], got)
	}
}

// TestEinoKindRTRewritesErrors：4xx/5xx 错误体统一改写成 OpenAI 形状，
// 状态码保留（重试层按状态码分类）。
func TestEinoKindRTRewritesErrors(t *testing.T) {
	cap := &captureRT{resp: cannedResponse(429, "application/json",
		`{"type":"error","error":{"type":"rate_limit_error","message":"负载已饱和"}}`)}
	rt := buildKindRT(t, upstream.KindAnthropic, "https://api.example/v1/messages", cap)

	wire := mustMarshal(t, sampleWireReq("m1"))
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"http://eino.local/v1/chat/completions", bytes.NewReader(wire))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 429 {
		t.Errorf("状态码应保留 429：%d", resp.StatusCode)
	}
	out := decodeJSON(t, mustReadAll(t, resp.Body))
	errObj, _ := out["error"].(map[string]any)
	if errObj == nil || errObj["message"] != "负载已饱和" {
		t.Errorf("错误体应改写成 OpenAI 形状并保留 message：%v", out)
	}
}

// TestEinoKindRTRejectsSSE：网关无视 stream=false 回 SSE 时给出明确错误。
func TestEinoKindRTRejectsSSE(t *testing.T) {
	cap := &captureRT{resp: cannedResponse(200, "text/event-stream", "data: {}\n\n")}
	rt := buildKindRT(t, upstream.KindGemini, "https://x/models/m:generateContent", cap)

	wire := mustMarshal(t, sampleWireReq("m1"))
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"http://eino.local/v1/chat/completions", bytes.NewReader(wire))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := decodeJSON(t, mustReadAll(t, resp.Body))
	errObj, _ := out["error"].(map[string]any)
	if errObj == nil || !strings.Contains(errObj["message"].(string), "stream=false") {
		t.Errorf("SSE 响应应换成明确的错误说明：%v", out)
	}
}

func mustReadAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestEinoTargetURL：各渠道端点拼接约定。
func TestEinoTargetURL(t *testing.T) {
	cases := []struct {
		kind          upstream.Kind
		base, modelID string
		want          string
	}{
		{upstream.KindOpenAIResp, "https://gw.example", "m", "https://gw.example/v1/responses"},
		{upstream.KindOpenAIResp, "https://nekoapi.ai/v1/", "m", "https://nekoapi.ai/v1/responses"},
		{upstream.KindAnthropic, "https://api.anthropic.com", "m", "https://api.anthropic.com/v1/messages"},
		{upstream.KindGemini, "https://generativelanguage.googleapis.com/v1beta", "gem-2",
			"https://generativelanguage.googleapis.com/v1beta/models/gem-2:generateContent"},
	}
	for _, c := range cases {
		u := &upstream.Upstream{Name: "t", BaseURL: c.base, Kind: c.kind}
		got, err := einoTargetURL(u, c.modelID)
		if err != nil || got != c.want {
			t.Errorf("%s 端点应为 %s，得到 %s（err=%v）", c.kind, c.want, got, err)
		}
	}
	if _, err := einoTargetURL(&upstream.Upstream{Name: "t", Kind: upstream.KindGemini}, ""); err == nil {
		t.Error("Gemini 缺模型 ID 应报错")
	}
}

// TestPickConfiguredRuleAgentModelAcceptsTranslatedKinds：Responses /
// Anthropic / Gemini 渠道都应被接受。
func TestPickConfiguredRuleAgentModelAcceptsTranslatedKinds(t *testing.T) {
	for _, kind := range []string{"openai-responses", "anthropic", "gemini"} {
		t.Run(kind, func(t *testing.T) {
			b, _ := testutil.NewTestBot(t, 1)
			if _, err := b.Store.Write.Exec(`INSERT INTO upstreams
				(name,base_url,api_key,weight,status,supports_chat,supports_systemone,kind)
				VALUES ('up','http://x','k',1,1,1,0,?)`, kind); err != nil {
				t.Fatal(err)
			}
			if _, err := b.Store.Write.Exec(`INSERT INTO models
				(name,prompt_price,completion_price,cache_read_price,cache_write_price,enabled)
				VALUES ('up/m1',0,0,0,0,1)`); err != nil {
				t.Fatal(err)
			}
			if err := b.PutSetting("antiad_rule_model", "up/m1"); err != nil {
				t.Fatal(err)
			}
			snap := b.Shared.Cache.Snap()
			name, u, err := pickRuleAgentModel(snap)
			if err != nil {
				t.Fatalf("%s 渠道应被接受：%v", kind, err)
			}
			if name != "up/m1" || u.EffectiveKind().Label() == "" {
				t.Errorf("应选中配置的模型与上游：name=%q u=%v", name, u)
			}
			// 客户端构建也要能走通（不发网络请求）。
			if _, err := newRuleAgentChatModel(b.Shared, u, name); err != nil {
				t.Fatalf("%s 渠道应能构建 Eino 客户端：%v", kind, err)
			}
		})
	}
}

// TestStartRuleDiscoveryCloudflareRejected：cloudflare 渠道仍被拒绝。
func TestStartRuleDiscoveryCloudflareRejected(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	if _, err := b.Store.Write.Exec(`INSERT INTO upstreams
		(name,base_url,api_key,weight,status,supports_chat,supports_systemone,kind)
		VALUES ('cf','http://x','k',1,1,1,0,'cloudflare')`); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Store.Write.Exec(`INSERT INTO models
		(name,prompt_price,completion_price,cache_read_price,cache_write_price,enabled)
		VALUES ('cf/m1',0,0,0,0,1)`); err != nil {
		t.Fatal(err)
	}
	if err := b.PutSetting("antiad_rule_model", "cf/m1"); err != nil {
		t.Fatal(err)
	}
	err := StartRuleDiscovery(b.Shared, 1)
	if err == nil || !strings.Contains(err.Error(), "没有工具调用能力") {
		t.Fatalf("cloudflare 渠道应被明确拒绝：%v", err)
	}
}

// 编译依赖保护：core 包在本文件只用到类型引用。
var _ = core.Shared{}
