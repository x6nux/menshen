package panel

import (
	"strings"
	"testing"

	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// 另一个格式合法但完全虚构的 token，用于「接入第二个 bot」的场景。
const testToken2 = "555555555:CCotherfaketoken_ForUnitTests5555555"

// TestPrecheckRejectsLocally 确认能在本地挡掉的都别去打 TG。
//
// precheck 的全部价值就是「不出网」：格式错、重复接入、超配额这三类
// 占了失败的绝大多数，先跑一次 getMe 再告诉他「你已经加过了」既慢又蠢。
func TestPrecheckRejectsLocally(t *testing.T) {
	reg, b := testutil.NewTestRegistry(t, dispatch)
	fake := b.TG.(*testutil.FakeTG)

	if err := reg.Precheck("不是token", 777); err == nil {
		t.Error("格式非法应当被拒")
	}
	if err := reg.Precheck(testutil.TestToken, 777); err == nil {
		t.Error("已接入的 token 应当被拒")
	}
	if n := fake.CountCalls("getMe"); n != 0 {
		t.Errorf("本地就能判的失败不该出网，实际发了 %d 次 getMe", n)
	}
}

// TestPrecheckRequiresWebhook 确认子 bot 只能走 webhook。
//
// 长轮询要为每个 bot 各开一条 getUpdates 长连接，而 Telegram 的限速
// 同时按 bot 和出口 IP 算，几个 bot 一起轮询会互相挤掉配额 —— 表现是
// 所有 bot 一起变慢、一起丢更新，且没有任何一条日志指向真正的原因。
func TestPrecheckRequiresWebhook(t *testing.T) {
	reg, b := testutil.NewTestRegistry(t, dispatch)
	b.Cfg.PublicURL = "" // 长轮询模式

	err := reg.Precheck(testToken2, 777)
	if err == nil {
		t.Fatal("长轮询模式下不该允许接入子 bot")
	}
	if !strings.Contains(err.Error(), "public_url") {
		t.Errorf("错误里应当说清怎么解决，得到 %q", err)
	}

	// 配置里那个主 bot 例外：它就是长轮询模式下唯一的那条通道，
	// 挡住它等于连主 bot 都起不来。
	b.Cfg.BotToken = testToken2
	if err := reg.Precheck(testToken2, 777); err != nil {
		t.Errorf("配置里的主 bot 不该被挡: %v", err)
	}

	// webhook 模式下一切照常
	b.Cfg.BotToken = testutil.TestToken
	b.Cfg.PublicURL = "https://ad.example.com"
	if err := reg.Precheck(testToken2, 777); err != nil {
		t.Errorf("webhook 模式下应当放行: %v", err)
	}
}

// TestLoadAllSkipsSubBotsInPolling 确认长轮询模式下不给子 bot 建实例。
//
// 建了也不会去轮询它 —— 只是让面板显示它在运行，而它一条更新都收不到。
// 记录留在表里不动：切回 webhook 模式后它们应当自动恢复。
func TestLoadAllSkipsSubBotsInPolling(t *testing.T) {
	reg, b := testutil.NewTestRegistry(t, dispatch)
	b.Cfg.PublicURL = "" // 长轮询模式
	testutil.RegisterTestBot(t, b.Shared, testToken2, 4343, 777)

	reg.LoadAll()
	if _, ok := reg.LookupID(4343); ok {
		t.Error("长轮询模式下不该给子 bot 建实例")
	}
	if _, ok := reg.LookupToken(testutil.TestToken); !ok {
		t.Error("主 bot 必须照常运行")
	}

	// 切到 webhook 模式后应当自动补上
	b.Cfg.PublicURL = "https://ad.example.com"
	reg.LoadAll()
	if _, ok := reg.LookupID(4343); !ok {
		t.Error("切到 webhook 模式后子 bot 应当被拉起来")
	}
}

// TestPrecheckQuota 确认配额对次管生效、对主管不生效。
func TestPrecheckQuota(t *testing.T) {
	reg, b := testutil.NewTestRegistry(t, dispatch) // 主管是 777
	if err := b.AddAdmin(200, "次管", 777); err != nil {
		t.Fatalf("addAdmin: %v", err)
	}
	if err := b.PutSetting("max_bots_per_admin", "1"); err != nil {
		t.Fatalf("putSetting: %v", err)
	}
	// 次管 200 名下先占掉一个
	testutil.RegisterTestBot(t, b.Shared, testToken2, 4343, 200)

	if err := reg.Precheck("111222333:DDthirdfaketoken_ForUnitTests1112", 200); err == nil {
		t.Error("次管超配额应当被拒")
	}
	if err := reg.Precheck("111222333:DDthirdfaketoken_ForUnitTests1112", 777); err != nil {
		t.Errorf("主管理员不该受配额限制: %v", err)
	}
}

// TestRegisterProbeFailureLeavesNothing 确认验真失败不留半个 bot。
//
// 落了库却没有实例，表现是面板上躺着一个「已接入」、名下的群却永远
// 收不到任何更新 —— 排查时最难想到的方向。
func TestRegisterProbeFailureLeavesNothing(t *testing.T) {
	reg, b := testutil.NewTestRegistry(t, dispatch)
	fake := b.TG.(*testutil.FakeTG)
	fake.Resp["getMe"] = `{"ok":false,"description":"Unauthorized"}`
	before := reg.Size()

	rec, err := reg.Register(testToken2, 777)
	if err == nil {
		t.Fatal("验真失败时应当报错")
	}
	if rec != nil {
		t.Error("验真失败时不该返回记录")
	}
	if !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("错误里应当带上 TG 的说法，得到 %q", err)
	}
	if reg.Size() != before {
		t.Errorf("不该建实例，实例数 %d → %d", before, reg.Size())
	}
	var n int
	b.Store.Read.QueryRow(`SELECT COUNT(*) FROM bots WHERE token=?`,
		testToken2).Scan(&n)
	if n != 0 {
		t.Error("验真失败的 bot 不该落库")
	}
}

