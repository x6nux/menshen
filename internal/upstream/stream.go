package upstream

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// 本文件是流式响应适配层：所有对话渠道默认走 SSE，各家的流式事件在这里
// 被转成 OpenAI chat/completions 的 delta 形状，antiad 的读流器（首字看门狗、
// 流空闲超时、usage 汇总）对任何渠道都一视同仁。
//
// 上游忽略 stream、直接回整包 JSON 时，走 NormalizeResponse 的非流式翻译，
// 两条路产出的都是同一形状。

// StreamAdapter 把渠道的 SSE 流转成 OpenAI 形状；OpenAI Completions 渠道
// 原样返回。其余渠道用 io.Pipe 边读边转。
//
// 调用方读完后必须 Close 返回的 Reader：提前放弃读取时（首字看门狗取消、
// 解析报错返回），关闭读端会让转换 goroutine 从写阻塞中退出，不泄漏。
func (u *Upstream) StreamAdapter(r io.ReadCloser) io.ReadCloser {
	if u.EffectiveKind() == KindOpenAI {
		return r
	}
	pr, pw := io.Pipe()
	kind := u.EffectiveKind()
	go func() {
		pw.CloseWithError(translateStream(kind, r, pw))
	}()
	return pr
}

// streamState 汇总流式过程中拿到的用量；usageSent 防止重复下发。
type streamState struct {
	prompt, completion, cached, cacheWrite int
	usageSent                              bool
}

func translateStream(kind Kind, r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	st := &streamState{}
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue // event: / id: / 空行 / 心跳注释
		}
		data = strings.TrimSpace(data)
		if data == "" || data == "[DONE]" {
			continue
		}
		var err error
		switch kind {
		case KindOpenAIResp:
			err = feedOpenAIResponses([]byte(data), st, w)
		case KindAnthropic:
			err = feedAnthropic([]byte(data), st, w)
		case KindGemini:
			err = feedGemini([]byte(data), st, w)
		case KindCloudflare:
			err = feedCloudflare([]byte(data), st, w)
		}
		if err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	// 上游没在流里报用量时（个别网关如此）即计费归零，判定照常。
	if err := st.flushUsage(w); err != nil {
		return err
	}
	_, err := io.WriteString(w, "data: [DONE]\n\n")
	return err
}

// writeDelta 输出一个 content / reasoning 增量块。
func writeDelta(w io.Writer, content, reasoning string) error {
	if content == "" && reasoning == "" {
		return nil
	}
	delta := map[string]string{}
	if content != "" {
		delta["content"] = content
	}
	if reasoning != "" {
		delta["reasoning_content"] = reasoning
	}
	b, err := json.Marshal(map[string]any{"choices": []map[string]any{{"delta": delta}}})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", b)
	return err
}

// writeStreamError 把上游的错误事件转成 readChatStream 认的 error 块。
func writeStreamError(w io.Writer, msg string) error {
	b, err := json.Marshal(map[string]any{"error": map[string]string{"message": msg}})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", b)
	return err
}

func (st *streamState) setUsage(prompt, completion, cached, cacheWrite int) {
	st.prompt, st.completion = prompt, completion
	st.cached, st.cacheWrite = cached, cacheWrite
}

