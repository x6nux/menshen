package main

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"menshen/internal/config"
	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/tg"
)

func TestTokenFromPath(t *testing.T) {
	cases := []struct{ path, want string }{
		// 本项目的默认形态：<public_url 的路径部分>/<token>/webhook
		{"/svc/" + testutil.TestToken + "/webhook", testutil.TestToken},
		{"/" + testutil.TestToken + "/webhook", testutil.TestToken},
		// 反代再套几层子路径也要认得出来
		{"/a/b/c/" + testutil.TestToken + "/webhook", testutil.TestToken},
		// TG 官方形态，以及它挂在子路径下的样子
		{"/bot" + testutil.TestToken, testutil.TestToken},
		{"/svc/tg/bot" + testutil.TestToken, testutil.TestToken},
		{"/healthz", ""},
		{"/", ""},
		{"/bot", ""},
		// 长得像但格式不合法的，一律不认
		{"/123:short/webhook", ""},
		{"/svc/webhook", ""},
	}
	for _, c := range cases {
		if got := core.TokenFromPath(c.path); got != c.want {
			t.Errorf("core.TokenFromPath(%q) = %q, 期望 %q", c.path, got, c.want)
		}
	}
}

// TestWebhookURLRoundTrip 确认我们发给 Telegram 的回调地址，自己解析得回来。
//
// 这两头分别在 config.go 和 webhook.go 里，改任何一边而忘了另一边，
// 表现都是「setWebhook 成功但一条更新都收不到」—— 没有任何报错。
func TestWebhookURLRoundTrip(t *testing.T) {
	for _, base := range []string{
		"https://ad.example.com",
		"https://ad.example.com/",   // 结尾多一条斜杠
		"https://ad.example.com/tg", // 带子路径
		"https://ad.example.com/a/b/c",
	} {
		cfg := &config.Config{PublicURL: base}
		if !cfg.UseWebhook() {
			t.Errorf("填了 public_url=%q 却没进 webhook 模式", base)
		}
		u := cfg.WebhookURLFor(testutil.TestToken)
		if !strings.HasSuffix(u, "/webhook") {
			t.Errorf("回调地址 %q 不是 <前缀>/<token>/webhook 的形态", u)
		}
		if strings.Contains(u, "//"+testutil.TestToken) || strings.Contains(u, testutil.TestToken+"//") {
			t.Errorf("回调地址里有多余的斜杠: %q", u)
		}
		// 去掉 scheme 里那个 "//" 之后再解析，模拟服务端拿到的 URL.Path
		path := u[strings.Index(u, "//")+2:]
		path = path[strings.Index(path, "/"):]
		if got := core.TokenFromPath(path); got != testutil.TestToken {
			t.Errorf("自己发出的地址 %q 解析出 %q，期望原 token", u, config.MaskToken(got))
		}
	}

	if (&config.Config{}).UseWebhook() {
		t.Error("没填 public_url 时不该进 webhook 模式")
	}
}

// TestValidToken 守的是 webhook 的入口：格式不设门槛的话，任何人往
// /bot<随便什么> 发一次 POST 就能让本进程凭空多一个实例。
func TestValidToken(t *testing.T) {
	good := []string{testutil.TestToken, "1234567890:" + strings.Repeat("a", 35)}
	bad := []string{
		"", "abc", "notanumber:AAEEfaketoken_ForUnitTestsOnly1234",
		"123:short", "123456789:with space in it aaaaaaaaaaaaaaaaaaaa",
		"1234:" + strings.Repeat("a", 35), // bot_id 不足 5 位
		"../../etc/passwd",
	}
	for _, tk := range good {
		if !config.ValidToken(tk) {
			t.Errorf("config.ValidToken(%q) 应为 true", config.MaskToken(tk))
		}
	}
	for _, tk := range bad {
		if config.ValidToken(tk) {
			t.Errorf("config.ValidToken(%q) 应为 false", tk)
		}
	}
}

