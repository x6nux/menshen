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

// enableCaptchaDemo 打开演示页并给 hcaptcha 配一组假密钥。
func enableCaptchaDemo(t *testing.T, b *core.Bot) {
	t.Helper()
	// 测试台开关与密钥都在 settings 表里（网页面板配置）。
	for k, v := range map[string]string{
		"captcha_demo":      "1",
		"captcha_demo_keys": "hcaptcha=hsite,hsecret",
	} {
		if err := b.PutSetting(k, v); err != nil {
			t.Fatalf("putSetting %s: %v", k, err)
		}
	}
	b.Cfg.PublicURL = "https://ad.example.com"
	if err := EnsureWebSecret(b.Shared); err != nil {
		t.Fatal(err)
	}
}

func TestCaptchaDemoListsProviders(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	enableCaptchaDemo(t, b)

	req := httptest.NewRequest(http.MethodGet, "/_w/demo/1/x?json=1", nil)
	w := httptest.NewRecorder()
	WebHandler(b.Shared).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("演示页数据应 200，得到 %d", w.Code)
	}
	var out struct {
		Providers []struct {
			Provider string `json:"provider"`
			SiteKey  string `json:"sitekey"`
			Endpoint string `json:"endpoint"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应无法解析：%s", w.Body.String())
	}
	names := map[string]string{}
	for _, p := range out.Providers {
		names[p.Provider] = p.Endpoint
	}
	if names["hcaptcha"] != "" {
		t.Errorf("hcaptcha 不该带 endpoint：%v", names)
	}
	if _, ok := names["cap"]; !ok {
		t.Error("cap 是内置实现，应始终出现在演示页")
	}
	if !strings.Contains(names["cap"], "/cap/menshen/") {
		t.Errorf("cap 的端点应指向内置服务，得到 %q", names["cap"])
	}
	if _, ok := names["turnstile"]; ok {
		t.Error("未配密钥的 turnstile 不该出现")
	}
}

func TestCaptchaDemoDisabled(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	b.Cfg.PublicURL = "https://ad.example.com"
	EnsureWebSecret(b.Shared)
	// 未打开 captcha_demo
	w := httptest.NewRecorder()
	WebHandler(b.Shared).ServeHTTP(w,
		httptest.NewRequest(http.MethodGet, "/_w/demo/1/x?json=1", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("未开启演示页应 404，得到 %d", w.Code)
	}
}

func TestCaptchaDemoPostHcaptcha(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	enableCaptchaDemo(t, b)

	old := hcaptchaVerifyURL
	t.Cleanup(func() { hcaptchaVerifyURL = old })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"hostname":"ad.example.com"}`))
	}))
	t.Cleanup(srv.Close)
	hcaptchaVerifyURL = srv.URL

	body, _ := json.Marshal(map[string]any{
		"provider": "hcaptcha", "token": "tok", "signals": map[string]any{}})
	req := httptest.NewRequest(http.MethodPost, "/_w/demo/1/x", strings.NewReader(string(body)))
	req.RemoteAddr = "203.0.113.7:9999"
	w := httptest.NewRecorder()
	WebHandler(b.Shared).ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("hcaptcha 校验应通过，得到 %s", w.Body.String())
	}

	// 未知 / 未配置的提供方。
	body, _ = json.Marshal(map[string]any{"provider": "turnstile", "token": "tok"})
	req = httptest.NewRequest(http.MethodPost, "/_w/demo/1/x", strings.NewReader(string(body)))
	req.RemoteAddr = "203.0.113.8:9999"
	w = httptest.NewRecorder()
	WebHandler(b.Shared).ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("未配置的提供方应 400，得到 %d", w.Code)
	}
}

// TestCaptchaDemoPostCap：cap 走进程内校验，端到端（挑战→解算→兑换→提交）。
func TestCaptchaDemoPostCap(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	enableCaptchaDemo(t, b)

	// 取回内置 cap 的端点，按 widget 的方式取挑战并解算。
	base := CapBaseURL(b.Shared)
	if base == "" {
		t.Fatal("cap 端点为空")
	}
	w := httptest.NewRecorder()
	CapHandler(b.Shared).ServeHTTP(w,
		httptest.NewRequest(http.MethodPost, "/cap/menshen/challenge", nil))
	var ch struct {
		Challenge struct{ C, S, D int } `json:"challenge"`
		Token     string                `json:"token"`
	}
	json.Unmarshal(w.Body.Bytes(), &ch)
	sol := capSolve(t, ch.Token, ch.Challenge.C, ch.Challenge.S, ch.Challenge.D)
	rb, _ := json.Marshal(map[string]any{"token": ch.Token, "solutions": sol})
	w = httptest.NewRecorder()
	CapHandler(b.Shared).ServeHTTP(w, httptest.NewRequest(http.MethodPost,
		"/cap/menshen/redeem", strings.NewReader(string(rb))))
	var rd struct {
		Success bool   `json:"success"`
		Token   string `json:"token"`
	}
	json.Unmarshal(w.Body.Bytes(), &rd)
	if !rd.Success {
		t.Fatalf("兑换失败：%s", w.Body.String())
	}

	body, _ := json.Marshal(map[string]any{
		"provider": "cap", "token": rd.Token, "signals": map[string]any{}})
	req := httptest.NewRequest(http.MethodPost, "/_w/demo/1/x", strings.NewReader(string(body)))
	req.RemoteAddr = "203.0.113.9:9999"
	w = httptest.NewRecorder()
	WebHandler(b.Shared).ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("cap 端到端应通过，得到 %s", w.Body.String())
	}
}

// TestParseCaptchaDemoKeys：紧凑格式解析与错误分支（面板写入也走它）。
func TestParseCaptchaDemoKeys(t *testing.T) {
	got, err := parseCaptchaDemoKeys(
		"turnstile=sk1,sec1; hcaptcha=sk2,sec2; cap=ignored,ignored")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got["turnstile"].SiteKey != "sk1" || got["turnstile"].Secret != "sec1" {
		t.Errorf("turnstile 解析错误: %+v", got["turnstile"])
	}
	if got["hcaptcha"].SiteKey != "sk2" {
		t.Errorf("hcaptcha 解析错误: %+v", got["hcaptcha"])
	}
	if _, ok := got["cap"]; ok {
		t.Error("cap 是内置实现，不该进演示密钥表")
	}
	for _, bad := range []string{
		"recaptcha=a,b",  // 不支持的提供方
		"turnstile=sk1",  // 缺 secret
		"turnstile=,sec", // 缺 site key
		"turnstile=a,b,c",
	} {
		if _, err := parseCaptchaDemoKeys(bad); err == nil {
			t.Errorf("%q 应当报错", bad)
		}
	}
}

// TestCaptchaProviderValid：提供方白名单（空串 = 关闭，合法）。
func TestCaptchaProviderValid(t *testing.T) {
	for _, ok := range []string{"", "turnstile", "hcaptcha", "cap", " CAP "} {
		if !CaptchaProviderValid(ok) {
			t.Errorf("%q 应当合法", ok)
		}
	}
	for _, bad := range []string{"recaptcha", "google", "captcha"} {
		if CaptchaProviderValid(bad) {
			t.Errorf("%q 应当被拒", bad)
		}
	}
}
