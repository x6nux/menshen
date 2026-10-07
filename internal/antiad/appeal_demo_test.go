package antiad

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"menshen/internal/core"
	"menshen/internal/testutil"
)

// enableAppealDemo 打开测试台，配上申诉验证用的 Turnstile 密钥（config 直读）。
func enableAppealDemo(t *testing.T, b *core.Bot) {
	t.Helper()
	if err := b.PutSetting("captcha_demo", "1"); err != nil {
		t.Fatalf("putSetting: %v", err)
	}
	b.Cfg.PublicURL = "https://ad.example.com"
	b.Cfg.TurnstileSiteKey = "asite"
	b.Cfg.TurnstileSecret = "asecret"
	if err := EnsureWebSecret(b.Shared); err != nil {
		t.Fatal(err)
	}
}

func TestAppealDemoDisabled(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	b.Cfg.PublicURL = "https://ad.example.com"
	EnsureWebSecret(b.Shared)
	// 未打开 captcha_demo
	w := httptest.NewRecorder()
	WebHandler(b.Shared).ServeHTTP(w,
		httptest.NewRequest(http.MethodGet, "/_w/apdemo/1/x?json=1", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("未开启测试台应 404，得到 %d", w.Code)
	}
}

func TestAppealDemoData(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	enableAppealDemo(t, b)

	w := httptest.NewRecorder()
	WebHandler(b.Shared).ServeHTTP(w,
		httptest.NewRequest(http.MethodGet, "/_w/apdemo/1/x?json=1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("测试台数据应 200，得到 %d", w.Code)
	}
	var out struct {
		SiteKey string `json:"sitekey"`
		CData   string `json:"cdata"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应无法解析：%s", w.Body.String())
	}
	if out.SiteKey != "asite" || out.CData != "1" {
		t.Errorf("sitekey / cdata 应是 config 密钥与路由 id，得到 %+v", out)
	}
}

func TestAppealDemoPost(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	enableAppealDemo(t, b)

	old := turnstileVerifyURL
	t.Cleanup(func() { turnstileVerifyURL = old })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"success":true,"hostname":"ad.example.com",` +
			`"action":"appeal","cdata":"1"}`))
	}))
	t.Cleanup(srv.Close)
	turnstileVerifyURL = srv.URL

	// 全部相符：通过。
	body, _ := json.Marshal(map[string]any{"token": "tok", "signals": map[string]any{}})
	req := httptest.NewRequest(http.MethodPost, "/_w/apdemo/1/x", strings.NewReader(string(body)))
	req.RemoteAddr = "203.0.113.7:9999"
	w := httptest.NewRecorder()
	WebHandler(b.Shared).ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("校验应通过，得到 %s", w.Body.String())
	}

	// cdata 不符：失败并把原因显示出来 —— 这正是测试台存在的意义。
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"success":true,"hostname":"ad.example.com",` +
			`"action":"appeal","cdata":"2"}`))
	})
	body, _ = json.Marshal(map[string]any{"token": "tok", "signals": map[string]any{}})
	req = httptest.NewRequest(http.MethodPost, "/_w/apdemo/1/x", strings.NewReader(string(body)))
	req.RemoteAddr = "203.0.113.8:9999"
	w = httptest.NewRecorder()
	WebHandler(b.Shared).ServeHTTP(w, req)
	if strings.Contains(w.Body.String(), `"ok":true`) ||
		!strings.Contains(w.Body.String(), "cdata 不符") {
		t.Errorf("cdata 不符应失败且显示原因，得到 %s", w.Body.String())
	}

	// 空令牌：提示组件没加载出来。
	body, _ = json.Marshal(map[string]any{"token": "", "signals": map[string]any{}})
	req = httptest.NewRequest(http.MethodPost, "/_w/apdemo/1/x", strings.NewReader(string(body)))
	req.RemoteAddr = "203.0.113.9:9999"
	w = httptest.NewRecorder()
	WebHandler(b.Shared).ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "没有返回令牌") {
		t.Errorf("空令牌应提示组件未加载，得到 %s", w.Body.String())
	}

	// 未配密钥：直接说明缺什么。
	b.Cfg.TurnstileSiteKey = ""
	b.Cfg.TurnstileSecret = ""
	body, _ = json.Marshal(map[string]any{"token": "tok", "signals": map[string]any{}})
	req = httptest.NewRequest(http.MethodPost, "/_w/apdemo/1/x", strings.NewReader(string(body)))
	req.RemoteAddr = "203.0.113.10:9999"
	w = httptest.NewRecorder()
	WebHandler(b.Shared).ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "未配置申诉验证的 Turnstile 密钥") {
		t.Errorf("未配密钥应说明缺什么，得到 %s", w.Body.String())
	}
}
