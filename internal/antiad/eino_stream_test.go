package antiad

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestReassembleOpenAIStream：SSE 分片（含分段 tool_call 与流末 usage）要能
// 重建成非流式响应体，tool_calls 按 index 合并、参数拼接完整。
func TestReassembleOpenAIStream(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant","content":""}}]}`,
		`data: {"choices":[{"delta":{"content":"你好"}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"find","arguments":"{\"q\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`data: {"usage":{"prompt_tokens":3,"completion_tokens":4}}`,
		`data: [DONE]`,
	}, "\n\n")

	raw, err := reassembleOpenAIStream(strings.NewReader(sse))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("重建结果不是合法 JSON：%v\n%s", err, raw)
	}
	if len(out.Choices) != 1 {
		t.Fatalf("应有 1 个 choice，得到 %d：%s", len(out.Choices), raw)
	}
	m := out.Choices[0].Message
	if m.Content != "你好" {
		t.Errorf("content = %q，期望 你好", m.Content)
	}
	if len(m.ToolCalls) != 1 {
		t.Fatalf("应有 1 个 tool_call，得到 %d：%s", len(m.ToolCalls), raw)
	}
	if m.ToolCalls[0].ID != "call_1" || m.ToolCalls[0].Function.Name != "find" {
		t.Errorf("tool_call 身份不对：%+v", m.ToolCalls[0])
	}
	if m.ToolCalls[0].Function.Arguments != `{"q":"x"}` {
		t.Errorf("tool_call 参数应拼接完整，得到 %q", m.ToolCalls[0].Function.Arguments)
	}
	if out.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %q，期望 tool_calls", out.Choices[0].FinishReason)
	}
	if out.Usage.PromptTokens != 3 {
		t.Errorf("usage 应保留，得到 %+v", out.Usage)
	}
}

// TestEinoStreamRoundTripForcesStreamAndRebuilds：传输层要把非流式请求体
// 改成 stream=true，并把上游的 SSE 重建成非流式 JSON 交给调用方。
func TestEinoStreamRoundTripForcesStreamAndRebuilds(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"+
			"data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := einoStreamHTTPClient(srv.Client())
	req, _ := http.NewRequest(http.MethodPost, srv.URL,
		strings.NewReader(`{"model":"m","messages":[]}`))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if gotBody["stream"] != true {
		t.Errorf("请求体应被改成 stream=true，实际 body=%v", gotBody)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("重建后的响应 Content-Type 应为 application/json，得到 %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil || len(out.Choices) == 0 {
		t.Fatalf("重建结果无法解析：%v\n%s", err, body)
	}
	if out.Choices[0].Message.Content != "hi" {
		t.Errorf("content = %q，期望 hi", out.Choices[0].Message.Content)
	}
}

// TestEinoStreamRoundTripPassesJSONThrough：上游忽略 stream、直接回整包
// JSON 时原样放行，不做任何重建。
func TestEinoStreamRoundTripPassesJSONThrough(t *testing.T) {
	const payload = `{"choices":[{"message":{"content":"直接回 JSON"}}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, payload)
	}))
	defer srv.Close()

	c := einoStreamHTTPClient(srv.Client())
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{}`))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != payload {
		t.Errorf("非流式响应应原样透传，得到 %s", body)
	}
}
