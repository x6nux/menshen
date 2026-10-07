package core

import (
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"menshen/internal/config"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// countingTG 只数 webhook 生命周期与菜单按钮这几个方法，其余一律回 ok。
type countingTG struct {
	setWebhook    atomic.Int32
	deleteWebhook atomic.Int32
	menuButtons   atomic.Int32
}

func (c *countingTG) Call(method string, payload any) (json.RawMessage, error) {
	switch method {
	case "setWebhook":
		c.setWebhook.Add(1)
	case "deleteWebhook":
		c.deleteWebhook.Add(1)
	case "setChatMenuButton":
		c.menuButtons.Add(1)
	}
	return json.RawMessage(`{"ok":true,"result":{}}`), nil
}

// newCoreTestShared 造一个挂着临时库的 Shared。core 的测试不能 import
// testutil（它反过来 import core），所以这里自己搭最小环境。
func newCoreTestShared(t *testing.T) *Shared {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	c, err := store.NewCache(s)
	if err != nil {
		t.Fatal(err)
	}
	return NewShared(&config.Config{AdminIDs: []int64{777}, TGAPIBase: "http://tg.invalid"}, s, c)
}

// insertCoreBot 直接落一条 bots 行并刷新快照。
func insertCoreBot(t *testing.T, sh *Shared, token string, botID, owner int64) *store.BotRec {
	t.Helper()
	if _, err := sh.Store.Write.Exec(`INSERT INTO bots
		(token,bot_id,username,owner_id,so_model,llm_model,enabled,created_at,is_main)
		VALUES (?,?,?,?,'','',1,?,0)`,
		token, botID, "coretest", owner, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if err := sh.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	rec := sh.Cache.Snap().Bots[botID]
	if rec == nil {
		t.Fatal("bot 没落库")
	}
	return rec
}

// TestSpawnReplacesExistingInstance：同一个 bot 重复 spawn 必须先停掉旧实例。
// 旧实例的 64 个 worker 与队列不会被回收，且两个实例会一起消费更新。
func TestSpawnReplacesExistingInstance(t *testing.T) {
	sh := newCoreTestShared(t)
	rec := insertCoreBot(t, sh, "123456789:AAcoretest_token_0000000000000001", 42, 777)
	reg := NewRegistry(sh, make(chan struct{}), nil)

	reg.mu.Lock()
	first := reg.spawn(rec)
	second := reg.spawn(rec)
	reg.mu.Unlock()
	defer second.Shutdown()

	if first == second {
		t.Fatal("重复 spawn 应换上新实例")
	}
	if got, _ := reg.LookupID(42); got != second {
		t.Error("注册表应指向新实例")
	}
	select {
	case <-first.done:
	default:
		t.Error("旧实例应被停掉（done 未关闭）")
	}
}

// TestDisableBotDropsWebhook：停用 bot 后 TG 侧还留着 webhook 的话，它会持续
// 投递并收到 401、按退避一直重推。停用必须把 webhook 一起撤掉。
func TestDisableBotDropsWebhook(t *testing.T) {
	sh := newCoreTestShared(t)
	// webhook 模式才会给子 bot 建实例（长轮询只跑配置里那一个 bot）。
	sh.Cfg.PublicURL = "https://ad.example.com"
	insertCoreBot(t, sh, "123456789:AAcoretest_token_0000000000000002", 42, 777)
	fake := &countingTG{}
	sh.TransportFor = func(string) tg.Transport { return fake }
	reg := NewRegistry(sh, make(chan struct{}), nil)
	reg.LoadAll()

	if err := reg.SetBotEnabled(42, false); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for fake.deleteWebhook.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if fake.deleteWebhook.Load() == 0 {
		t.Error("停用 bot 应撤掉 webhook")
	}
}

// TestFinishSetupWaitsForPendingDrop：重新接入同一个 token 时必须等上一次
// deleteWebhook 结束 —— 迟到的 deleteWebhook 会把刚设上的 webhook 悄悄删掉，
// 而表现是 setWebhook 成功却一条更新都收不到。
func TestFinishSetupWaitsForPendingDrop(t *testing.T) {
	sh := newCoreTestShared(t)
	sh.Cfg.PublicURL = "https://ad.example.com"
	insertCoreBot(t, sh, "123456789:AAcoretest_token_0000000000000003", 42, 777)
	fake := &countingTG{}
	sh.TransportFor = func(string) tg.Transport { return fake }
	reg := NewRegistry(sh, make(chan struct{}), nil)
	reg.LoadAll()
	b, ok := reg.LookupID(42)
	if !ok {
		t.Fatal("实例没起来")
	}

	ch := make(chan struct{})
	reg.pendingDrops.Store(b.Token, ch)
	done := make(chan error, 1)
	go func() { done <- b.FinishSetup() }()

	time.Sleep(50 * time.Millisecond)
	if fake.setWebhook.Load() != 0 {
		t.Fatal("应等 pending drop 结束再设 webhook")
	}
	close(ch)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("FinishSetup: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("FinishSetup 卡住了")
	}
	if n := fake.setWebhook.Load(); n != 1 {
		t.Errorf("应设一次 webhook，得到 %d", n)
	}
}

// TestAddAdminRegistersMiniAppButton：新加的次级管理员要立刻收到 Mini App
// 菜单按钮。按钮是 per-chat 的、必须由 bot 主动设置，等到下次重启才挂的话
// 他点了菜单也进不去配置台。
func TestAddAdminRegistersMiniAppButton(t *testing.T) {
	sh := newCoreTestShared(t)
	sh.Cfg.PublicURL = "https://ad.example.com"
	insertCoreBot(t, sh, "123456789:AAcoretest_token_0000000000000004", 42, 777)
	fake := &countingTG{}
	sh.TransportFor = func(string) tg.Transport { return fake }
	reg := NewRegistry(sh, make(chan struct{}), nil)
	reg.LoadAll()

	if err := sh.AddAdmin(888, "新次管", 777); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for fake.menuButtons.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if fake.menuButtons.Load() == 0 {
		t.Error("新增管理员后应补挂 Mini App 菜单按钮")
	}
}