// TestMaskToken 确认日志脱敏：token 等同于 bot 的完整控制权。
func TestMaskToken(t *testing.T) {
	got := config.MaskToken(testutil.TestToken)
	if strings.Contains(got, "faketoken") {
		t.Errorf("脱敏后仍含密钥部分: %q", got)
	}
	if !strings.HasPrefix(got, "123456789:") {
		t.Errorf("应保留 bot_id 以便排障, 得到 %q", got)
	}
}

// TestWebhookDispatch 确认 <前缀>/<TOKEN>/webhook 的更新真的被那个 bot
// 处理了。观察点取群消息留底而不是队列本身：loadAll 已经把 worker 启起来
// 了，队列里的东西转瞬就被取走，只有走完整条分发链路才会留下痕迹。
func TestWebhookDispatch(t *testing.T) {
	reg, b := testutil.NewTestRegistry(t, dispatch)
	testutil.EnableAntiad(t, b, -100)

	body := `{"update_id":7,"message":{"message_id":1,"date":1700000000,
		"from":{"id":4242},"chat":{"id":-100,"type":"supergroup"},"text":"喵"}}`
	req := httptest.NewRequest(http.MethodPost,
		"/svc/"+testutil.TestToken+"/webhook", strings.NewReader(body))
	w := httptest.NewRecorder()
	reg.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", w.Code)
	}

	count := func() (n int) {
		b.Store.Read.QueryRow(`SELECT COUNT(*) FROM group_messages
			WHERE chat_id=-100 AND text='喵'`).Scan(&n)
		return n
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && count() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if n := count(); n != 1 {
		t.Errorf("更新未被处理，留底里有 %d 条", n)
	}
}

// TestWebhookDispatchesEditedMessage 确认编辑过的群消息也走进反广告：
// 分发里漏掉 edited_message 的话，「先发正常、再编辑成广告」畅通无阻，
// 而留底里仍是编辑前的样子，复查也看不见。
func TestWebhookDispatchesEditedMessage(t *testing.T) {
	reg, b := testutil.NewTestRegistry(t, dispatch)
	testutil.EnableAntiad(t, b, -100)

	for i, body := range []string{
		`{"update_id":7,"message":{"message_id":1,"date":1700000000,
			"from":{"id":4242},"chat":{"id":-100,"type":"supergroup"},"text":"喵"}}`,
		`{"update_id":8,"edited_message":{"message_id":1,"date":1700000000,"edit_date":1700000060,
			"from":{"id":4242},"chat":{"id":-100,"type":"supergroup"},"text":"改过了"}}`,
	} {
		w := httptest.NewRecorder()
		reg.ServeHTTP(w, httptest.NewRequest(http.MethodPost,
			"/svc/"+testutil.TestToken+"/webhook", strings.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("第 %d 条状态码 = %d", i, w.Code)
		}
	}

	text := func() (s string) {
		b.Store.Read.QueryRow(`SELECT text FROM group_messages
			WHERE chat_id=-100 AND message_id=1`).Scan(&s)
		return s
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && text() != "改过了" {
		time.Sleep(10 * time.Millisecond)
	}
	if got := text(); got != "改过了" {
		t.Errorf("编辑后的留底 = %q，edited_message 没有被分发", got)
	}
}

// TestWebhookRejects 覆盖三条拒绝路径。
func TestWebhookRejects(t *testing.T) {
	reg, _ := testutil.NewTestRegistry(t, dispatch)

	t.Run("非 POST", func(t *testing.T) {
		w := httptest.NewRecorder()
		reg.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/bot"+testutil.TestToken, nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("状态码 = %d, 期望 405", w.Code)
		}
	})

	t.Run("token 格式非法", func(t *testing.T) {
		w := httptest.NewRecorder()
		reg.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/botnope",
			strings.NewReader("{}")))
		if w.Code != http.StatusNotFound {
			t.Errorf("状态码 = %d, 期望 404", w.Code)
		}
		if strings.Contains(w.Body.String(), "nope") {
			t.Error("错误响应不得回显路径——它含 token")
		}
	})

	t.Run("请求体不是 JSON", func(t *testing.T) {
		w := httptest.NewRecorder()
		reg.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/bot"+testutil.TestToken,
			strings.NewReader("不是 json")))
		if w.Code != http.StatusBadRequest {
			t.Errorf("状态码 = %d, 期望 400", w.Code)
		}
	})
}

