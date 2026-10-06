package tg

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestNewHTTPRoutesThroughProxy 确认传进来的 RoundTripper 真的被用上了。
//
// 不只是断言字段相等：那只能证明赋值没写错，证明不了请求真的绕了代理。
// 这里起一个假代理，看它有没有收到指向目标主机的那一条请求 —— 接错线时
// 请求会直奔目标地址，假代理一条都收不到。
func TestNewHTTPRoutesThroughProxy(t *testing.T) {
	var gotHost, gotPath string
	proxy := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			gotHost, gotPath = r.Host, r.URL.Path
			w.Write([]byte(`{"ok":true,"result":{}}`))
		}))
	defer proxy.Close()

	pu, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatalf("解析代理地址: %v", err)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(pu)

	// 目标用 http 而不是 https：https 会先发 CONNECT 建隧道，
	// 假代理看不到里面的路径，也就验证不了「请求确实是发给它的」。
	c := NewHTTP("http://api.telegram.invalid", "tok", tr)
	if _, err := c.Call("getMe", nil); err != nil {
		t.Fatalf("经代理调用失败: %v", err)
	}

	if gotHost != "api.telegram.invalid" {
		t.Errorf("代理收到的目标主机 = %q, 期望 api.telegram.invalid"+
			"（为空说明请求没走代理）", gotHost)
	}
	if gotPath != "/bottok/getMe" {
		t.Errorf("代理收到的路径 = %q, 期望 /bottok/getMe", gotPath)
	}
}

// TestNewHTTPNilTransportKeepsDefault 锁住「不配代理 = 沿用默认」。
//
// client.Transport 必须保持 nil，这样 net/http 才会落到 DefaultTransport，
// 而它的 Proxy 是 ProxyFromEnvironment —— 环境变量代理靠的就是这条路径。
// 在这里塞一个自造的 Transport 会把 HTTPS_PROXY 悄悄废掉。
func TestNewHTTPNilTransportKeepsDefault(t *testing.T) {
	h, ok := NewHTTP("https://x", "tok", nil).(*httpTransport)
	if !ok {
		t.Fatal("NewHTTP 应当返回 *httpTransport")
	}
	if h.client.Transport != nil {
		t.Errorf("未配代理时 Transport 必须为 nil（否则环境变量代理失效），得到 %T",
			h.client.Transport)
	}
}

// TestCallRetriesAfterFloodWait：429 + 短的 retry_after 会等一轮再来，
// 第二次成功。限流是瞬时状态，直接失败会让删除/禁言在广告洪峰里成批丢失。
func TestCallRetriesAfterFloodWait(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"ok":false,"error_code":429,` +
				`"description":"Too Many Requests: retry after 1",` +
				`"parameters":{"retry_after":1}}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{"id":42}}`))
	}))
	defer srv.Close()

	c := NewHTTP(srv.URL, "tok", nil)
	raw, err := c.Call("getMe", nil)
	if err != nil {
		t.Fatalf("429 后重试应成功: %v", err)
	}
	if !strings.Contains(string(raw), `"id":42`) {
		t.Errorf("应返回第二次的响应，得到 %s", raw)
	}
	if n != 2 {
		t.Errorf("应恰好请求两次，得到 %d", n)
	}
}

// TestCallFloodWaitTooLongFailsFast：retry_after 超过上限时不等待，直接
// 带状态码失败——否则串行的更新处理会被 flood wait 卡住几分钟。
func TestCallFloodWaitTooLongFailsFast(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"ok":false,"error_code":429,` +
			`"parameters":{"retry_after":600}}`))
	}))
	defer srv.Close()

	c := NewHTTP(srv.URL, "tok", nil)
	_, err := c.Call("sendMessage", nil)
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("长 flood wait 应带 429 直接失败，得到 %v", err)
	}
}

// TestCallBadRequestIsStructuredAPIError：400 的「查无此人」类拒答要带
// 结构化的 description 回来——上层靠它区分「TG 明确说没有」与「网络故障」，
// 前者是常态不该刷警告（离群成员曾因此在复查阶梯里无限空转）。
func TestCallBadRequestIsStructuredAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"ok":false,"error_code":400,` +
			`"description":"Bad Request: chat not found"}`))
	}))
	defer srv.Close()

	c := NewHTTP(srv.URL, "tok", nil)
	_, err := c.Call("getChat", map[string]any{"chat_id": 123})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("400 应返回 *APIError，得到 %T: %v", err, err)
	}
	if apiErr.Code != http.StatusBadRequest {
		t.Errorf("Code = %d，期望 400", apiErr.Code)
	}
	if apiErr.Desc != "Bad Request: chat not found" {
		t.Errorf("Desc = %q", apiErr.Desc)
	}
	if !apiErr.NotFound() {
		t.Error("chat not found 应判为 NotFound")
	}
	// 日志格式保持原样：方法 + 状态码 + 响应体。
	if want := "getChat: HTTP 400: "; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("错误文本应保留 %q 前缀，得到 %q", want, err.Error())
	}

	// 非 JSON 的 400（网关错误页）：Desc 解析不出，NotFound 必须为假，
	// 宁可当故障重试，也不能把网关抽风当成「人不在了」。
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`<html>bad request</html>`))
	}))
	defer srv2.Close()
	c2 := NewHTTP(srv2.URL, "tok", nil)
	_, err = c2.Call("getChatMember", nil)
	if !errors.As(err, &apiErr) || apiErr.NotFound() {
		t.Errorf("非 JSON 的 400 不得判为 NotFound，得到 %v", err)
	}
}

// TestCallServerErrorIncludesStatus：5xx 不重试（不确定 TG 是否已处理），
// 但错误里要能看见状态码——原实现把错误页当成功响应返回，调用方只能报
// 「响应无法解析」，排障时看不出是网关挂了。
func TestCallServerErrorIncludesStatus(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`<html>bad gateway</html>`))
	}))
	defer srv.Close()

	c := NewHTTP(srv.URL, "tok", nil)
	_, err := c.Call("sendMessage", nil)
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("502 应带状态码失败，得到 %v", err)
	}
	if n != 1 {
		t.Errorf("5xx 不该重试，得到 %d 次请求", n)
	}
}
