package antiad

// 本文件实现规则发现 Agent 的多渠道传输层翻译。Eino 的 ReAct 图只会说
// OpenAI chat/completions（tools / tool_calls / role:"tool"），而上游渠道
// 有好几种协议。eino_stream.go 已在传输层给 OpenAI Completions 渠道做了
// 流式转换；这里用同样的思路覆盖其余支持工具调用的渠道：
//
//   - OpenAI Responses（/v1/responses）
//   - Anthropic Messages（/v1/messages）
//   - Google Gemini（generateContent）
//
// RT 把 Eino 发出的 chat/completions 请求整体改写成目标渠道的端点、鉴权
// 头与请求体，把渠道响应翻译回 chat/completions —— Eino 与 ReAct 图对
// 渠道差异完全无感，工具定义、工具调用与工具结果的往返由这里的翻译保证。
//
// 与判定链路适配层（upstream 包）的差异：那边服务的是纯文本对话，会丢掉
// tool_calls；工具调用的多轮往返（含 tool_call_id 的对应关系）只有这里需要。
//
// 非流式取舍：翻译渠道按 stream:false 发请求、读整包 JSON。判定链路走流式
// 是为了首字看门狗与长输出不拖死整包超时；规则发现没有看门狗，整包响应
// （含工具调用）重建比逐家解析 SSE 分片可靠得多，客户端 100 秒超时与重试
// 层（超时可重试）足够兜底。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/upstream"
)

// einoKindHTTPClient 给非 OpenAI Completions 渠道建一个带传输层翻译的
// HTTP 客户端。base 为 nil 时新建（100 秒超时，与判定链路一致），否则克隆
// 以保留连接池与超时。
func einoKindHTTPClient(base *http.Client, u *upstream.Upstream,
	fullName string) (*http.Client, error) {

	_, id := upstream.SplitModelName(fullName)
	target, err := einoTargetURL(u, id)
	if err != nil {
		return nil, err
	}
	c := &http.Client{Timeout: 100 * time.Second}
	var inner http.RoundTripper
	if base != nil {
		cc := *base
		c = &cc
		inner = base.Transport
	}
	c.Transport = &einoKindRT{
		kind: u.EffectiveKind(), target: target, apiKey: u.APIKey, base: inner,
	}
	return c, nil
}

// einoTargetURL 按渠道给出真实端点。base_url 约定与 upstream.URL 一致
// （Anthropic 到域名、Gemini 到 /v1beta），只有 Gemini 不带 ?alt=sse：
// 这里固定非流式，用 :generateContent。
func einoTargetURL(u *upstream.Upstream, modelID string) (string, error) {
	base := strings.TrimRight(u.BaseURL, "/")
	if base == "" {
		return "", fmt.Errorf("上游 %s 未配置 base_url", u.Name)
	}
	// 容错：有的管理员按 OpenAI 习惯把 base_url 登记成带 /v1 的网关前缀
	//（OpenAI Completions 渠道两种写法等价）。翻译渠道在 base 后接自己的
	// 路径，先把尾巴上的 /v1 去掉再拼，两种写法都能拼出正确端点。
	base = strings.TrimSuffix(base, "/v1")
	switch u.EffectiveKind() {
	case upstream.KindOpenAIResp:
		return base + "/v1/responses", nil
	case upstream.KindAnthropic:
		return base + "/v1/messages", nil
	case upstream.KindGemini:
		if modelID == "" {
			return "", fmt.Errorf("模型 %q 缺少模型 ID", modelID)
		}
		return base + "/models/" + modelID + ":generateContent", nil
	}
	return "", fmt.Errorf("上游 %s 是 %s 渠道，不需要传输层翻译",
		u.Name, u.EffectiveKind().Label())
}

// einoKindRT 是各渠道的翻译 RoundTripper。
type einoKindRT struct {
	kind   upstream.Kind
	target string // 真实目标 URL（含路径）
	apiKey string
	base   http.RoundTripper
}

