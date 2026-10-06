package antiad

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// enableJoinVerify 打开入群验证并配好提供方（默认 Turnstile）。
func enableJoinVerify(t *testing.T, b *core.Bot) {
	t.Helper()
	if err := b.PutSetting("antiad_joinverify", "1"); err != nil {
		t.Fatalf("打开入群验证失败: %v", err)
	}
	// 提供方与密钥存 settings（网页面板配置），不再是 config.yaml。
	for k, v := range map[string]string{
		"captcha_provider": "turnstile", "captcha_site_key": "site",
		"captcha_secret": "secret"} {
		if err := b.PutSetting(k, v); err != nil {
			t.Fatalf("putSetting %s: %v", k, err)
		}
	}
	b.Cfg.PublicURL = "https://ad.example.com"
	if err := EnsureWebSecret(b.Shared); err != nil {
		t.Fatalf("EnsureWebSecret: %v", err)
	}
}

// insertJoinVerify 直接落一张待验证单，返回 id。
func insertJoinVerify(t *testing.T, b *core.Bot, chatID, uid, createdAt int64) int64 {
	t.Helper()
	res, err := b.Store.Write.Exec(`INSERT INTO join_verify
		(bot_id,chat_id,user_id,status,created_at) VALUES (?,?,?,?,?)`,
		b.BotID(), chatID, uid, jvPending, createdAt)
	if err != nil {
		t.Fatalf("插入验证单失败: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func joinVerifyStatus(t *testing.T, b *core.Bot, id int64) string {
	t.Helper()
	var st string
	if err := b.Store.Read.QueryRow(
		`SELECT status FROM join_verify WHERE id=?`, id).Scan(&st); err != nil {
		t.Fatalf("读验证单状态失败: %v", err)
	}
	return st
}

// ---- 提供方校验 ----

func TestCaptchaProviders(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	b.Cfg.PublicURL = "https://ad.example.com"

	oldH := hcaptchaVerifyURL
	t.Cleanup(func() { hcaptchaVerifyURL = oldH })

	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		form = r.PostForm
		w.Write([]byte(`{"success":true,"hostname":"ad.example.com","score":0.9}`))
	}))
	t.Cleanup(srv.Close)
	hcaptchaVerifyURL = srv.URL

	hc := captchaProvider{name: "hcaptcha", siteKey: "hk", secret: "hsec"}
	if ok, why := hc.verify(b.Shared, "tok", "203.0.113.7"); !ok {
		t.Fatalf("hCaptcha 应通过: %s", why)
	}
	if form["secret"][0] != "hsec" || form["response"][0] != "tok" ||
		form["remoteip"][0] != "203.0.113.7" || form["sitekey"][0] != "hk" {
		t.Errorf("hCaptcha 请求字段不对: %v", form)
	}
	hc.minScore = 95
	if ok, _ := hc.verify(b.Shared, "tok", "1.2.3.4"); ok {
		t.Error("分数 0.9 低于下限 95 时应判失败")
	}
	hc.minScore = 0

	// hostname：hCaptcha 对子域页面返回可注册域（如 free.edu.kg），
	// 高峰期还可能返回 "not-provided"，所以相等/父域/缺省都算过；
	// 无关域名与「兄弟子域」仍然拒绝。
	hostCases := []struct {
		hostname string
		ok       bool
	}{
		{"ad.example.com", true},      // 完整主机名
		{"example.com", true},         // 父域（可注册域）
		{"EXAMPLE.COM", true},         // 大小写不敏感
		{"not-provided", true},        // hCaptcha 高峰期的占位值
		{"", true},                    // 对端没给
		{"evil.com", false},           // 无关域
		{"vil.example.com", false},    // 兄弟子域
		{"sub.ad.example.com", false}, // 本站不是它的子域
	}
	for _, c := range hostCases {
		srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"success":true,"hostname":"%s"}`, c.hostname)
		})
		if ok, _ := hc.verify(b.Shared, "tok", "1.2.3.4"); ok != c.ok {
			t.Errorf("hostname %q: 期望 ok=%v", c.hostname, c.ok)
		}
	}

	// 空令牌不发请求直接失败。
	if ok, _ := hc.verify(b.Shared, "", "1.2.3.4"); ok {
		t.Error("缺令牌应判失败")
	}
}

// TestVerifyTurnstileTokenHostname：入群验证用的 Turnstile 只核对
// success 与 hostname，不要求 action / cdata（那是申诉页的约束）。
func TestVerifyTurnstileTokenHostname(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	b.Cfg.PublicURL = "https://ad.example.com"
	if err := b.PutSetting("captcha_secret", "secret"); err != nil {
		t.Fatal(err)
	}

	old := turnstileVerifyURL
	t.Cleanup(func() { turnstileVerifyURL = old })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"hostname":"ad.example.com"}`))
	}))
	t.Cleanup(srv.Close)
	turnstileVerifyURL = srv.URL

	if ok, why := verifyTurnstileToken(b.Shared, b.Cache.Snap().Setting("captcha_secret"), "tok", "1.2.3.4"); !ok {
		t.Fatalf("只有 success+hostname 时应通过: %s", why)
	}
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"hostname":"evil.example.com"}`))
	})
	if ok, _ := verifyTurnstileToken(b.Shared, b.Cache.Snap().Setting("captcha_secret"), "tok", "1.2.3.4"); ok {
		t.Error("hostname 不符应判失败")
	}
}

// ---- 进群门槛 ----

func TestStartJoinVerifyMutesAndLinks(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	enableJoinVerify(t, b)
	conf := testutil.ChatConfOf(t, b, -100)

	onJoin(b, conf, &tg.TGUser{ID: 777, FirstName: "新人"}, time.Now().Unix())
	waitIdle(t, b)

	calls := fake.Calls("restrictChatMember")
	if len(calls) == 0 {
		t.Fatal("应调用 restrictChatMember 禁言")
	}
	if perms, _ := calls[0]["permissions"].(map[string]any); perms["can_send_messages"] != false {
		t.Errorf("进群应先禁言，得到权限 %v", perms)
	}
	if _, ok := pendingJoinVerify(b.Store, -100, 777); !ok {
		t.Error("应落一张待验证单")
	}
	msg := fake.LastCall("sendMessage")
	if msg == nil {
		t.Fatal("应在群里发验证链接")
	}
	if text, _ := msg["text"].(string); !strings.Contains(text, "人机验证") {
		t.Errorf("提示文案应说明人机验证，得到 %q", text)
	}
	if !strings.Contains(fmt.Sprint(msg["reply_markup"]), "/_w/jv/") {
		t.Errorf("提示应带验证链接按钮，得到 %v", msg["reply_markup"])
	}
}

func TestJoinVerifySkipsWhenGroupSilent(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	enableJoinVerify(t, b)
	if err := b.PutSetting("antiad_group_silent", "1"); err != nil {
		t.Fatal(err)
	}
	conf := testutil.ChatConfOf(t, b, -100)

	onJoin(b, conf, &tg.TGUser{ID: 777, FirstName: "新人"}, time.Now().Unix())
	waitIdle(t, b)

	if n := fake.CountCalls("restrictChatMember"); n != 0 {
		t.Errorf("群内静默时不该禁言（链接送不出去），restrictChatMember 调用 %d 次", n)
	}
	if _, ok := pendingJoinVerify(b.Store, -100, 777); ok {
		t.Error("群内静默时不该落待验证单")
	}
}

func TestJoinVerifyDisabledByDefault(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	// 不打开 antiad_joinverify。
	conf := testutil.ChatConfOf(t, b, -100)

	onJoin(b, conf, &tg.TGUser{ID: 777, FirstName: "新人"}, time.Now().Unix())
	waitIdle(t, b)

	if n := fake.CountCalls("restrictChatMember"); n != 0 {
		t.Errorf("未开启时不该禁言，调用 %d 次", n)
	}
}

// TestJoinVerifyPagePass：通过验证 → 解除禁言、标记通过、撤回提示。
func TestJoinVerifyPagePass(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	enableJoinVerify(t, b)
	withRegistry(t, b)
	id := insertJoinVerify(t, b, -100, 777, time.Now().Unix())
	// 记一条群内提示消息号，验证通过后应撤回。
	b.Store.Write.Exec(`UPDATE join_verify SET notice_msg=555 WHERE id=?`, id)

	old := turnstileVerifyURL
	t.Cleanup(func() { turnstileVerifyURL = old })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"hostname":"ad.example.com"}`))
	}))
	t.Cleanup(srv.Close)
	turnstileVerifyURL = srv.URL

	body, _ := json.Marshal(map[string]any{"token": "tok", "signals": map[string]any{
		"platform": "Win32", "languages": []string{"zh-CN"},
		"screen": map[string]any{"w": 1920, "h": 1080, "depth": 24, "dpr": 2}}})
	path := fmt.Sprintf("/_w/jv/%d/%s", id, joinVerifySig(b.Shared, id, 777))
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	req.RemoteAddr = "203.0.113.7:9999"
	w := httptest.NewRecorder()
	WebHandler(b.Shared).ServeHTTP(w, req)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("应通过，得到 %d：%s", w.Code, w.Body.String())
	}
	if st := joinVerifyStatus(t, b, id); st != jvVerified {
		t.Errorf("验证单应标记 verified，得到 %q", st)
	}
	// 解除禁言：restrictChatMember 带 can_send_messages=true。
	unmuted := false
	for _, c := range fake.Calls("restrictChatMember") {
		if perms, _ := c["permissions"].(map[string]any); perms["can_send_messages"] == true {
			unmuted = true
		}
	}
	if !unmuted {
		t.Error("通过后应解除禁言")
	}
	if fake.LastCall("deleteMessage") == nil {
		t.Error("通过后应撤回群内验证提示")
	}
}