// TestFinishSetupRegistersWebhook 确认接入后自动挂上回调地址。
func TestFinishSetupRegistersWebhook(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	fake := b.TG.(*testutil.FakeTG)
	b.Cfg.PublicURL = "https://ad.example.com/tg"

	if err := b.FinishSetup(); err != nil {
		t.Fatalf("finishSetup: %v", err)
	}
	p := fake.LastCall("setWebhook")
	if p == nil {
		t.Fatal("没有发出 setWebhook")
	}
	want := "https://ad.example.com/tg/" + testutil.TestToken + "/webhook"
	if p["url"] != want {
		t.Errorf("回调地址 = %v, 期望 %q", p["url"], want)
	}
	if fake.CountCalls("setMyCommands") == 0 {
		t.Error("应当同时注册命令菜单")
	}
}

// TestFinishSetupSkipsWebhookInPolling 确认轮询模式下不设 webhook。
// 设了的话 getUpdates 会一直拿 409，表现为「一条消息都收不到」。
func TestFinishSetupSkipsWebhookInPolling(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	fake := b.TG.(*testutil.FakeTG)
	b.Cfg.PublicURL = "" // 轮询模式

	if err := b.FinishSetup(); err != nil {
		t.Fatalf("finishSetup: %v", err)
	}
	if n := fake.CountCalls("setWebhook"); n != 0 {
		t.Errorf("轮询模式下发了 %d 次 setWebhook", n)
	}
}

