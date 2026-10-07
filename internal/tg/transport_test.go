package tg

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestNewHTTPRoutesThroughProxy 验证传入的 RoundTripper 确实被使用。
//
// 不只是断言字段相等（那只能证明赋值正确），而是起一个假代理，检查它是否
// 收到指向目标主机的请求：若未走代理，请求会直接发往目标地址，假代理收不到
// 任何请求。
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

	// 目标使用 http 而非 https：https 会先发 CONNECT 建隧道，
	// 假代理看不到内部路径，无法验证请求确实经过它。
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

// TestNewHTTPNilTransportKeepsDefault 锁定不配代理 = 沿用默认。
//
// client.Transport 必须保持 nil，这样 net/http 才会落到 DefaultTransport，
// 其 Proxy 是 ProxyFromEnvironment，环境变量代理依赖这条路径。
// 自己构造 Transport 会使 HTTPS_PROXY 失效。
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

// TestCallRetriesAfterFloodWait：429 且 retry_after 较短时会等待后重试，
// 第二次成功。限流是瞬时状态，直接失败会使删除/禁言在广告高峰时批量丢失。
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

// TestCallBadRequestIsStructuredAPIError：400 的查无此人类拒答要带回
// 结构化的 description：上层据此区分 TG 明确返回不存在与网络故障，前者是
// 常态，不应打印警告。两类文本都要识别：Bot API 自述的 * not found，
// 与透传的 MTProto *_ID_INVALID。
func TestCallBadRequestIsStructuredAPIError(t *testing.T) {
	gone := []string{
		"Bad Request: chat not found",
		"Bad Request: member not found",
		"Bad Request: PARTICIPANT_ID_INVALID",
		"Bad Request: PEER_ID_INVALID",
		"Bad Request: USER_ID_INVALID",
	}
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"ok":false,"error_code":400,"description":"` +
			gone[n-1] + `"}`))
	}))
	defer srv.Close()

	c := NewHTTP(srv.URL, "tok", nil)
	for i, desc := range gone {
		_, err := c.Call("getChat", map[string]any{"chat_id": 123})
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("400 应返回 *APIError，得到 %T: %v", err, err)
		}
		if apiErr.Code != http.StatusBadRequest || apiErr.Desc != desc {
			t.Errorf("第 %d 次: Code=%d Desc=%q，期望 %q", i+1, apiErr.Code, apiErr.Desc, desc)
		}
		if !apiErr.NotFound() {
			t.Errorf("%q 应判为 NotFound", desc)
		}
		// 日志格式保持原样：方法 + 状态码 + 响应体。
		if want := "getChat: HTTP 400: "; !strings.HasPrefix(err.Error(), want) {
			t.Errorf("错误文本应保留 %q 前缀，得到 %q", want, err.Error())
		}
	}

	// 无关的 400（对象存在但请求本身有问题）不得判为 NotFound。
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"ok":false,"error_code":400,` +
			`"description":"Bad Request: MESSAGE_ID_INVALID"}`))
	}))
	defer srv2.Close()
	c2 := NewHTTP(srv2.URL, "tok", nil)
	_, err := c2.Call("getChatMember", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.NotFound() {
		t.Errorf("MESSAGE_ID_INVALID 不得判为 NotFound，得到 %v", err)
	}

	// 非 JSON 的 400（网关错误页）：Desc 解析不出，NotFound 必须为假，
	// 按故障重试处理，不得当作对象不存在。
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`<html>bad request</html>`))
	}))
	defer srv3.Close()
	c3 := NewHTTP(srv3.URL, "tok", nil)
	_, err = c3.Call("getChatMember", nil)
	if !errors.As(err, &apiErr) || apiErr.NotFound() {
		t.Errorf("非 JSON 的 400 不得判为 NotFound，得到 %v", err)
	}
}

// TestCallServerErrorIncludesStatus：5xx 不重试（不确定 TG 是否已处理），
// 但错误里要能看到状态码：否则出错时只能得到响应无法解析，排障时无法辨识
// 网关故障。
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