func TestWebhookHealthz(t *testing.T) {
	reg, _ := testutil.NewTestRegistry(t, dispatch)
	w := httptest.NewRecorder()
	reg.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK || w.Body.String() != "ok" {
		t.Errorf("healthz = %d %q", w.Code, w.Body.String())
	}
}

// TestWebhookUnknownTokenRejected 确认未登记的 token 直接被拒。
//
// 接入是注册制：没在 bots 表里登记过的 token，连 getMe 都不会发。
// 没有归属就谈不上谁能管它、谁为它的开销负责；顺带也堵死了「拿本进程
// 当跳板反复刷 TG」这条路 —— 拒绝发生在查表那一步，压根不出网。
func TestWebhookUnknownTokenRejected(t *testing.T) {
	reg, _ := testutil.NewTestRegistry(t, dispatch)
	before := reg.Size()

	other := "987654321:BBFFanotherfaketoken_ForTests98765432"
	for i := range 2 {
		w := httptest.NewRecorder()
		reg.ServeHTTP(w, httptest.NewRequest(http.MethodPost,
			"/svc/"+other+"/webhook", strings.NewReader("{}")))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("第 %d 次状态码 = %d, 期望 401", i+1, w.Code)
		}
	}
	if reg.Size() != before {
		t.Errorf("未登记的 token 不该建实例，实例数 %d → %d", before, reg.Size())
	}
}

// TestWebhookDisabledBotRejected 确认停用的 bot 收不到更新。
// 停用要真的停：只在面板上标个灰、后台照常处理消息，是最坏的一种。
func TestWebhookDisabledBotRejected(t *testing.T) {
	reg, _ := testutil.NewTestRegistry(t, dispatch)
	if err := reg.SetBotEnabled(testutil.TestBotID, false); err != nil {
		t.Fatalf("停用失败: %v", err)
	}

	w := httptest.NewRecorder()
	reg.ServeHTTP(w, httptest.NewRequest(http.MethodPost,
		"/svc/"+testutil.TestToken+"/webhook", strings.NewReader("{}")))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("状态码 = %d, 期望 401", w.Code)
	}
}

// TestEnqueueDropsWhenFull 确认队列满时丢弃而不是阻塞：
// 阻塞会让 TG 投递超时并重推，重推又落到同一个满队列上。
func TestEnqueueDropsWhenFull(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	for i := range core.UpdateQueueCap {
		if !b.Enqueue(&tg.Update{UpdateID: int64(i)}) {
			t.Fatalf("第 %d 条不该被丢弃", i)
		}
	}
	if b.Enqueue(&tg.Update{UpdateID: 9999}) {
		t.Error("队列已满时应返回 false")
	}
}

