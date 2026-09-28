package antiad

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"menshen/internal/core"
	"menshen/internal/testutil"
)

// withRegistry 给测试里的 Shared 挂上一个 registry，让 botFor 找得到实例
// （网页流程要靠它给申诉人和管理员发消息）。
func withRegistry(t *testing.T, b *core.Bot) {
	t.Helper()
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	core.NewRegistry(b.Shared, stop, nil).LoadAll()
}

func TestFingerprintNormalization(t *testing.T) {
	base := webSignals{Platform: "Win32", Languages: []string{"zh-CN", "en-US"},
		Timezone: "Asia/Shanghai", CanvasHash: "abc", WebGLVendor: "Google Inc.",
		WebGLRenderer: "ANGLE", AudioHash: 12.3456789, Fonts: []string{"Arial", "Menlo"}}
	base.Screen.W, base.Screen.H, base.Screen.Depth, base.Screen.DPR = 1920, 1080, 24, 2
	base.HardwareConcurrency, base.DeviceMemory, base.MaxTouchPoints = 8, 8, 0
	fp := fingerprintOf(base)

	// 横竖屏得到同一个指纹
	rot := base
	rot.Screen.W, rot.Screen.H = 1080, 1920
	if got := fingerprintOf(rot); got != fp {
		t.Error("屏幕宽高互换应得到同一指纹")
	}
	// 语言顺序无关
	langs := base
	langs.Languages = []string{"en-US", "zh-CN"}
	if got := fingerprintOf(langs); got != fp {
		t.Error("语言顺序应无关")
	}
	// 完整 UA 不在指纹里（浏览器一升级就变）
	ua := base
	ua.UA = "Mozilla/5.0 Chrome/140"
	if got := fingerprintOf(ua); got != fp {
		t.Error("UA 变化不该影响指纹")
	}
	// 稳定字段变化则指纹变化
	diff := base
	diff.CanvasHash = "def"
	if got := fingerprintOf(diff); got == fp {
		t.Error("canvas 哈希变化应改变指纹")
	}
}

func TestEvalSignals(t *testing.T) {
	hardCases := []struct {
		name string
		ua   string
		sig  webSignals
	}{
		{"webdriver", "", webSignals{Webdriver: true}},
		{"HeadlessChrome", "Mozilla/5.0 HeadlessChrome/120", webSignals{}},
		{"PhantomJS", "", webSignals{UA: "PhantomJS/2.1"}},
		{"自动化全局量", "", webSignals{Automation: []string{"callPhantom"}}},
		{"$cdc_ 属性", "", webSignals{DocProps: []string{"$cdc_asdjflasutopfhvcZLmcfl_"}}},
	}
	for _, c := range hardCases {
		hard, _ := evalSignals(c.ua, c.sig)
		if len(hard) == 0 {
			t.Errorf("%s 应判硬信号", c.name)
		}
	}

	// 名单外的全局量不算硬信号
	if hard, _ := evalSignals("", webSignals{Automation: []string{"jQuery"}}); len(hard) != 0 {
		t.Errorf("名单外的全局量不该判硬信号: %v", hard)
	}

	softSig := webSignals{
		UA:            "Mozilla/5.0 Chrome/140",
		Languages:     nil,
		WebGLRenderer: "Google SwiftShader",
		Screen: struct {
			W     int     `json:"w"`
			H     int     `json:"h"`
			Depth int     `json:"depth"`
			DPR   float64 `json:"dpr"`
		}{},
		PluginsLen:             0,
		NotificationPermission: "denied",
		PermissionQuery:        "prompt",
	}
	hard, soft := evalSignals("Mozilla/5.0 Firefox/130", softSig)
	if len(hard) != 0 {
		t.Errorf("软信号不该判失败: %v", hard)
	}
	if len(soft) < 5 {
		t.Errorf("软信号应被记录（UA 不一致/语言空/软渲染/屏幕 0/无插件/权限不一致），得到 %v", soft)
	}
}

func TestClientIP(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.9:1234"
	if got := clientIP(b.Shared, req); got != "10.0.0.9" {
		t.Errorf("没配请求头时应取对端地址，得到 %q", got)
	}

	b.Cfg.ClientIPHeader = "CF-Connecting-IP"
	req.Header.Set("CF-Connecting-IP", "203.0.113.7")
	if got := clientIP(b.Shared, req); got != "203.0.113.7" {
		t.Errorf("应取配置的请求头，得到 %q", got)
	}

	req.Header.Set("CF-Connecting-IP", "203.0.113.7, 10.1.1.1")
	if got := clientIP(b.Shared, req); got != "203.0.113.7" {
		t.Errorf("逗号列表应取第一个，得到 %q", got)
	}

	req.Header.Set("CF-Connecting-IP", "not-an-ip")
	if got := clientIP(b.Shared, req); got != "10.0.0.9" {
		t.Errorf("非法值应退回对端地址，得到 %q", got)
	}
}