func (t *einoKindRT) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	raw, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return nil, err
	}
	var in einoWireReq
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("规则发现请求体不是合法 JSON: %w", err)
	}
	// 上游明确拒绝 temperature 的模型（见 upstream/temperature.go）：翻译时
	// 不再带该参数。Eino 把 temperature 写在模型配置里，改不了，这里去掉。
	if upstream.TemperatureUnsupported(in.Model) {
		in.Temperature = nil
	}
	// Eino 发的 model 字段已是不带上游前缀的模型 ID，翻译时原样透传。
	body, err := json.Marshal(t.translateRequest(&in))
	if err != nil {
		return nil, fmt.Errorf("规则发现请求翻译失败: %w", err)
	}
	out, err := http.NewRequestWithContext(req.Context(), http.MethodPost,
		t.target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	out.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	out.Header.Set("Content-Type", "application/json")
	t.setAuth(out)

	resp, err := base.RoundTrip(out)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		// 各家错误体形状不同（SDK 只认 OpenAI 形状），统一改写成
		// {"error":{"message":...}}，状态码原样保留 —— 重试层按状态码
		// 分类 429/5xx，SDK 的 APIError 也能给出可读 message。
		return rewriteHTTPError(resp), nil
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		// 网关无视 stream:false 直接回 SSE：SDK 没法解析，换成一个明确
		// 的错误响应，别让管理员面对晦涩的 JSON 解析错误。
		resp.Body.Close()
		return errorHTTPResponse(resp.StatusCode,
			"上游无视 stream=false 直接返回了 SSE 流，该渠道不支持非流式请求"), nil
	}
	// 渠道整包响应翻译回 chat/completions，SDK 读到的永远是 OpenAI 形状。
	raw, err = io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	reply, err := t.translateReply(raw)
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(reply))
	resp.ContentLength = int64(len(reply))
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Del("Transfer-Encoding")
	return resp, nil
}

// setAuth 按渠道设置鉴权头（与 upstream.SetAuth 同一约定）。
func (t *einoKindRT) setAuth(req *http.Request) {
	switch t.kind {
	case upstream.KindAnthropic:
		req.Header.Set("x-api-key", t.apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	case upstream.KindGemini:
		req.Header.Set("x-goog-api-key", t.apiKey)
	default:
		req.Header.Set("Authorization", "Bearer "+t.apiKey)
	}
}

func (t *einoKindRT) translateRequest(in *einoWireReq) map[string]any {
	switch t.kind {
	case upstream.KindOpenAIResp:
		return responsesRequest(in)
	case upstream.KindAnthropic:
		return anthropicRequest(in)
	case upstream.KindGemini:
		return geminiRequest(in)
	}
	// 构造方只给支持的 kind，不会走到。
	return nil
}

// rewriteHTTPError 把渠道的错误响应改写成 OpenAI 形状（状态码保留）。
func rewriteHTTPError(resp *http.Response) *http.Response {
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return errorHTTPResponse(resp.StatusCode, kindErrorMessage(raw))
}

// errorHTTPResponse 用 message 组装一个 OpenAI 形状的错误响应。
func errorHTTPResponse(status int, msg string) *http.Response {
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{"message": msg, "type": "upstream_error"},
	})
	resp := &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Header:        http.Header{},
	}
	resp.Header.Set("Content-Type", "application/json")
	return resp
}

// kindErrorMessage 从渠道错误体里抠可读信息。Anthropic / Gemini / Responses
// 的错误体恰好都是 {"error":{"message":...}} 形状；抠不到就原文截断。
func kindErrorMessage(raw []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	if s := strings.TrimSpace(string(raw)); s != "" {
		return core.TruncateRunes(s, 300)
	}
	return "上游返回错误但响应体为空"
}

// ---- Eino 请求的中间形状 ----

// einoWireFunc / einoWireCall / einoWireMsg / einoWireTool / einoWireReq 是
// Eino OpenAI 客户端发出的 chat/completions 请求形状（含工具调用）。
type einoWireFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type einoWireCall struct {
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function einoWireFunc `json:"function"`
}

type einoWireMsg struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []einoWireCall `json:"tool_calls,omitempty"`
}

// einoWireToolFunc 是工具定义的函数描述（name/description/parameters）。
type einoWireToolFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type einoWireTool struct {
	Type     string           `json:"type"`
	Function einoWireToolFunc `json:"function"`
}