// TestJoinVerifyPageExpired：超出窗口的提交返回 410。
func TestJoinVerifyPageExpired(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	enableJoinVerify(t, b)
	withRegistry(t, b)
	// 默认窗口 10 分钟，造一条 11 分钟前的。
	id := insertJoinVerify(t, b, -100, 777, time.Now().Unix()-11*60)

	body, _ := json.Marshal(map[string]any{"token": "tok", "signals": map[string]any{}})
	path := fmt.Sprintf("/_w/jv/%d/%s", id, joinVerifySig(b.Shared, id, 777))
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	req.RemoteAddr = "203.0.113.7:9999"
	w := httptest.NewRecorder()
	WebHandler(b.Shared).ServeHTTP(w, req)

	if w.Code != http.StatusGone {
		t.Fatalf("过期提交应 410，得到 %d：%s", w.Code, w.Body.String())
	}
}

// TestJoinVerifyPageBadSig：签名不对返回 404。
func TestJoinVerifyPageBadSig(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	enableJoinVerify(t, b)
	withRegistry(t, b)
	id := insertJoinVerify(t, b, -100, 777, time.Now().Unix())

	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/_w/jv/%d/deadbeef", id), nil)
	w := httptest.NewRecorder()
	WebHandler(b.Shared).ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("错误签名应 404，得到 %d", w.Code)
	}
}

