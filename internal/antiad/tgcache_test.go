package antiad

import (
	"errors"
	"log/slog"
	"strings"
	"testing"

	"menshen/internal/logbuf"
	"menshen/internal/testutil"
)

// adminRequiredErr 造一个非管理员 bot 问成员状态时 TG 的拒答。
func adminRequiredErr() error {
	return testutil.TGNotFound("getChatMember", "Bad Request: CHAT_ADMIN_REQUIRED")
}

// warnMessages 接住 slog 的警告，返回读已收集消息的函数。
// slog 默认 logger 是进程级的，测完恢复，避免污染同包其他测试。
func warnMessages(t *testing.T) func() []string {
	t.Helper()
	prev := slog.Default()
	buf := logbuf.New(50)
	slog.SetDefault(slog.New(logbuf.NewHandler(buf, nil, slog.LevelWarn)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() []string {
		recs := buf.Snapshot()
		out := make([]string, 0, len(recs))
		for _, r := range recs {
			out = append(out, r.Message)
		}
		return out
	}
}

func containsMsg(msgs []string, sub string) bool {
	for _, m := range msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

// TestIsChatAdminPicksBotWithRights：同一群里挂着两个 bot，处理更新的那个
// 不是管理员时，改用群里问得动成员状态的那个 bot 查询。
//
// getChatMember 只有群管理员发得动，而发起查询的 bot 未必是：主 bot 不入群，
// 同群多 bot 时也只提了其中一个。查成员身份失败会把群管按普通成员处理，
// 等于让群管发广告时没人拦得住。
func TestIsChatAdminPicksBotWithRights(t *testing.T) {
	reg, primary := testutil.NewTestRegistry(t, nil)
	sh := primary.Shared
	first, firstTG := testutil.AddRegistryBot(t, reg, sh, 4343, 777)
	second, secondTG := testutil.AddRegistryBot(t, reg, sh, 4344, 777)
	testutil.EnableAntiad(t, first, -100)
	testutil.EnableAntiad(t, second, -100)
	firstTG.Err["getChatMember"] = adminRequiredErr()
	secondTG.Resp["getChatMember"] = `{"ok":true,"result":{"status":"administrator"}}`

	if !IsChatAdmin(first, -100, 42) {
		t.Fatal("同群另有管理员 bot，应查得出来")
	}
	if n := firstTG.CountCalls("getChatMember"); n != 1 {
		t.Errorf("非管理员的 bot 只该被问一次，实际 %d 次", n)
	}
	if n := secondTG.CountCalls("getChatMember"); n != 1 {
		t.Errorf("管理员的 bot 应被问一次，实际 %d 次", n)
	}

	// 查成的 bot 记下来给该群后续查询直接用：换个成员再查，不再先问那个
	// 已知问不动的 bot（每次都要等一次 TG 往返，而查询跑在串行的更新
	// 处理路径上）。
	if !IsChatAdmin(second, -100, 43) {
		t.Fatal("第二次查询也应成功")
	}
	if n := firstTG.CountCalls("getChatMember"); n != 1 {
		t.Errorf("已知问不动的 bot 不该再被问，实际 %d 次", n)
	}
	if n := secondTG.CountCalls("getChatMember"); n != 2 {
		t.Errorf("管理员的 bot 应被问两次，实际 %d 次", n)
	}
}

// TestIsChatAdminMainBotRoutesToWorker：面板回调常落在不入群的主 bot 上，
// 用它查群成员必然失败。查询要落到该群名下的工作 bot。
func TestIsChatAdminMainBotRoutesToWorker(t *testing.T) {
	reg, primary := testutil.NewTestRegistry(t, nil)
	sh := primary.Shared
	mainTG := primary.TG.(*testutil.FakeTG)
	testutil.RegisterMainTestBot(t, sh, testutil.TestToken, primary.BotID(), 777)
	reg.LoadAll()
	mainBot, ok := reg.LookupID(primary.BotID())
	if !ok || !mainBot.IsMainBot() {
		t.Fatal("主 bot 实例没能重建")
	}

	worker, workerTG := testutil.AddRegistryBot(t, reg, sh, 4343, 777)
	testutil.EnableAntiad(t, worker, -100)
	workerTG.Resp["getChatMember"] = `{"ok":true,"result":{"status":"administrator"}}`

	if !IsChatAdmin(mainBot, -100, 42) {
		t.Fatal("主 bot 应改用群里的工作 bot 查询")
	}
	if n := mainTG.CountCalls("getChatMember"); n != 0 {
		t.Errorf("主 bot 不入群，不该由它发出查询，实际 %d 次", n)
	}
	if n := workerTG.CountCalls("getChatMember"); n != 1 {
		t.Errorf("查询该由工作 bot 发出，实际 %d 次", n)
	}
}

// TestIsChatAdminChannelUsesBotInChat：频道身份的关联频道判断同样要落到
// 群里的 bot：看不到该群的 bot 只会得到 chat not found。
func TestIsChatAdminChannelUsesBotInChat(t *testing.T) {
	reg, primary := testutil.NewTestRegistry(t, nil)
	sh := primary.Shared
	first, firstTG := testutil.AddRegistryBot(t, reg, sh, 4343, 777)
	second, secondTG := testutil.AddRegistryBot(t, reg, sh, 4344, 777)
	testutil.EnableAntiad(t, first, -100)
	testutil.EnableAntiad(t, second, -100)
	firstTG.Err["getChat"] = testutil.TGNotFound("getChat", "Bad Request: chat not found")
	secondTG.Resp["getChat"] = `{"ok":true,"result":{"linked_chat_id":-1001234567890}}`

	if !IsChatAdmin(first, -100, -1001234567890) {
		t.Error("以关联频道身份发言的应识别为群主一方")
	}
	if n := firstTG.CountCalls("getChat"); n != 1 {
		t.Errorf("看不到该群的 bot 只该被问一次，实际 %d 次", n)
	}
	if n := secondTG.CountCalls("getChat"); n != 1 {
		t.Errorf("看得到该群的 bot 应被问一次，实际 %d 次", n)
	}
}

// TestIsChatAdminNoAdminBotWarns：群里的 bot 一个都不是管理员时，警告要说清
// 是配置问题（把任一 bot 提为管理员），而不是笼统的查询失败——后者看不出
// 该做什么，只会被当成 TG 抖动反复刷屏。
func TestIsChatAdminNoAdminBotWarns(t *testing.T) {
	reg, primary := testutil.NewTestRegistry(t, nil)
	sh := primary.Shared
	first, firstTG := testutil.AddRegistryBot(t, reg, sh, 4343, 777)
	second, secondTG := testutil.AddRegistryBot(t, reg, sh, 4344, 777)
	testutil.EnableAntiad(t, first, -100)
	testutil.EnableAntiad(t, second, -100)
	firstTG.Err["getChatMember"] = adminRequiredErr()
	secondTG.Err["getChatMember"] = adminRequiredErr()

	msgs := warnMessages(t)
	if IsChatAdmin(first, -100, 42) {
		t.Error("查不到就该按普通成员处理")
	}
	if firstTG.CountCalls("getChatMember") != 1 || secondTG.CountCalls("getChatMember") != 1 {
		t.Error("两个候选都该被问到")
	}
	if !containsMsg(msgs(), "群内没有管理员 bot") {
		t.Errorf("该提示群里没有管理员 bot，实际日志 %v", msgs())
	}
	if containsMsg(msgs(), "查询群管理员失败") {
		t.Errorf("配置问题不该报成查询失败，实际日志 %v", msgs())
	}
}

// TestIsChatAdminNetworkErrorNoFailover：网络故障不换 bot 再试——候选走
// 同一条出口与同一份代理，换一个只是把传输层超时再等一遍（每次最坏 40 秒），
// 而这条查询跑在串行的更新处理路径上。
func TestIsChatAdminNetworkErrorNoFailover(t *testing.T) {
	reg, primary := testutil.NewTestRegistry(t, nil)
	sh := primary.Shared
	first, firstTG := testutil.AddRegistryBot(t, reg, sh, 4343, 777)
	second, secondTG := testutil.AddRegistryBot(t, reg, sh, 4344, 777)
	testutil.EnableAntiad(t, first, -100)
	testutil.EnableAntiad(t, second, -100)
	firstTG.Err["getChatMember"] = errors.New("Post \"https://example.invalid\": dial tcp: i/o timeout")

	if IsChatAdmin(first, -100, 42) {
		t.Error("查不到就该按普通成员处理")
	}
	if n := firstTG.CountCalls("getChatMember"); n != 1 {
		t.Errorf("发起查询的 bot 只该被问一次，实际 %d 次", n)
	}
	if n := secondTG.CountCalls("getChatMember"); n != 0 {
		t.Errorf("网络故障不该换 bot 折返，实际 %d 次", n)
	}
}