type einoWireReq struct {
	Model       string         `json:"model"`
	Messages    []einoWireMsg  `json:"messages"`
	Tools       []einoWireTool `json:"tools"`
	Temperature *float32       `json:"temperature"`
	MaxTokens   int            `json:"max_tokens"`
}

// jsonArgsObject 把工具参数串解析成 JSON 对象（Anthropic tool_use.input 与
// Gemini functionCall.args 都要求对象）；空串或畸形时给空对象，别让请求 400。
func jsonArgsObject(args string) map[string]any {
	m := map[string]any{}
	if strings.TrimSpace(args) != "" {
		_ = json.Unmarshal([]byte(args), &m)
	}
	return m
}

// ---- 请求翻译 ----

// responsesRequest 把 chat/completions 请求翻译成 Responses API 请求：
// system 走顶层 instructions；assistant 的 tool_calls 摊成 input 里的
// function_call 项；role:"tool" 消息变成 function_call_output；工具定义
// 去掉 function 嵌套（Responses 的工具是扁平形状）。
func responsesRequest(in *einoWireReq) map[string]any {
	var system []string
	input := make([]map[string]any, 0, len(in.Messages))
	for _, m := range in.Messages {
		switch m.Role {
		case "system", "developer":
			if m.Content != "" {
				system = append(system, m.Content)
			}
		case "tool":
			input = append(input, map[string]any{
				"type": "function_call_output", "call_id": m.ToolCallID,
				"output": m.Content,
			})
		case "assistant":
			if m.Content != "" {
				input = append(input, map[string]any{"role": "assistant", "content": m.Content})
			}
			for _, tc := range m.ToolCalls {
				input = append(input, map[string]any{
					"type": "function_call", "call_id": tc.ID,
					"name": tc.Function.Name, "arguments": tc.Function.Arguments,
				})
			}
		default:
			input = append(input, map[string]any{"role": "user", "content": m.Content})
		}
	}
	body := map[string]any{"model": in.Model, "input": input, "stream": false}
	if len(system) > 0 {
		body["instructions"] = strings.Join(system, "\n\n")
	}
	if in.Temperature != nil {
		body["temperature"] = *in.Temperature
	}
	if in.MaxTokens > 0 {
		body["max_output_tokens"] = in.MaxTokens
	}
	if len(in.Tools) > 0 {
		tools := make([]map[string]any, 0, len(in.Tools))
		for _, tl := range in.Tools {
			t := map[string]any{"type": "function",
				"name": tl.Function.Name, "description": tl.Function.Description}
			if len(tl.Function.Parameters) > 0 {
				t["parameters"] = tl.Function.Parameters
			}
			tools = append(tools, t)
		}
		body["tools"] = tools
	}
	return body
}

// ruleAgentAnthropicMaxTokens 是 Anthropic 渠道未显式传 max_tokens 时的
// 默认值：Messages API 必填；一轮的输出是一条消息（文本 + 工具调用），
// 8192 给推理型模型留足空间。
const ruleAgentAnthropicMaxTokens = 8192

