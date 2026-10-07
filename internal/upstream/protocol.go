package upstream

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// 本文件是渠道类型与判定链路之间的协议适配层：
//
//	请求侧  BuildBody / SetAuth —— 把逻辑载荷改写成目标渠道的形状；
//	响应侧  NormalizeResponse —— 非流式响应（上游忽略 stream 时）翻译回
//	        OpenAI chat/completions 或 SystemOne 答案形状。
//
// 流式响应由 stream.go 的 StreamAdapter 边读边转；两条路产出同一形状，
// 判定解析、计费都不用认识各家协议。

// SetAuth 按渠道类型设置鉴权头。绝大多数走 Bearer；Anthropic 用
// x-api-key + 版本头，Gemini 用 x-goog-api-key。
func (u *Upstream) SetAuth(req *http.Request) {
	switch u.EffectiveKind() {
	case KindAnthropic:
		req.Header.Set("x-api-key", u.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	case KindGemini:
		req.Header.Set("x-goog-api-key", u.APIKey)
	default:
		req.Header.Set("Authorization", "Bearer "+u.APIKey)
	}
}

// BuildBody 把逻辑载荷改写成目标渠道的请求体。
//
// payload 是判定链路的逻辑形状：
//   - chat：{messages, temperature, stream, ...}，可能带 user / session_id
//     这类给 OpenAI 兼容网关的私有字段；
//   - systemone：{state, questions}。
//
// OpenAI Completions 渠道原样透传（只补 model）；其余类型只挑自己认识的
// 字段重新组装，避免把 stream_options 这类参数发给不认它的上游。
func (u *Upstream) BuildBody(ep Endpoint, model string, payload map[string]any) map[string]any {
	_, id := SplitModelName(model)
	switch u.EffectiveKind() {
	case KindOpenAIResp:
		return bodyOpenAIResponses(id, payload)
	case KindAnthropic:
		return bodyAnthropic(id, payload)
	case KindGemini:
		return bodyGemini(id, payload)
	case KindCloudflare:
		if ep == EPSystemOne {
			b := clonePayload(payload)
			// Clef 的 model 只认 "clef" / "clef-flash"（模型 ID 最后一段），
			// URL 里才用完整 @cf/cloudflare/clef。
			b["model"] = lastSegment(id)
			return b
		}
		return bodyCloudflareChat(payload)
	}
	b := clonePayload(payload)
	b["model"] = id
	// 统一默认流式：chat 载荷没写 stream 时按流式发（上游忽略、回整包
	// JSON 时读侧也能回落）。systemone 是 TypeSafe 原生形态，多带字段
	// 会 400，不能改。
	if ep == EPChat {
		if _, ok := b["stream"]; !ok {
			b["stream"] = true
		}
	}
	return b
}

// NormalizeResponse 把渠道响应翻译成调用方认的形状：chat 端点是 OpenAI
// chat/completions 形状，systemone 端点是 SystemOne 答案形状。
// OpenAI Completions 渠道原样返回。
func (u *Upstream) NormalizeResponse(ep Endpoint, raw []byte) ([]byte, error) {
	switch u.EffectiveKind() {
	case KindCloudflare:
		result, err := UnwrapCF(raw)
		if err != nil {
			return nil, err
		}
		if ep == EPSystemOne {
			return result, nil
		}
		return chatFromCloudflare(result)
	case KindOpenAIResp:
		return chatFromOpenAIResponses(raw)
	case KindAnthropic:
		return chatFromAnthropic(raw)
	case KindGemini:
		return chatFromGemini(raw)
	}
	return raw, nil
}

// ---- 请求体组装 ----

func clonePayload(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func lastSegment(id string) string {
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		return id[i+1:]
	}
	return id
}

type chatMsg struct{ Role, Content string }

// messagesOf 从逻辑载荷里取对话消息。判定链路用 []map[string]string，
// 面板连通性测试与反序列化后的形状可能给 []any，两种都认。
func messagesOf(payload map[string]any) []chatMsg {
	var out []chatMsg
	switch msgs := payload["messages"].(type) {
	case []map[string]string:
		for _, m := range msgs {
			out = append(out, chatMsg{Role: m["role"], Content: m["content"]})
		}
	case []any:
		for _, mv := range msgs {
			m, ok := mv.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, chatMsg{Role: anyString(m["role"]), Content: anyString(m["content"])})
		}
	}
	return out
}

func anyString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(b)
	}
}

func maxTokensOf(payload map[string]any) int {
	for _, k := range []string{"max_tokens", "max_completion_tokens"} {
		switch v := payload[k].(type) {
		case float64:
			return int(v)
		case int:
			return v
		case int64:
			return int(v)
		case json.Number:
			if n, err := v.Int64(); err == nil {
				return int(n)
			}
		}
	}
	return 0
}