func TestVerifyTurnstileChecks(t *testing.T) {
	old := turnstileVerifyURL
	t.Cleanup(func() { turnstileVerifyURL = old })

	b, _ := testutil.NewTestBot(t, 1)
	b.Cfg.PublicURL = "https://ad.example.com"
	b.Cfg.TurnstileSecret = "secret"

	var lastForm map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		lastForm = r.PostForm
		w.Write([]byte(`{"success":true,"hostname":"ad.example.com",` +
			`"action":"appeal","cdata":"7"}`))
	}))
	t.Cleanup(srv.Close)
	turnstileVerifyURL = srv.URL

	if ok, why := verifyTurnstile(b.Shared, "tok", "203.0.113.7", 7); !ok {
		t.Fatalf("全部匹配时应通过: %s", why)
	}
	if lastForm["secret"][0] != "secret" || lastForm["remoteip"][0] != "203.0.113.7" {
		t.Errorf("请求应带上 secret 与 remoteip: %v", lastForm)
	}

	cases := []struct {
		name string
		resp string
	}{
		{"success 为假", `{"success":false,"error-codes":["invalid-input-response"]}`},
		{"hostname 不符", `{"success":true,"hostname":"evil.example.com","action":"appeal","cdata":"7"}`},
		{"action 不符", `{"success":true,"hostname":"ad.example.com","action":"login","cdata":"7"}`},
		{"cdata 不符", `{"success":true,"hostname":"ad.example.com","action":"appeal","cdata":"8"}`},
	}
	for _, c := range cases {
		srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(c.resp))
		})
		if ok, _ := verifyTurnstile(b.Shared, "tok", "1.2.3.4", 7); ok {
			t.Errorf("%s 时应判失败", c.name)
		}
	}
}