// anthropicRequest 把 chat/completions 请求翻译成 Messages API 请求。
// Anthropic 要求 tool_result 挂在 tool_use 之后紧邻的 user 消息里，Eino 的
// 连续 role:"tool" 消息在这里合并成一条 user 消息的多个 tool_result 块。
func anthropicRequest(in *einoWireReq) map[string]any {
	var system []string
	msgs := make([]map[string]any, 0, len(in.Messages))
	var pending []map[string]any // 待合并的 tool_result 块
	flush := func() {
		if len(pending) > 0 {
			msgs = append(msgs, map[string]any{"role": "user", "content": pending})
			pending = nil
		}
	}
	for _, m := range in.Messages {
		switch m.Role {
		case "system", "developer":
			if m.Content != "" {
				system = append(system, m.Content)
			}
		case "tool":
			pending = append(pending, map[string]any{
				"type": "tool_result", "tool_use_id": m.ToolCallID, "content": m.Content,
			})
		case "assistant":
			flush()
			blocks := make([]map[string]any, 0, 1+len(m.ToolCalls))
			if m.Content != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, map[string]any{
					"type": "tool_use", "id": tc.ID, "name": tc.Function.Name,
					"input": jsonArgsObject(tc.Function.Arguments),
				})
			}
			if len(blocks) == 0 {
				// Anthropic 拒绝空 content：塞一个空格文本块占位。
				blocks = append(blocks, map[string]any{"type": "text", "text": " "})
			}
			msgs = append(msgs, map[string]any{"role": "assistant", "content": blocks})
		default:
			flush()
			msgs = append(msgs, map[string]any{"role": "user", "content": m.Content})
		}
	}
	flush()

	maxTokens := in.MaxTokens
	if maxTokens <= 0 {
		maxTokens = ruleAgentAnthropicMaxTokens
	}
	body := map[string]any{"model": in.Model, "max_tokens": maxTokens, "stream": false}
	if len(msgs) > 0 {
		body["messages"] = msgs
	}
	if len(system) > 0 {
		body["system"] = strings.Join(system, "\n\n")
	}
	if in.Temperature != nil {
		body["temperature"] = *in.Temperature
	}
	if len(in.Tools) > 0 {
		tools := make([]map[string]any, 0, len(in.Tools))
		for _, tl := range in.Tools {
			t := map[string]any{"name": tl.Function.Name, "description": tl.Function.Description}
			if len(tl.Function.Parameters) > 0 {
				t["input_schema"] = tl.Function.Parameters
			}
			tools = append(tools, t)
		}
		body["tools"] = tools
	}
	return body
}

// geminiRequest 把 chat/completions 请求翻译成 generateContent 请求：
// assistant → role:"model"；tool 结果 → functionResponse 块（Gemini 要求
// 带 function 名，从我们合成 tool_call id 时编码进去的名字还原）。
func geminiRequest(in *einoWireReq) map[string]any {
	var system []string
	contents := make([]map[string]any, 0, len(in.Messages))
	for _, m := range in.Messages {
		switch m.Role {
		case "system", "developer":
			if m.Content != "" {
				system = append(system, m.Content)
			}
		case "tool":
			contents = append(contents, map[string]any{
				"role": "user",
				"parts": []map[string]any{{
					"functionResponse": map[string]any{
						"name":     geminiFuncNameOfCallID(m.ToolCallID),
						"response": map[string]any{"result": m.Content},
					},
				}},
			})
		case "assistant":
			parts := make([]map[string]any, 0, 1+len(m.ToolCalls))
			if m.Content != "" {
				parts = append(parts, map[string]any{"text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				parts = append(parts, map[string]any{
					"functionCall": map[string]any{
						"name": tc.Function.Name, "args": jsonArgsObject(tc.Function.Arguments),
					},
				})
			}
			if len(parts) > 0 {
				contents = append(contents, map[string]any{"role": "model", "parts": parts})
			}
		default:
			contents = append(contents, map[string]any{
				"role": "user", "parts": []map[string]any{{"text": m.Content}},
			})
		}
	}
	body := map[string]any{"contents": contents}
	if len(system) > 0 {
		body["systemInstruction"] = map[string]any{
			"parts": []map[string]any{{"text": strings.Join(system, "\n\n")}}}
	}
	if in.Temperature != nil {
		body["generationConfig"] = map[string]any{"temperature": *in.Temperature}
	}
	if len(in.Tools) > 0 {
		decls := make([]map[string]any, 0, len(in.Tools))
		for _, tl := range in.Tools {
			d := map[string]any{"name": tl.Function.Name, "description": tl.Function.Description}
			if len(tl.Function.Parameters) > 0 {
				d["parameters"] = geminiSchema(tl.Function.Parameters)
			}
			decls = append(decls, d)
		}
		body["tools"] = []map[string]any{{"functionDeclarations": decls}}
	}
	return body
}

// geminiCallIDSuffix 匹配合成 tool_call id 末尾的序号（"_3"）。
var geminiCallIDSuffix = regexp.MustCompile(`_[0-9]+$`)

// geminiFuncNameOfCallID 从合成的 tool_call id 还原 function 名。Gemini 的
// functionCall/functionResponse 都没有 id，回复翻译时把名字编码进 id
// （call_<name>_<序号>），这里再解出来 —— 工具名不含结尾数字，够用。
func geminiFuncNameOfCallID(id string) string {
	return strings.TrimPrefix(geminiCallIDSuffix.ReplaceAllString(id, ""), "call_")
}

