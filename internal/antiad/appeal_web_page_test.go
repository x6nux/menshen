package antiad

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	saveJoinMute(b, -100, 555, kindProfile, "简介里有联系方式", 0)
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
	saveJoinMute(b, -100, 555, kindProfile, "简介里有联系方式", 0)
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

// TestAppealPageDataAndExpiry：验证页数据接口带齐 no-store/noindex，
// 带上 sitekey / cdata / 天数；链接过 24 小时返回 410，签名不对 404。
func TestAppealPageDataAndExpiry(t *testing.T) {
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
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path+"?json=1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("有效链接应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	h := w.Header()
	if h.Get("Cache-Control") != "no-store" || !strings.Contains(h.Get("X-Robots-Tag"), "noindex") {
		t.Errorf("缺少 no-store / noindex 头: %v", h)
	}
	var data map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatalf("响应应是 JSON：%v（%s）", err, w.Body.String())
	}
	if data["sitekey"] != "site" {
		t.Errorf("数据应带 Turnstile sitekey，得到 %v", data["sitekey"])
	}
	if data["cdata"] != fmt.Sprintf("%d", appealID) {
		t.Errorf("cdata 应是申诉单 id，得到 %v", data["cdata"])
	}
	if days, _ := data["days"].(float64); days <= 0 {
		t.Errorf("数据应带保留天数，得到 %v", data["days"])
	}

	// 超过 24 小时：410 + 中文错误（前端渲染失效页）。
	if _, err := b.Store.Write.Exec(`UPDATE appeals SET web_since=? WHERE id=?`,
		now-25*3600, appealID); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path+"?json=1", nil))
	if w.Code != http.StatusGone {
		t.Errorf("过期链接应 410，得到 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "失效") {
		t.Errorf("410 响应应说明链接已失效：%s", w.Body.String())
	}

	// 签名不对：404。
	bad := fmt.Sprintf("/_w/ap/%d/%s", appealID, strings.Repeat("0", 32))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, bad+"?json=1", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("坏签名应 404，得到 %d", w.Code)
	}
}

// TestAppealPageShowsEvidence：验证页数据要包含「为什么被限制 + 账号信息 +
// 发言留底 + AI 复核结论」。只写「完成验证拿解禁码」时，用户既不知道
// 为什么被罚、也不知道该改什么，只能盲点一遍。
//
// JSON 层保留原文（不转义），转义由 React 渲染负责。
func TestAppealPageShowsEvidence(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	b.Cfg.PublicURL = "https://ad.example.com"
	b.Cfg.TurnstileSiteKey, b.Cfg.TurnstileSecret = "site", "secret"
	if err := EnsureWebSecret(b.Shared); err != nil {
		t.Fatal(err)
	}
	testutil.EnableAntiad(t, b, -100)
	now := time.Now().Unix()

	// 一条处罚流水（含判定时的昵称、理由与原文）。
	if _, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(bot_id,chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,created_at,user_name,lifted_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,0)`,
		b.BotID(), -100, 555, 1, "加微信买号", "ad", 0.95, "so", "scam",
		"deleted_muted", "昵称里写着日入5000", now-60, "张三 (@zs)"); err != nil {
		t.Fatal(err)
	}
	// 两条发言留底（其中一条带脚本，JSON 原样保留、由前端转义）。
	for i, text := range []string{"加微信买号", "日入过万 <script>alert(1)</script>"} {
		if _, err := b.Store.Write.Exec(`INSERT INTO group_messages
			(chat_id,message_id,user_id,text,at) VALUES (-100,?,555,?,?)`,
			i+1, text, now-120+int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	// 申诉单：web 状态 + AI 维持原判 + 申诉理由。
	res, err := b.Store.Write.Exec(`INSERT INTO appeals
		(bot_id,user_id,status,statement,ai_result,ai_conf,ai_reason,ai_model,
		 web_since,created_at,updated_at)
		VALUES (?,?,'web','我改资料了','uphold',0.92,'资料仍写着推广','m1',?,?,?)`,
		b.BotID(), 555, now, now, now)
	if err != nil {
		t.Fatal(err)
	}
	appealID, _ := res.LastInsertId()
	path := fmt.Sprintf("/_w/ap/%d/%s", appealID, appealSig(b.Shared, appealID, 555))

	w := httptest.NewRecorder()
	WebHandler(b.Shared).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path+"?json=1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("验证页应 200，得到 %d", w.Code)
	}
	var data struct {
		Account struct {
			UID  int64  `json:"uid"`
			Name string `json:"name"`
		} `json:"account"`
		Limits []struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		} `json:"limits"`
		Messages []struct {
			Text string `json:"text"`
		} `json:"messages"`
		AI struct {
			Label     string  `json:"label"`
			Conf      float64 `json:"conf"`
			Reason    string  `json:"reason"`
			Statement string  `json:"statement"`
		} `json:"ai"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatalf("响应应是 JSON：%v（%s）", err, w.Body.String())
	}
	if data.Account.UID != 555 || !strings.Contains(data.Account.Name, "张三 (@zs)") {
		t.Errorf("账号信息不对：%+v", data.Account)
	}
	if len(data.Limits) == 0 || !strings.Contains(data.Limits[0].Reason, "昵称里写着日入5000") {
		t.Errorf("为什么被限制应给出理由：%+v", data.Limits)
	}
	if len(data.Messages) != 2 {
		t.Fatalf("应带两条发言留底：%+v", data.Messages)
	}
	foundRaw := false
	for _, m := range data.Messages {
		if m.Text == "日入过万 <script>alert(1)</script>" {
			foundRaw = true
		}
	}
	if !foundRaw {
		t.Errorf("JSON 层应原样保留用户文本（前端负责转义）：%+v", data.Messages)
	}
	if !strings.Contains(data.AI.Label, "维持原判") ||
		!strings.Contains(data.AI.Reason, "资料仍写着推广") ||
		!strings.Contains(data.AI.Statement, "我改资料了") {
		t.Errorf("AI 复核结论不对：%+v", data.AI)
	}
}
