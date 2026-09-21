package tg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
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
// 挂起时长（25 秒，见 core 的 runPolling），否则每一轮空闲轮询都会被
// 客户端超时掐断，表现为日志里持续刷 getUpdates 失败而更新照收。
const httpTimeout = 40 * time.Second

// NewHTTP 按 token 建一个指向 Telegram 的传输层。
func NewHTTP(base, token string) Transport {
	return &httpTransport{base: base, token: token,
		client: &http.Client{Timeout: httpTimeout}}
}

// endpoint 拼出某个方法的请求 URL。
// 基址侧已统一去掉尾部斜杠，这里只补一个，避免拼出双斜杠 ——
// 有些反代对 // 的处理与官方不一致，会直接 404。
func (h *httpTransport) endpoint(method string) string {
	return strings.TrimRight(h.base, "/") + "/bot" + h.token + "/" + method
}

func (h *httpTransport) Call(method string, payload any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp, err := h.client.Post(h.endpoint(method),
		"application/json", bytes.NewReader(body))
	if err != nil {
		return nil, CallError(method, err)
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// CallError 去掉 Telegram 调用错误里的请求 URL。
//
// 请求 URL 形如 <基址>/bot<TOKEN>/<method>，而 Go 会把完整 URL 塞进
// *url.Error。原样往上抛的话，每一次网络抖动都会在日志里写一遍**明文
// bot token** —— 拿到它就等于完全接管这个 bot。
//
// 只剥掉外层的 url.Error，内层原因（dial tcp ...: i/o timeout 之类）
// 原样保留：排障需要的恰恰是它，而它不含 URL。
func CallError(method string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", method, ue.Err)
	}
	return fmt.Errorf("%s: %w", method, err)
}