// geminiSchema 把 OpenAI JSON Schema 收紧成 Gemini functionDeclarations 认的
// OpenAPI 子集：剔除 Gemini 会整包拒绝的字段（additionalProperties、$schema、
// format 等），可空类型数组（["string","null"]）摊平成 type + nullable。
func geminiSchema(raw json.RawMessage) map[string]any {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return map[string]any{"type": "object"}
	}
	if out, ok := geminiSchemaValue(v).(map[string]any); ok {
		return out
	}
	return map[string]any{"type": "object"}
}

func geminiSchemaValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			switch k {
			case "additionalProperties", "$schema", "$defs", "definitions",
				"default", "format", "examples", "oneOf", "anyOf", "allOf":
				continue
			case "type":
				switch tv := val.(type) {
				case string:
					out["type"] = tv
				case []any:
					for _, item := range tv {
						if s, ok := item.(string); ok && s != "null" {
							out["type"] = s
							break
						}
					}
					if _, ok := out["type"]; ok {
						out["nullable"] = true
					}
				}
			case "items":
				out["items"] = geminiSchemaValue(val)
			case "properties":
				props, ok := val.(map[string]any)
				if !ok {
					continue
				}
				np := make(map[string]any, len(props))
				for pk, pv := range props {
					np[pk] = geminiSchemaValue(pv)
				}
				out["properties"] = np
			default:
				out[k] = val
			}
		}
		return out
	default:
		return v
	}
}

// ---- 响应翻译 ----

// einoUsage 组装规则发现用的极简 usage（不走计费链路，无需 details）。
func einoUsage(prompt, completion int) map[string]any {
	return map[string]any{"prompt_tokens": prompt, "completion_tokens": completion}
}

// einoChatReply 把翻译结果组装成 SDK 认的 chat/completions 响应体。
func einoChatReply(content, reasoning string, calls []einoWireCall,
	finish string, usage map[string]any) []byte {

	msg := map[string]any{"role": "assistant", "content": content}
	if reasoning != "" {
		msg["reasoning_content"] = reasoning
	}
	if len(calls) > 0 {
		msg["tool_calls"] = calls
	}
	if finish == "" {
		finish = "stop"
	}
	out := map[string]any{
		"choices": []map[string]any{{
			"index": 0, "message": msg, "finish_reason": finish,
		}},
	}
	if usage != nil {
		out["usage"] = usage
	}
	b, _ := json.Marshal(out)
	return b
}

// translateReply 把渠道的整包响应翻译回 chat/completions。
func (t *einoKindRT) translateReply(raw []byte) ([]byte, error) {
	switch t.kind {
	case upstream.KindOpenAIResp:
		return responsesReply(raw)
	case upstream.KindAnthropic:
		return anthropicReply(raw)
	case upstream.KindGemini:
		return geminiReply(raw)
	}
	return nil, fmt.Errorf("渠道 %s 不需要响应翻译", t.kind)
}

// responsesReply 把 Responses API 的整包响应翻译回 chat/completions：
// output 里的 message / reasoning / function_call 项分别对应 content、
// reasoning_content 与 tool_calls。
func responsesReply(raw []byte) ([]byte, error) {
	var r struct {
		Status string `json:"status"`
		Output []struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Summary []struct {
				Text string `json:"text"`
			} `json:"summary"`
		} `json:"output"`
		Usage struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			InputTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("OpenAI Responses 响应无法解析: %w", err)
	}
	if r.Error != nil && r.Error.Message != "" {
		return nil, fmt.Errorf("上游返回错误: %s", r.Error.Message)
	}
	var text, reasoning strings.Builder
	var calls []einoWireCall
	for _, item := range r.Output {
		switch item.Type {
		case "function_call":
			id := item.CallID
			if id == "" {
				id = item.ID
			}
			calls = append(calls, einoWireCall{ID: id, Type: "function",
				Function: einoWireFunc{Name: item.Name, Arguments: item.Arguments}})
		case "reasoning":
			for _, s := range item.Summary {
				reasoning.WriteString(s.Text)
			}
		default: // message
			for _, c := range item.Content {
				if c.Type == "output_text" || c.Text != "" {
					text.WriteString(c.Text)
				}
			}
		}
	}
	finish := "stop"
	switch {
	case len(calls) > 0:
		finish = "tool_calls"
	case r.IncompleteDetails != nil && r.IncompleteDetails.Reason == "max_output_tokens":
		finish = "length"
	}
	return einoChatReply(text.String(), reasoning.String(), calls, finish,
		einoUsage(r.Usage.InputTokens, r.Usage.OutputTokens)), nil
}