// anthropicDefaultMaxTokens：Messages API 的 max_tokens 是必填项；判定
// 链路没传时给一个足够放完整 JSON 判决的值。
const anthropicDefaultMaxTokens = 2048

func bodyOpenAIResponses(model string, payload map[string]any) map[string]any {
	msgs := messagesOf(payload)
	input := make([]map[string]string, 0, len(msgs))
	var system []string
	for _, m := range msgs {
		switch m.Role {
		case "system", "developer":
			// Responses API 的 system 提示走顶层 instructions 更稳。
			system = append(system, m.Content)
		default:
			role := m.Role
			if role == "" {
				role = "user"
			}
			input = append(input, map[string]string{"role": role, "content": m.Content})
		}
	}
	body := map[string]any{"model": model, "input": input, "stream": true}
	if len(system) > 0 {
		body["instructions"] = strings.Join(system, "\n\n")
	}
	if t, ok := payload["temperature"]; ok {
		body["temperature"] = t
	}
	if mt := maxTokensOf(payload); mt > 0 {
		body["max_output_tokens"] = mt
	}
	return body
}

func bodyAnthropic(model string, payload map[string]any) map[string]any {
	msgs := messagesOf(payload)
	out := make([]map[string]string, 0, len(msgs))
	var system []string
	for _, m := range msgs {
		switch m.Role {
		case "system", "developer":
			system = append(system, m.Content)
		case "assistant":
			out = append(out, map[string]string{"role": "assistant", "content": m.Content})
		default:
			out = append(out, map[string]string{"role": "user", "content": m.Content})
		}
	}
	maxTokens := anthropicDefaultMaxTokens
	if mt := maxTokensOf(payload); mt > 0 {
		maxTokens = mt
	}
	body := map[string]any{"model": model, "max_tokens": maxTokens, "stream": true}
	if len(out) > 0 {
		body["messages"] = out
	}
	if len(system) > 0 {
		body["system"] = strings.Join(system, "\n\n")
	}
	if t, ok := payload["temperature"]; ok {
		body["temperature"] = t
	}
	return body
}

func bodyGemini(model string, payload map[string]any) map[string]any {
	msgs := messagesOf(payload)
	contents := make([]map[string]any, 0, len(msgs))
	var system []string
	for _, m := range msgs {
		switch m.Role {
		case "system", "developer":
			system = append(system, m.Content)
		case "assistant":
			contents = append(contents, geminiContent("model", m.Content))
		default:
			contents = append(contents, geminiContent("user", m.Content))
		}
	}
	body := map[string]any{"contents": contents}
	if len(system) > 0 {
		body["systemInstruction"] = geminiContent("", strings.Join(system, "\n\n"))
	}
	gen := map[string]any{}
	if t, ok := payload["temperature"]; ok {
		gen["temperature"] = t
	}
	if mt := maxTokensOf(payload); mt > 0 {
		gen["maxOutputTokens"] = mt
	}
	if len(gen) > 0 {
		body["generationConfig"] = gen
	}
	return body
}

func geminiContent(role, text string) map[string]any {
	c := map[string]any{"parts": []map[string]string{{"text": text}}}
	if role != "" {
		c["role"] = role
	}
	return c
}

func bodyCloudflareChat(payload map[string]any) map[string]any {
	body := map[string]any{"stream": true}
	if msgs, ok := payload["messages"]; ok {
		body["messages"] = msgs
	}
	if t, ok := payload["temperature"]; ok {
		body["temperature"] = t
	}
	if mt := maxTokensOf(payload); mt > 0 {
		body["max_tokens"] = mt
	}
	// 不带 stream_options / user / session_id：Workers AI 的文本模型不认
	// 这些字段；流式帧由 StreamAdapter 翻译。
	return body
}

// ---- 响应翻译 ----

// chatEnvelope 组装 chat/completions 形状。判定的复判解析与
// billing.ExtractUsage(EPChat) 都按这个形状读，各渠道不用再单开分支。
func chatEnvelope(content, reasoning string, usage map[string]any) ([]byte, error) {
	msg := map[string]string{"content": content}
	if reasoning != "" {
		msg["reasoning_content"] = reasoning
	}
	out := map[string]any{"choices": []map[string]any{{"message": msg}}}
	if usage != nil {
		out["usage"] = usage
	}
	return json.Marshal(out)
}

// openAIUsage 组装 OpenAI 口径的 usage。prompt 是含缓存的输入总量，
// cached / cacheWrite 分列 details —— 与 ExtractUsage 的减法约定一致。
func openAIUsage(prompt, completion, cached, cacheWrite int) map[string]any {
	u := map[string]any{"prompt_tokens": prompt, "completion_tokens": completion}
	details := map[string]any{}
	if cached > 0 {
		details["cached_tokens"] = cached
	}
	if cacheWrite > 0 {
		details["cache_write_tokens"] = cacheWrite
	}
	if len(details) > 0 {
		u["prompt_tokens_details"] = details
	}
	return u
}