// TestAppealWebFlowPass 走通「POST 验证 → 签发解禁码 → 通知」。
func TestAppealWebFlowPass(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	b.Cfg.PublicURL = "https://ad.example.com"
	b.Cfg.TurnstileSiteKey, b.Cfg.TurnstileSecret = "site", "secret"
	if err := EnsureWebSecret(b.Shared); err != nil {
		t.Fatal(err)
	}
	saveJoinMute(b, -100, 555, "简介里有联系方式", 0)
	withRegistry(t, b)

	now := time.Now().Unix()
	res, err := b.Store.Write.Exec(`INSERT INTO appeals
		(bot_id,user_id,status,web_since,created_at,updated_at)
		VALUES (?,?,'web',?,?,?)`, b.BotID(), 555, now, now, now)
	if err != nil {
		t.Fatal(err)
	}
	appealID, _ := res.LastInsertId()

	old := turnstileVerifyURL
	t.Cleanup(func() { turnstileVerifyURL = old })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"success":true,"hostname":"ad.example.com","action":"appeal","cdata":"%d"}`,
			appealID)
	}))
	t.Cleanup(srv.Close)
	turnstileVerifyURL = srv.URL

	body, _ := json.Marshal(map[string]any{
		"token": "tok",
		"signals": map[string]any{
			"platform": "Win32", "languages": []string{"zh-CN"},
			"screen": map[string]any{"w": 1920, "h": 1080, "depth": 24, "dpr": 2},
		},
	})
	path := fmt.Sprintf("/_w/ap/%d/%s", appealID, appealSig(b.Shared, appealID, 555))
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	req.RemoteAddr = "203.0.113.7:9999"
	w := httptest.NewRecorder()
	WebHandler(b.Shared).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	var out struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || !out.OK {
		t.Fatalf("响应应 ok:true，得到 %s", w.Body.String())
	}
	if !unlockCodeRe.MatchString(out.Code) {
		t.Errorf("解禁码格式不对: %q", out.Code)
	}
	if st := appealStatus(t, b, 555); st != "code" {
		t.Errorf("申诉单应进入 code，得到 %q", st)
	}
	var result, fp string
	if err := b.Store.Read.QueryRow(
		`SELECT result, fp FROM web_checks WHERE appeal_id=?`, appealID).
		Scan(&result, &fp); err != nil || result != "pass" || fp == "" {
		t.Errorf("应写一行通过的验证记录，得到 result=%q fp=%q err=%v", result, fp, err)
	}
	// 私聊申诉人解禁码 + 给归属人推卡片
	found := false
	for _, c := range fake.Calls("sendMessage") {
		if text, _ := c["text"].(string); strings.Contains(text, out.Code) {
			found = true
		}
	}
	if !found {
		t.Error("应把解禁码私聊给申诉人")
	}
}

// TestAppealWebFlowBotFailsAndRejects：硬信号判失败，满 5 次结案。
func TestAppealWebFlowBotFailsAndRejects(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	b.Cfg.PublicURL = "https://ad.example.com"
	b.Cfg.TurnstileSiteKey, b.Cfg.TurnstileSecret = "site", "secret"
	if err := EnsureWebSecret(b.Shared); err != nil {
		t.Fatal(err)
	}
	saveJoinMute(b, -100, 555, "简介里有联系方式", 0)
	withRegistry(t, b)

	now := time.Now().Unix()
	res, err := b.Store.Write.Exec(`INSERT INTO appeals
		(bot_id,user_id,status,web_since,created_at,updated_at)
		VALUES (?,?,'web',?,?,?)`, b.BotID(), 555, now, now, now)
	if err != nil {
		t.Fatal(err)
	}
	appealID, _ := res.LastInsertId()
	path := fmt.Sprintf("/_w/ap/%d/%s", appealID, appealSig(b.Shared, appealID, 555))
	handler := WebHandler(b.Shared)

	for i := 0; i < 5; i++ {
		body, _ := json.Marshal(map[string]any{
			"token": "tok", "signals": map[string]any{"webdriver": true}})
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
		req.RemoteAddr = "203.0.113.7:9999"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("第 %d 次请求应 200，得到 %d", i+1, w.Code)
		}
		if strings.Contains(w.Body.String(), `"ok":true`) {
			t.Fatalf("第 %d 次应判失败", i+1)
		}
	}

	if st := appealStatus(t, b, 555); st != "rejected" {
		t.Errorf("失败满 5 次应进入 rejected，得到 %q", st)
	}
	var result string
	if err := b.Store.Read.QueryRow(
		`SELECT result FROM web_checks WHERE appeal_id=? ORDER BY id DESC LIMIT 1`,
		appealID).Scan(&result); err != nil || result != "bot" {
		t.Errorf("最后一次验证记录应为 bot，得到 %q err=%v", result, err)
	}
}

// TestAppealPageHeadersAndExpiry：验证页带齐安全响应头；超过 24 小时
// 的链接返回失效页。
func TestAppealPageHeadersAndExpiry(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	b.Cfg.PublicURL = "https://ad.example.com"
	b.Cfg.TurnstileSiteKey, b.Cfg.TurnstileSecret = "site", "secret"
	if err := EnsureWebSecret(b.Shared); err != nil {
		t.Fatal(err)
	}

	now := time.Now().Unix()
	res, err := b.Store.Write.Exec(`INSERT INTO appeals
		(bot_id,user_id,status,web_since,created_at,updated_at)
		VALUES (?,?,'web',?,?,?)`, b.BotID(), 555, now, now, now)
	if err != nil {
		t.Fatal(err)
	}
	appealID, _ := res.LastInsertId()
	path := fmt.Sprintf("/_w/ap/%d/%s", appealID, appealSig(b.Shared, appealID, 555))
	handler := WebHandler(b.Shared)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("有效链接应 200，得到 %d", w.Code)
	}
	h := w.Header()
	if h.Get("Cache-Control") != "no-store" || !strings.Contains(h.Get("X-Robots-Tag"), "noindex") {
		t.Errorf("缺少 no-store / noindex 头: %v", h)
	}
	csp := h.Get("Content-Security-Policy")
	// nonce 必须整体被成对单引号包住（'nonce-xxx'）。少收尾那个引号时，
	// CSP 解析器判定整个 script-src 非法、回退到 default-src 'self'，
	// 页面上的内联脚本从此不执行 —— Turnstile 能解出来，但没有任何东西
	// 会去提交，用户永远拿不到解禁码，而服务端日志里一行痕迹都没有。
	m := regexp.MustCompile(`script-src 'nonce-([A-Za-z0-9+/=_-]+)'`).FindStringSubmatch(csp)
	if m == nil {
		t.Fatalf("CSP 的 nonce 表达式应形如 'nonce-<value>'（成对引号）: %q", csp)
	}
	if !strings.Contains(csp, "challenges.cloudflare.com") {
		t.Errorf("CSP 应放行 Turnstile 脚本: %q", csp)
	}
	body := w.Body.String()
	if !strings.Contains(body, `nonce="`+m[1]+`"`) {
		t.Errorf("页面脚本的 nonce 应与 CSP 头一致：csp=%s", m[1])
	}
	if !strings.Contains(body, "function onToken") {
		t.Error("页面应包含提交用的内联脚本")
	}
	if !strings.Contains(body, "cf-turnstile") ||
		!strings.Contains(body, `data-sitekey="site"`) {
		t.Error("页面应包含 Turnstile 组件")
	}

	// 超过 24 小时：失效页
	if _, err := b.Store.Write.Exec(`UPDATE appeals SET web_since=? WHERE id=?`,
		now-25*3600, appealID); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusGone {
		t.Errorf("过期链接应 410，得到 %d", w.Code)
	}
}