// flushUsage 输出最终的 usage 块（readChatStream 收在流末把它拼回整包）。
func (st *streamState) flushUsage(w io.Writer) error {
	if st.usageSent || (st.prompt == 0 && st.completion == 0) {
		return nil
	}
	st.usageSent = true
	b, err := json.Marshal(map[string]any{
		"choices": []any{},
		"usage":   openAIUsage(st.prompt, st.completion, st.cached, st.cacheWrite),
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", b)
	return err
}

// ---- Anthropic Messages ----

func feedAnthropic(data []byte, st *streamState, w io.Writer) error {
	var ev struct {
		Type    string `json:"type"`
		Message *struct {
			Usage struct {
				InputTokens              int `json:"input_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Delta struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"delta"`
		Usage struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		return nil // 个别网关夹带非 JSON 行，跳过即可
	}
	if ev.Error != nil && ev.Error.Message != "" {
		return writeStreamError(w, ev.Error.Message)
	}
	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			u := ev.Message.Usage
			// input_tokens 不含缓存；拼成 OpenAI 口径的含缓存总量，
			// ExtractUsage 会按 details 再减回去。
			st.setUsage(u.InputTokens+u.CacheReadInputTokens+u.CacheCreationInputTokens,
				0, u.CacheReadInputTokens, u.CacheCreationInputTokens)
		}
	case "content_block_delta":
		switch ev.Delta.Type {
		case "thinking_delta":
			return writeDelta(w, "", ev.Delta.Thinking)
		case "text_delta", "":
			return writeDelta(w, ev.Delta.Text, "")
		}
	case "message_delta":
		if ev.Usage.OutputTokens > 0 {
			st.completion = ev.Usage.OutputTokens
			return st.flushUsage(w)
		}
	case "error":
		return writeStreamError(w, "上游返回 error 事件")
	}
	return nil
}

// ---- OpenAI Responses ----

func feedOpenAIResponses(data []byte, st *streamState, w io.Writer) error {
	var ev struct {
		Type    string `json:"type"`
		Delta   string `json:"delta"`
		Message string `json:"message"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
		Response *struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Usage struct {
				InputTokens        int `json:"input_tokens"`
				OutputTokens       int `json:"output_tokens"`
				InputTokensDetails struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"input_tokens_details"`
			} `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		return nil
	}
	switch ev.Type {
	case "response.output_text.delta":
		return writeDelta(w, ev.Delta, "")
	case "response.reasoning_summary_text.delta":
		return writeDelta(w, "", ev.Delta)
	case "response.completed":
		if ev.Response != nil {
			u := ev.Response.Usage
			st.setUsage(u.InputTokens, u.OutputTokens, u.InputTokensDetails.CachedTokens, 0)
			return st.flushUsage(w)
		}
	case "response.failed":
		msg := ev.Message
		if msg == "" && ev.Response != nil && ev.Response.Error != nil {
			msg = ev.Response.Error.Message
		}
		if msg == "" {
			msg = "response.failed"
		}
		return writeStreamError(w, msg)
	case "error":
		if ev.Error != nil && ev.Error.Message != "" {
			return writeStreamError(w, ev.Error.Message)
		}
		if ev.Message != "" {
			return writeStreamError(w, ev.Message)
		}
		return writeStreamError(w, "上游返回 error 事件")
	}
	return nil
}

// ---- Gemini generateContent ----

func feedGemini(data []byte, st *streamState, w io.Writer) error {
	var ev struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata *struct {
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
	if err := json.Unmarshal(data, &ev); err != nil {
		return nil
	}
	if ev.Error != nil && ev.Error.Message != "" {
		return writeStreamError(w, ev.Error.Message)
	}
	if ev.PromptFeedback != nil && ev.PromptFeedback.BlockReason != "" {
		return writeStreamError(w, "Gemini 拒答: "+ev.PromptFeedback.BlockReason)
	}
	for _, c := range ev.Candidates {
		for _, p := range c.Content.Parts {
			if p.Thought {
				if err := writeDelta(w, "", p.Text); err != nil {
					return err
				}
				continue
			}
			if err := writeDelta(w, p.Text, ""); err != nil {
				return err
			}
		}
	}
	if ev.UsageMetadata != nil {
		st.setUsage(ev.UsageMetadata.PromptTokenCount,
			ev.UsageMetadata.CandidatesTokenCount,
			ev.UsageMetadata.CachedContentTokenCount, 0)
		return st.flushUsage(w)
	}
	return nil
}

// ---- Cloudflare Workers AI ----

func feedCloudflare(data []byte, st *streamState, w io.Writer) error {
	var ev struct {
		Response string `json:"response"`
		Usage    *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			InputTokens      int `json:"input_tokens"`
			OutputTokens     int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		return nil
	}
	if ev.Response != "" {
		if err := writeDelta(w, ev.Response, ""); err != nil {
			return err
		}
	}
	if ev.Usage != nil {
		prompt, completion := ev.Usage.PromptTokens, ev.Usage.CompletionTokens
		if prompt == 0 {
			prompt = ev.Usage.InputTokens
		}
		if completion == 0 {
			completion = ev.Usage.OutputTokens
		}
		st.setUsage(prompt, completion, 0, 0)
		return st.flushUsage(w)
	}
	return nil
}