func chatFromCloudflare(result []byte) ([]byte, error) {
	var r struct {
		Response string `json:"response"`
		Usage    struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			InputTokens      int `json:"input_tokens"`
			OutputTokens     int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return nil, fmt.Errorf("Cloudflare chat 响应无法解析: %w", err)
	}
	prompt, completion := r.Usage.PromptTokens, r.Usage.CompletionTokens
	if prompt == 0 {
		prompt = r.Usage.InputTokens
	}
	if completion == 0 {
		completion = r.Usage.OutputTokens
	}
	return chatEnvelope(r.Response, "", openAIUsage(prompt, completion, 0, 0))
}

func chatFromOpenAIResponses(raw []byte) ([]byte, error) {
	var r struct {
		OutputText string `json:"output_text"`
		Output     []struct {
			Type    string `json:"type"`
			Summary []struct {
				Text string `json:"text"`
			} `json:"summary"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			InputTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
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
	text.WriteString(r.OutputText)
	for _, item := range r.Output {
		for _, s := range item.Summary {
			reasoning.WriteString(s.Text)
		}
		for _, c := range item.Content {
			if c.Type == "output_text" || c.Text != "" {
				text.WriteString(c.Text)
			}
		}
	}
	return chatEnvelope(text.String(), reasoning.String(),
		openAIUsage(r.Usage.InputTokens, r.Usage.OutputTokens,
			r.Usage.InputTokensDetails.CachedTokens, 0))
}

func chatFromAnthropic(raw []byte) ([]byte, error) {
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
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
	var text strings.Builder
	for _, c := range r.Content {
		if c.Type == "" || c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	// Anthropic 的 input_tokens 不含缓存命中/写入，OpenAI 口径的
	// prompt_tokens 是含缓存总量，这里补齐后再由 ExtractUsage 减回去。
	prompt := r.Usage.InputTokens + r.Usage.CacheReadInputTokens + r.Usage.CacheCreationInputTokens
	return chatEnvelope(text.String(), "",
		openAIUsage(prompt, r.Usage.OutputTokens,
			r.Usage.CacheReadInputTokens, r.Usage.CacheCreationInputTokens))
}

func chatFromGemini(raw []byte) ([]byte, error) {
	var r struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount        int `json:"promptTokenCount"`
			CandidatesTokenCount    int `json:"candidatesTokenCount"`
			CachedContentTokenCount int `json:"cachedContentTokenCount"`
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
	var text strings.Builder
	for _, c := range r.Candidates {
		for _, p := range c.Content.Parts {
			text.WriteString(p.Text)
		}
	}
	if text.Len() == 0 && r.PromptFeedback != nil && r.PromptFeedback.BlockReason != "" {
		return nil, fmt.Errorf("Gemini 拒答: %s", r.PromptFeedback.BlockReason)
	}
	return chatEnvelope(text.String(), "",
		openAIUsage(r.UsageMetadata.PromptTokenCount, r.UsageMetadata.CandidatesTokenCount,
			r.UsageMetadata.CachedContentTokenCount, 0))
}

// ---- Cloudflare 信封 ----

// cfEnvelope 是 Cloudflare API v4 的统一响应信封。
type cfEnvelope struct {
	Result  json.RawMessage `json:"result"`
	Success *bool           `json:"success"`
	Errors  []cfError       `json:"errors"`
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// UnwrapCF 解 Cloudflare API 的 {result,success,errors} 信封：
// 成功返回其中的 result，失败返回带 errors[0].message 的错误。
//
// success 字段用指针：兼容那些只回 {result:...} 的极简网关。
func UnwrapCF(raw []byte) (json.RawMessage, error) {
	var env cfEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("不是 Cloudflare 信封: %w", err)
	}
	if env.Success != nil && !*env.Success {
		return nil, fmt.Errorf("Cloudflare 返回失败: %s", cfErrText(env.Errors))
	}
	if len(env.Result) == 0 || string(env.Result) == "null" {
		return nil, fmt.Errorf("Cloudflare 信封里没有 result")
	}
	return env.Result, nil
}

// CFErrorMessage 从 Cloudflare 错误响应里取可读原因；取不到返回空串。
func CFErrorMessage(raw []byte) string {
	var env cfEnvelope
	if json.Unmarshal(raw, &env) != nil {
		return ""
	}
	return cfErrText(env.Errors)
}

func cfErrText(errs []cfError) string {
	for _, e := range errs {
		if e.Message == "" {
			continue
		}
		if e.Code != 0 {
			return fmt.Sprintf("[%d] %s", e.Code, e.Message)
		}
		return e.Message
	}
	return "未知错误"
}