// TestJoinVerifyKeepsProfileMute：账号还背着资料类限制时，验证通过也不
// 解除禁言 —— 否则门口验证会变成绕过画像限制的后门。
func TestJoinVerifyKeepsProfileMute(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	enableJoinVerify(t, b)
	withRegistry(t, b)
	saveJoinMute(b, -100, 777, kindProfile, "简介里有联系方式", 0)
	id := insertJoinVerify(t, b, -100, 777, time.Now().Unix())

	old := turnstileVerifyURL
	t.Cleanup(func() { turnstileVerifyURL = old })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"hostname":"ad.example.com"}`))
	}))
	t.Cleanup(srv.Close)
	turnstileVerifyURL = srv.URL

	fake.Reset()
	body, _ := json.Marshal(map[string]any{"token": "tok", "signals": map[string]any{}})
	path := fmt.Sprintf("/_w/jv/%d/%s", id, joinVerifySig(b.Shared, id, 777))
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	req.RemoteAddr = "203.0.113.7:9999"
	w := httptest.NewRecorder()
	WebHandler(b.Shared).ServeHTTP(w, req)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("验证本身应通过，得到 %d：%s", w.Code, w.Body.String())
	}
	for _, c := range fake.Calls("restrictChatMember") {
		if perms, _ := c["permissions"].(map[string]any); perms["can_send_messages"] == true {
			t.Error("背着资料限制时不该解除禁言")
		}
	}
	if !strings.Contains(w.Body.String(), "资料") {
		t.Errorf("应提示资料限制仍在，得到 %s", w.Body.String())
	}
}

// TestSweepJoinVerifiesKicksExpired：超时未验证 → 踢出（封禁+解封）并结案。
func TestSweepJoinVerifiesKicksExpired(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	enableJoinVerify(t, b)
	id := insertJoinVerify(t, b, -100, 777, time.Now().Unix()-11*60)

	SweepJoinVerifies(b, time.Now())

	if fake.CountCalls("banChatMember") == 0 {
		t.Error("超时应封禁（踢出）")
	}
	last := fake.LastCall("unbanChatMember")
	if last == nil || last["only_if_banned"] != true {
		t.Errorf("踢出后应立即解封以便重进，得到 %v", last)
	}
	if st := joinVerifyStatus(t, b, id); st != jvExpired {
		t.Errorf("应标记 expired，得到 %q", st)
	}
}

// TestSweepJoinVerifiesKeepsPending：未到窗口的待验证单不动。
func TestSweepJoinVerifiesKeepsPending(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	enableJoinVerify(t, b)
	id := insertJoinVerify(t, b, -100, 777, time.Now().Unix())

	SweepJoinVerifies(b, time.Now())

	if n := fake.CountCalls("banChatMember"); n != 0 {
		t.Errorf("未到窗口不该踢人，banChatMember 调用 %d 次", n)
	}
	if st := joinVerifyStatus(t, b, id); st != jvPending {
		t.Errorf("应保持 pending，得到 %q", st)
	}
}

// TestJoinVerifyPagePath：jv 走公开页面壳。
func TestJoinVerifyPagePath(t *testing.T) {
	if !IsWebPagePath("/x/_w/jv/7/abc") {
		t.Error("_w/jv 应被认成公开页面路径")
	}
	if IsWebPagePath("/_w/nope/1/x") {
		t.Error("未知 kind 不该被认成公开页面路径")
	}
}

// TestActiveMuteSeesPendingVerify：验证未完成期间的禁言被外部解除时，
// activeMute 要能认出它（事件驱动的重新施加靠这个）。
func TestActiveMuteSeesPendingVerify(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	insertJoinVerify(t, b, -100, 777, time.Now().Unix())

	_, why, ok := activeMute(b, -100, 777)
	if !ok || !strings.Contains(why, "验证") {
		t.Errorf("应把待验证禁言当成未解除的限制，得到 ok=%v why=%q", ok, why)
	}
}
