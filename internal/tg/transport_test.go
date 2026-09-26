package tg

import (
	"net/http"
	"net/http/httptest"
	"net/url"
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
