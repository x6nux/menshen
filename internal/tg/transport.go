package tg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// Transport 抽象对 Telegram Bot API 的调用，便于测试注入假实现。
type Transport interface {
	Call(method string, payload any) (json.RawMessage, error)
}

type httpTransport struct {
	base   string // Bot API 基址，不含尾部斜杠
	token  string
	client *http.Client
}

// httpTimeout 是单次 Bot API 调用的超时。必须大于长轮询 getUpdates 自带的
// 挂起时长（25 秒，见 core 的 RunPolling），否则每一轮空闲轮询都会被
// 客户端超时掐断，表现为日志里持续刷 getUpdates 失败而更新照收。
const httpTimeout = 40 * time.Second

// NewHTTP 按 token 建一个指向 Telegram 的传输层。
//
// tr 为 nil 时不设 Transport，让 net/http 落到 DefaultTransport ——
// 它的 Proxy 是 ProxyFromEnvironment，HTTP_PROXY / HTTPS_PROXY 因此仍然
// 生效。这里若塞一个自造的 Transport，会把环境变量代理静默废掉。
func NewHTTP(base, token string, tr http.RoundTripper) Transport {
	return &httpTransport{base: base, token: token,
		client: &http.Client{Timeout: httpTimeout, Transport: tr}}
}

// endpoint 拼出某个方法的请求 URL。
// 基址侧已统一去掉尾部斜杠，这里只补一个，避免拼出双斜杠 ——
// 有些反代对 // 的处理与官方不一致，会直接 404。
func (h *httpTransport) endpoint(method string) string {
	return strings.TrimRight(h.base, "/") + "/bot" + h.token + "/" + method
}

// callMaxBody 是单次响应体的读取上限。TG 的正常响应很小（update 最多几十 KB），
// 给一个远大于它的界：反代/网关抽风返回巨大错误页时不会把内存吃光。
const callMaxBody = 8 << 20

// retryAfterMax 是 429 自动重试的最长等待。TG 的 flood wait 可能报几百秒，
// 全等会把调用方（尤其串行的更新处理）一起卡死；超过这个值就直接失败，
// 由上层按本次调用失败处理。
const retryAfterMax = 5 * time.Second

func (h *httpTransport) Call(method string, payload any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	// 429（flood control）是唯一自动重试的状态：请求被拒绝、没有副作用，
	// 等 retry_after 再来一次往往就过了。5xx 不重试——不确定 TG 是否已经
	// 处理（sendMessage 这类调用重试会产生重复消息）。
	for attempt := 0; ; attempt++ {
		resp, err := h.client.Post(h.endpoint(method),
			"application/json", bytes.NewReader(body))
		if err != nil {
			return nil, CallError(method, err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, callMaxBody))
		resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests && attempt < 1 {
			if d, ok := retryAfter(raw); ok && d <= retryAfterMax {
				slog.Warn("Telegram 限流，等待后重试", "method", method,
					"retry_after", d.String())
				time.Sleep(d)
				continue
			}
		}
		if readErr == nil && resp.StatusCode >= 400 {
			// 把状态码与响应片段带进错误：否则调用方只能看到响应无法解析，
			// 而 429/5xx 与 400 的处置方式完全不同。
			body := clip(string(raw), 200)
			var parsed struct {
				Description string `json:"description"`
			}
			// 解析失败不碍事：Body 原文照带，Desc 为空时 NotFound 不误判。
			json.Unmarshal(raw, &parsed)
			return raw, &APIError{Method: method, Code: resp.StatusCode,
				Body: body, Desc: parsed.Description}
		}
		return raw, readErr
	}
}

// APIError 是 Bot API 明确拒绝（HTTP ≥400）时的错误：方法、状态码、
// 响应体原文与解析出的 description 都在，调用方据此区分 TG 明确拒绝
// 与网络无响应——前者常是预期内的回答（查无此人之类），后者才是故障。
type APIError struct {
	Method string
	Code   int
	// Body 是截断后的响应体原文，用于日志与兜底匹配。
	Body string
	// Desc 是解析出的 description 字段，响应体不是 JSON 时为空。
	Desc string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: HTTP %d: %s", e.Method, e.Code, e.Body)
}

// NotFound 报告这是不是 Bot API 对查无此人/此群的标准回答。有两类
// 文本：Bot API 自述的 chat/member/user not found，与透传的 MTProto 错误
// PARTICIPANT_ID_INVALID、PEER_ID_INVALID、USER_ID_INVALID——含义一致：
// 对象不存在或与 bot 已无关联，是确定答案而非故障（人已离群、从未与
// bot 私聊过）。
func (e *APIError) NotFound() bool {
	if e == nil || e.Code != http.StatusBadRequest {
		return false
	}
	d := strings.ToLower(e.Desc)
	for _, frag := range [...]string{"not found", "participant_id_invalid",
		"peer_id_invalid", "user_id_invalid"} {
		if strings.Contains(d, frag) {
			return true
		}
	}
	return false
}

// retryAfter 从 429 响应体里取 parameters.retry_after（秒）。
func retryAfter(raw []byte) (time.Duration, bool) {
	var r struct {
		Parameters struct {
			RetryAfter int64 `json:"retry_after"`
		} `json:"parameters"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Parameters.RetryAfter <= 0 {
		return 0, false
	}
	return time.Duration(r.Parameters.RetryAfter) * time.Second, true
}

// clip 截断一段文本，保证不切碎 UTF-8。
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// CallError 去掉 Telegram 调用错误里的请求 URL。
//
// 请求 URL 形如 <基址>/bot<TOKEN>/<method>，而 Go 会把完整 URL 塞进
// *url.Error。原样往上抛的话，每一次网络抖动都会在日志里写一遍**明文
// bot token** —— 拿到它就等于完全接管这个 bot。
//
// 只剥掉外层的 url.Error，内层原因原样保留：排障需要它，而它不含 URL。
func CallError(method string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", method, ue.Err)
	}
	return fmt.Errorf("%s: %w", method, err)
}
