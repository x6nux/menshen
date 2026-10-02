package main

import (
	"context"
	"slices"
	"testing"
	"time"

	"menshen/internal/core"
	"menshen/internal/testutil"
)

// newMainBotEnv 造一个挂了临时库的 Shared，并把 registry 也备好。
// 初始状态下 bots 表里有一个 TestToken 的**工作 bot**，便于测升级路径。
func newMainBotEnv(t *testing.T) (*core.Shared, *core.Registry) {
	t.Helper()
	_, _, sh := testutil.NewTestBotDispatch(t, 777, 777, nil)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	return sh, core.NewRegistry(sh, stop, nil)
}

// TestBindLegacyModelsOnlyWithSingleUpstream：只有一个上游时自动补前缀
// （升级无痛）；多于一个时无从推断，必须原样不动。
func TestBindLegacyModelsOnlyWithSingleUpstream(t *testing.T) {
	sh, _ := newMainBotEnv(t)
	w := sh.Store.Write
	if _, err := w.Exec(`INSERT INTO upstreams
		(name,base_url,api_key,weight,status,supports_chat,supports_systemone)
		VALUES ('up1','http://x','k',1,1,1,1)`); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"m1", "m2"} {
		if _, err := w.Exec(`INSERT INTO models (name,prompt_price,
			completion_price,cache_read_price,cache_write_price,enabled)
			VALUES (?,0,0,0,0,1)`, n); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Exec(`INSERT INTO settings (k,v) VALUES
		('antiad_so_model','m1'),
		('antiad_llm_models','["m2"]')`); err != nil {
		t.Fatal(err)
	}
	if err := sh.Cache.Reload(); err != nil {
		t.Fatal(err)
	}

	bindLegacyModels(sh)

	snap := sh.Cache.Snap()
	if snap.Models["up1/m1"] == nil || snap.Models["m1"] != nil {
		t.Errorf("旧模型没补上前缀: %v", snap.Models)
	}
	if got := snap.Setting("antiad_so_model"); got != "up1/m1" {
		t.Errorf("单值设置没改写: %q", got)
	}
	if got := snap.SettingStrings("antiad_llm_models"); !slices.Equal(got, []string{"up1/m2"}) {
		t.Errorf("列表设置没改写: %v", got)
	}

	// 再加一个上游：新的旧格式模型不该被自动绑定。
	if _, err := w.Exec(`INSERT INTO upstreams
		(name,base_url,api_key,weight,status,supports_chat,supports_systemone)
		VALUES ('up2','http://y','k',1,1,1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`INSERT INTO models (name,prompt_price,
		completion_price,cache_read_price,cache_write_price,enabled)
		VALUES ('m3',0,0,0,0,1)`); err != nil {
		t.Fatal(err)
	}
	if err := sh.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	bindLegacyModels(sh)
	if sh.Cache.Snap().Models["m3"] == nil {
		t.Error("多个上游时不该自动绑定旧格式模型")
	}
}

// TestEnsureMainBotRegistersAndMarks 覆盖全新部署：配置里的 bot 还没登记，
// ensureMainBot 要把它接进来并标成主 bot。
func TestEnsureMainBotRegistersAndMarks(t *testing.T) {
	sh, reg := newMainBotEnv(t)
	// 清掉 testutil 预登记的工作 bot，模拟全新部署。
	if _, err := sh.Store.Write.Exec(`DELETE FROM bots`); err != nil {
		t.Fatal(err)
	}
	if err := sh.Cache.Reload(); err != nil {
		t.Fatal(err)
	}

	cfg := sh.Cfg
	cfg.BotToken = testutil.TestToken
	if err := ensureMainBot(cfg, sh, reg); err != nil {
		t.Fatalf("ensureMainBot: %v", err)
	}

	rec := sh.Cache.Snap().Bots[testutil.TestBotID]
	if rec == nil || !rec.IsMain {
		t.Fatalf("配置里的 bot 应被登记并标记为主 bot，得到 %+v", rec)
	}
}

// TestEnsureMainBotUpgradesAndClearsChats 覆盖老库升级：bot 已登记为工作
// bot，且名下有生效群。启动后要补上主 bot 标记，并清掉它不再需要的群配置。
func TestEnsureMainBotUpgradesAndClearsChats(t *testing.T) {
	sh, reg := newMainBotEnv(t)
	// 给这个 bot 挂一个群（升级前主 bot 可能在群里判过）。
	if _, err := sh.Store.Write.Exec(`INSERT INTO bot_chats
		(bot_id,chat_id,title,enabled,dryrun,group_alert,created_at)
		VALUES (?,?, '旧群',1,0,0,0)`, testutil.TestBotID, -100123); err != nil {
		t.Fatal(err)
	}
	if err := sh.Cache.Reload(); err != nil {
		t.Fatal(err)
	}

	cfg := sh.Cfg
	cfg.BotToken = testutil.TestToken
	if err := ensureMainBot(cfg, sh, reg); err != nil {
		t.Fatalf("ensureMainBot: %v", err)
	}

	snap := sh.Cache.Snap()
	rec := snap.Bots[testutil.TestBotID]
	if rec == nil || !rec.IsMain {
		t.Fatalf("老库里的配置 bot 应被补上主 bot 标记，得到 %+v", rec)
	}
	if _, ok := snap.ChatConf(testutil.TestBotID, -100123); ok {
		t.Error("主 bot 名下的遗留群配置应被清理")
	}
}

// TestEnsureMainBotDemotesOldMain 覆盖换配置：配置里换成另一个 bot 之后，
// 旧主 bot 要降级为工作 bot，不能出现两个主 bot。
func TestEnsureMainBotDemotesOldMain(t *testing.T) {
	sh, reg := newMainBotEnv(t)
	const secondToken = "123456789:AAEEfaketoken_SecondBotForUnitTest12"
	testutil.RegisterTestBot(t, sh, secondToken, 43, 777)
	testutil.RegisterMainTestBot(t, sh, testutil.TestToken, testutil.TestBotID, 777)

	cfg := sh.Cfg
	cfg.BotToken = secondToken
	if err := ensureMainBot(cfg, sh, reg); err != nil {
		t.Fatalf("ensureMainBot: %v", err)
	}

	snap := sh.Cache.Snap()
	if rec := snap.Bots[43]; rec == nil || !rec.IsMain {
		t.Errorf("新配置的 bot 应成为主 bot，得到 %+v", rec)
	}
	if rec := snap.Bots[testutil.TestBotID]; rec == nil || rec.IsMain {
		t.Errorf("旧主 bot 应降级为工作 bot，得到 %+v", rec)
	}
}

// TestRefreshChatTitles：空标题（先加配置、后入群）要被补上；全量刷新
// 还要跟进群改名，只补空标题时不动已有标题。
func TestRefreshChatTitles(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	title := "补回来的群名"
	fake.RespFunc = func(method string, payload map[string]any) (string, bool) {
		if method == "getChat" {
			return `{"ok":true,"result":{"id":-100,"type":"supergroup",` +
				`"title":"` + title + `"}}`, true
		}
		return "", false
	}
	insertChat := func(t *testing.T, chatID int64, title string) {
		t.Helper()
		if _, err := b.Store.Write.Exec(`INSERT INTO bot_chats
			(bot_id,chat_id,title,enabled,dryrun,group_alert,created_at)
			VALUES (?,?,?,1,0,0,0)`, b.BotID(), chatID, title); err != nil {
			t.Fatal(err)
		}
		if err := b.Cache.Reload(); err != nil {
			t.Fatal(err)
		}
	}
	chatTitle := func(t *testing.T, chatID int64) string {
		t.Helper()
		conf, ok := b.Cache.Snap().ChatConf(b.BotID(), chatID)
		if !ok {
			t.Fatalf("群 %d 不在配置里", chatID)
		}
		return conf.Title
	}

	// 空标题：只补空标题的分钟级刷新也要补上。
	insertChat(t, -100, "")
	refreshChatTitles(b, true)
	if got := chatTitle(t, -100); got != "补回来的群名" {
		t.Fatalf("空标题应被补上，得到 %q", got)
	}

	// 已有标题：只补空标题时不动；全量刷新跟进改名。
	insertChat(t, -200, "旧群名")
	refreshChatTitles(b, true)
	if got := chatTitle(t, -200); got != "旧群名" {
		t.Fatalf("只补空标题时不该动已有标题，得到 %q", got)
	}
	refreshChatTitles(b, false)
	if got := chatTitle(t, -200); got != "补回来的群名" {
		t.Fatalf("全量刷新应跟进改名，得到 %q", got)
	}
}

// TestWebhookServerTimeouts：只设 ReadHeaderTimeout 挡不住慢速发 body 的
// 连接——它会一直占着 goroutine 与内存。四项超时都必须就位。
func TestWebhookServerTimeouts(t *testing.T) {
	sh, reg := newMainBotEnv(t)
	sh.Cfg.ListenAddr = "127.0.0.1:0"
	srv := startWebhook(sh.Cfg, reg, sh)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 ||
		srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Errorf("HTTP 超时不全: header=%v read=%v write=%v idle=%v",
			srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout)
	}
}