// TestAddBotAndReportSucceeds 走一遍完整的接入流程。
func TestAddBotAndReportSucceeds(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	fake := b.TG.(*testutil.FakeTG)
	b.Cfg.PublicURL = "https://ad.example.com"
	// 第二个 bot 的身份
	fake.Resp["getMe"] = `{"ok":true,"result":{"id":4343,"username":"newbot"}}`

	addBotAndReport(b, 777, 777, testToken2, 55)

	if _, ok := b.Reg.LookupID(4343); !ok {
		t.Error("接入后应当有实例")
	}
	if fake.CountCalls("setWebhook") == 0 {
		t.Error("接入后应当自动注册 webhook")
	}
	// 进度消息被编辑成最终结果，而不是另发一条
	p := fake.LastCall("editMessageText")
	if p == nil {
		t.Fatal("没有把进度消息编辑成结果")
	}
	if int64(p["message_id"].(float64)) != 55 {
		t.Errorf("编辑的不是那条进度消息: %v", p["message_id"])
	}
	text, _ := p["text"].(string)
	if !strings.Contains(text, "newbot") {
		t.Errorf("结果里应当带上 bot 的身份，得到 %q", text)
	}
	if strings.Contains(text, "CCotherfaketoken") {
		t.Error("结果消息里泄露了 token")
	}
}

// TestAddBotAndReportProbeFailure 确认失败时会续上会话让他重发。
//
// 接入失败最常见的原因是 token 复制少了几个字符。让他重新点一遍按钮
// 才能再试，是在惩罚一个手滑。
func TestAddBotAndReportProbeFailure(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	fake := b.TG.(*testutil.FakeTG)
	fake.Resp["getMe"] = `{"ok":false,"description":"Unauthorized"}`

	addBotAndReport(b, 777, 777, testToken2, 55)

	if _, ok := b.TakePending(777); !ok {
		t.Error("失败后应当续上输入会话，让他直接重发")
	}
	p := fake.LastCall("editMessageText")
	if p == nil {
		t.Fatal("应当把进度消息编辑成失败说明")
	}
	if text, _ := p["text"].(string); !strings.Contains(text, "Unauthorized") {
		t.Errorf("应当说明失败原因，得到 %q", text)
	}
}

// TestBotAddDeletesTokenMessage 确认含 token 的那条消息被自动删掉。
//
// token 等同于该 bot 的完整控制权。指望用户自己记得删是不现实的，
// 而它会一直躺在聊天记录里 —— 换设备登录、被人借手机看一眼都够了。
func TestBotAddDeletesTokenMessage(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	fake := b.TG.(*testutil.FakeTG)

	// 刻意用一个**接不进来**的 token（已注册过）：那条消息照样得删。
	// 删除必须排在一切校验之前 —— 按「成功了才删」写的话，恰好是失败
	// 那几次把 token 留在了聊天记录里。
	m := &tg.Message{MessageID: 33, From: &tg.TGUser{ID: 777},
		Chat: &tg.Chat{ID: 777, Type: "private"}, Text: testutil.TestToken}
	handleSettingsInput(b, m, core.PendingInput{Op: "bot_add"}, testutil.TestToken)

	p := fake.LastCall("deleteMessage")
	if p == nil {
		t.Fatal("没有删掉含 token 的那条消息")
	}
	if int64(p["message_id"].(float64)) != 33 {
		t.Errorf("删错了消息: %v", p["message_id"])
	}
}

// TestBotAddRejectsEarly 确认本地就能判的失败不会走到「正在验证」那一步。
func TestBotAddRejectsEarly(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	fake := b.TG.(*testutil.FakeTG)

	m := &tg.Message{MessageID: 33, From: &tg.TGUser{ID: 777},
		Chat: &tg.Chat{ID: 777, Type: "private"}, Text: "乱敲的"}
	handleSettingsInput(b, m, core.PendingInput{Op: "bot_add"}, "乱敲的")

	if n := fake.CountCalls("getMe"); n != 0 {
		t.Errorf("格式就不对的 token 不该出网，发了 %d 次 getMe", n)
	}
	if _, ok := b.TakePending(777); !ok {
		t.Error("格式错时应当保留会话让他重发")
	}
}
