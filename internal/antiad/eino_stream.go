package antiad

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"menshen/internal/core"
)

// ---- Eino 客户端的流式传输层 ----
//
// 判定链路的 AI 请求（复判 / 识图 / 摘要 / 申诉）都由 aiCall 显式走流式。
// 规则发现 Agent 用的是 Eino 的 OpenAI ChatModel，它的 Generate 只发非流式
// 请求（CreateChatCompletion），而 Agent 依赖 SDK 自行解析流式 tool_calls
// 分片的能力：直接切成流式时，若上游忽略 stream、只回整包 JSON，tool_calls
// 会丢失，Agent 直接收尾，建不出规则。
//
// 所以这里在传输层做转换，且只包 Eino 那个客户端：请求发出前把 body 置为
// stream=true，响应若是 SSE 就重建成 OpenAI 非流式响应体再交给 SDK。这样
// 实际请求是流式的（首字可判卡顿、长响应不会被整包超时拖死），而 SDK 看到
// 的仍是普通 completion，tool_calls、usage 与回调不变。

// einoStreamRT 是只给 Eino 客户端用的 RoundTripper。
type einoStreamRT struct {
	base http.RoundTripper
}

func (t *einoStreamRT) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	if err := forceStreamBody(req); err != nil {
		return nil, err
	}
	resp, err := base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	// 上游忽略 stream、直接回整包 JSON：原样交给 SDK。
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return resp, nil
	}
	rebuilt, rerr := reassembleOpenAIStream(resp.Body)
	resp.Body.Close()
	if rerr != nil {
		return nil, rerr
	}
	resp.Body = io.NopCloser(bytes.NewReader(rebuilt))
	resp.ContentLength = int64(len(rebuilt))
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Del("Transfer-Encoding")
	return resp, nil
}

// forceStreamBody 把 JSON 请求体里的 stream 置为 true。
// 非 JSON 体原样放行（Eino 的 chat 请求都是 JSON，兜底而已）。
func forceStreamBody(req *http.Request) error {
	if req.Body == nil || req.Method != http.MethodPost {
		return nil
	}
	raw, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return err
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) == nil {
		body["stream"] = true
		if nb, merr := json.Marshal(body); merr == nil {
			raw = nb
		}
	}
	req.Body = io.NopCloser(bytes.NewReader(raw))
	req.ContentLength = int64(len(raw))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(raw)), nil
	}
	return nil
}

// toolCallAcc 按 index 累加一条流式 tool_call 的分片。
type toolCallAcc struct {
	id, typ, name string
	args          strings.Builder
}

// reassembleOpenAIStream 读 OpenAI 兼容的 chat/completions SSE 流，重建成
// 非流式响应体：choices[0].message（含 content / reasoning_content /
// tool_calls）与流末的 usage。tool_calls 按 index 合并，与官方 SDK 一致。
func reassembleOpenAIStream(r io.Reader) ([]byte, error) {
	var (
		content, reasoning strings.Builder
		order              []int
		byIndex            = map[int]*toolCallAcc{}
		usage              json.RawMessage
		finish             string
	)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "data:")
		if !ok {
			continue // 空行、心跳注释、event: 行
		}
		data = strings.TrimSpace(data)
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage json.RawMessage `json:"usage"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue // 个别网关会夹带非 JSON 的行
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return nil, fmt.Errorf("上游在流中报错: %s",
				core.TruncateRunes(string(chunk.Error), 200))
		}
		for _, c := range chunk.Choices {
			d := c.Delta
			content.WriteString(d.Content)
			reasoning.WriteString(d.ReasoningContent)
			reasoning.WriteString(d.Reasoning)
			if c.FinishReason != "" {
				finish = c.FinishReason
			}
			for _, tc := range d.ToolCalls {
				acc := byIndex[tc.Index]
				if acc == nil {
					acc = &toolCallAcc{}
					byIndex[tc.Index] = acc
					order = append(order, tc.Index)
				}
				if tc.ID != "" {
					acc.id = tc.ID
				}
				if tc.Type != "" {
					acc.typ = tc.Type
				}
				if tc.Function.Name != "" {
					acc.name = tc.Function.Name
				}
				acc.args.WriteString(tc.Function.Arguments)
			}
		}
		if len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
			usage = chunk.Usage
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	msg := map[string]any{"content": content.String()}
	if reasoning.Len() > 0 {
		msg["reasoning_content"] = reasoning.String()
	}
	if len(order) > 0 {
		sort.Ints(order)
		tcs := make([]map[string]any, 0, len(order))
		for _, i := range order {
			tc := byIndex[i]
			typ := tc.typ
			if typ == "" {
				typ = "function"
			}
			tcs = append(tcs, map[string]any{
				"id": tc.id, "type": typ,
				"function": map[string]any{"name": tc.name, "arguments": tc.args.String()},
			})
		}
		msg["tool_calls"] = tcs
	}
	if finish == "" {
		if len(order) > 0 {
			finish = "tool_calls"
		} else {
			finish = "stop"
		}
	}
	out := map[string]any{"choices": []map[string]any{{
		"message": msg, "finish_reason": finish,
	}}}
	if len(usage) > 0 {
		out["usage"] = usage
	}
	return json.Marshal(out)
}

// einoStreamHTTPClient 在现有 AI 客户端外面套一层流式转换。
// base 为 nil 时新建一个（默认 60 秒超时），否则克隆以保留连接池与超时。
func einoStreamHTTPClient(base *http.Client) *http.Client {
	if base == nil {
		return &http.Client{
			Transport: &einoStreamRT{base: http.DefaultTransport},
			Timeout:   60 * time.Second,
		}
	}
	c := *base
	c.Transport = &einoStreamRT{base: base.Transport}
	return &c
}