// TestQueueWorkerIsSerial 确认 worker 串行消费。
// 同一个人的更新有先后依赖：资历累计、留底，以及 recent_context 取的是
// 「这条之前」的留底。并发消费会让它们随调度乱序。
func TestQueueWorkerIsSerial(t *testing.T) {
	// 传真实的 dispatch：这个测试验证的就是「按到达顺序走完整条分发链」，
	// 换成空分发就只剩队列自己在转，留底永远是空的。
	b, _, _ := testutil.NewTestBotDispatch(t, 1, 1, dispatch)
	testutil.EnableAntiad(t, b, -100)

	stop := make(chan struct{})
	defer close(stop)
	go b.RunQueueWorker(stop)

	for i := 1; i <= 5; i++ {
		b.Enqueue(&tg.Update{UpdateID: int64(i),
			Message: testutil.GroupMsg(-100, 42, int64(i), "消息"+string(rune('０'+i)))})
	}

	// 按 rowid 排序：它就是插入顺序。五条消息的时间戳相同，分发链一旦
	// 乱序，落库顺序跟着乱，而 at / message_id 都看不出来。
	texts := func() []string {
		rows, err := b.Store.Read.Query(`SELECT text FROM group_messages
			WHERE chat_id=-100 ORDER BY rowid`)
		if err != nil {
			t.Fatalf("读留底失败: %v", err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			rows.Scan(&s)
			out = append(out, s)
		}
		return out
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(texts()) < 5 {
		time.Sleep(10 * time.Millisecond)
	}

	got := texts()
	if len(got) != 5 {
		t.Fatalf("留底里有 %d 条，期望 5 条", len(got))
	}
	for i, text := range got {
		want := "消息" + string(rune('０'+i+1))
		if text != want {
			t.Errorf("第 %d 条留底 = %q, 期望 %q（顺序必须与到达顺序一致）",
				i, text, want)
		}
	}
}

// TestPollAllowedUpdates 锁住那条从代码里看不出来的坑：
// 一旦显式声明 allowed_updates，Telegram 的默认清单就整体失效，
// 漏掉哪个哪个就彻底收不到，而 getUpdates/setWebhook 都不会报错。
func TestPollAllowedUpdates(t *testing.T) {
	list := core.PollAllowedUpdates()
	for _, must := range []string{"message", "edited_message", "callback_query", "chat_member", "my_chat_member"} {
		if !slices.Contains(list, must) {
			t.Errorf("allowed_updates 缺少 %q —— 该类更新会彻底收不到且无任何报错", must)
		}
	}
}

// TestSetWebhookPassesAllowedUpdates 确认 setWebhook 用的是同一份清单。
func TestSetWebhookPassesAllowedUpdates(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	if err := b.SetWebhook("https://example.com/bot" + testutil.TestToken); err != nil {
		t.Fatalf("setWebhook: %v", err)
	}

	p := fake.LastCall("setWebhook")
	if p == nil {
		t.Fatal("没有发出 setWebhook")
	}
	raw, ok := p["allowed_updates"].([]any)
	if !ok {
		t.Fatalf("allowed_updates 类型不对: %T", p["allowed_updates"])
	}
	var got []string
	for _, v := range raw {
		got = append(got, v.(string))
	}
	if !slices.Contains(got, "chat_member") {
		t.Error("setWebhook 的 allowed_updates 缺 chat_member —— " +
			"「新人/老人」分档会全部退化成年龄未知")
	}
}

// TestSetWebhookReportsTGFailure 确认 TG 的 ok:false 被当成失败。
// TG 的错误是 HTTP 200 + {"ok":false}，只看 err 会把业务失败当成功。
func TestSetWebhookReportsTGFailure(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	fake.Resp["setWebhook"] = `{"ok":false,"description":"bad webhook: HTTPS url must be provided"}`

	err := b.SetWebhook("http://example.com/x")
	if err == nil {
		t.Fatal("TG 返回 ok:false 时必须报错")
	}
	if !strings.Contains(err.Error(), "HTTPS") {
		t.Errorf("错误信息应带上 TG 的说明，得到 %q", err)
	}
}

// TestWebRouterRoutesWPaths：路径中任一段为 _w 的请求进网页处理器，
// 其余照旧进 webhook；反代套几层子路径同样能识别。
func TestWebRouterRoutesWPaths(t *testing.T) {
	reg, _ := testutil.NewTestRegistry(t, nil)
	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("web"))
	})
	mini := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("mini"))
	})
	router := webRouter{reg: reg, web: web, mini: mini}

	for _, p := range []string{"/_w/ap/1/x", "/tg/_w/v/2/y", "/a/b/_w/apv/3/z"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Body.String() != "web" {
			t.Errorf("%s 应进网页处理器，得到 %q", p, w.Body.String())
		}
	}

	// Mini App 走 /miniapp 前缀。
	for _, p := range []string{"/miniapp", "/miniapp/api/state"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Body.String() != "mini" {
			t.Errorf("%s 应进 Mini App 处理器，得到 %q", p, w.Body.String())
		}
	}

	// /healthz 与 webhook 路径不进网页处理器。
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Body.String() != "ok" {
		t.Errorf("/healthz 应由 webhook 处理器回应，得到 %q", w.Body.String())
	}
	if hasWebSegment("/ap/1/x") || hasWebSegment("/healthz") {
		t.Error("不含 _w 段的路径不该被当成网页")
	}
}