// anthropicReply 把 Messages API 的整包响应翻译回 chat/completions：
// text/thinking/tool_use 块分别对应 content、reasoning_content 与 tool_calls。
func anthropicReply(raw []byte) ([]byte, error) {
	var r struct {
		Content []struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			Thinking string          `json:"thinking"`
			ID       string          `json:"id"`
			Name     string          `json:"name"`
			Input    json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("Anthropic 响应无法解析: %w", err)
	}
	if r.Error != nil && r.Error.Message != "" {
		return nil, fmt.Errorf("上游返回错误: %s", r.Error.Message)
	}
	var text, reasoning strings.Builder
	var calls []einoWireCall
	for _, c := range r.Content {
		switch c.Type {
		case "tool_use":
			args := strings.TrimSpace(string(c.Input))
			if args == "" || !json.Valid([]byte(args)) {
				args = "{}"
			}
			calls = append(calls, einoWireCall{ID: c.ID, Type: "function",
				Function: einoWireFunc{Name: c.Name, Arguments: args}})
		case "thinking":
			reasoning.WriteString(c.Thinking)
		default: // text（type 为空时同样按 text 处理）
			text.WriteString(c.Text)
		}
	}
	finish := "stop"
	switch {
	case len(calls) > 0:
		finish = "tool_calls"
	case r.StopReason == "max_tokens":
		finish = "length"
	}
	prompt := r.Usage.InputTokens + r.Usage.CacheReadInputTokens +
		r.Usage.CacheCreationInputTokens
	return einoChatReply(text.String(), reasoning.String(), calls, finish,
		einoUsage(prompt, r.Usage.OutputTokens)), nil
}

// geminiReply 把 generateContent 的整包响应翻译回 chat/completions。
// Gemini 的 functionCall 没有 id，这里合成 call_<name>_<序号>：
// 下一轮请求翻译时靠它还原 function 名（见 geminiFuncNameOfCallID）。
func geminiReply(raw []byte) ([]byte, error) {
	var r struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text         string `json:"text"`
					Thought      bool   `json:"thought"`
					FunctionCall *struct {
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
		PromptFeedback *struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("Gemini 响应无法解析: %w", err)
	}
	if r.Error != nil && r.Error.Message != "" {
		return nil, fmt.Errorf("上游返回错误: %s", r.Error.Message)
	}
	var text, reasoning strings.Builder
	var calls []einoWireCall
	for _, c := range r.Candidates {
		for _, p := range c.Content.Parts {
			switch {
			case p.FunctionCall != nil:
				args := strings.TrimSpace(string(p.FunctionCall.Args))
				if args == "" || !json.Valid([]byte(args)) {
					args = "{}"
				}
				calls = append(calls, einoWireCall{
					ID:       fmt.Sprintf("call_%s_%d", p.FunctionCall.Name, len(calls)),
					Type:     "function",
					Function: einoWireFunc{Name: p.FunctionCall.Name, Arguments: args},
				})
			case p.Thought:
				reasoning.WriteString(p.Text)
			default:
				text.WriteString(p.Text)
			}
		}
	}
	if text.Len() == 0 && len(calls) == 0 && r.PromptFeedback != nil &&
		r.PromptFeedback.BlockReason != "" {
		return nil, fmt.Errorf("Gemini 拒答: %s", r.PromptFeedback.BlockReason)
	}
	finish := "stop"
	switch {
	case len(calls) > 0:
		finish = "tool_calls"
	case r.Candidates != nil && r.Candidates[0].FinishReason == "MAX_TOKENS":
		finish = "length"
	}
	return einoChatReply(text.String(), reasoning.String(), calls, finish,
		einoUsage(r.UsageMetadata.PromptTokenCount, r.UsageMetadata.CandidatesTokenCount)), nil
}
