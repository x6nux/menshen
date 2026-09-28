package core_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// probeTG 只回答 getMe，返回预设的 bot_id，供注册路径使用。
type probeTG struct{ id int64 }

func (p probeTG) Call(method string, payload any) (json.RawMessage, error) {
	if method == "getMe" {
		return json.RawMessage(fmt.Sprintf(
			`{"ok":true,"result":{"id":%d,"username":"probe"}}`, p.id)), nil
	}
	return json.RawMessage(`{"ok":true,"result":{}}`), nil
}

// TestRegisterMarksMainBot 确认 isMain 参数落到 bots.is_main：
// 主 bot 标记是「不入群、不判定」的唯一依据，注册时就该带上。
func TestRegisterMarksMainBot(t *testing.T) {
	_, _, sh := testutil.NewTestBotDispatch(t, 777, 777, nil)
	sh.Cfg.PublicURL = "https://test.invalid" // Register 的子 bot 只在 webhook 模式下放行
	sh.TransportFor = func(string) tg.Transport { return probeTG{id: 99} }

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	reg := core.NewRegistry(sh, stop, nil)

	const mainToken = "123456789:AAEEfaketoken_MainBotForUnitTests123"
	rec, err := reg.Register(mainToken, 777, true)
	if err != nil {
		t.Fatalf("注册主 bot 失败: %v", err)
	}
	if !rec.IsMain {
		t.Fatalf("Register(isMain=true) 返回的记录没带主 bot 标记: %+v", rec)
	}

	snap := sh.Cache.Snap()
	if r := snap.Bots[99]; r == nil || !r.IsMain {
		t.Errorf("快照里新登记的主 bot 标记丢了: %+v", r)
	}
	if r := snap.Bots[testutil.TestBotID]; r == nil || r.IsMain {
		t.Errorf("已登记的工作 bot 不该被连带标成主 bot: %+v", r)
	}
}

// TestMainBotCannotBeDisabledOrRemoved 守的是两个会把面板弄失联的动作：
// 停用主 bot 后没人能再启用它，移除它则会在下次启动时被 ensureMainBot
// 加回来 —— 表现为「删了又复活」，只会让人更糊涂。
func TestMainBotCannotBeDisabledOrRemoved(t *testing.T) {
	reg, b := testutil.NewTestRegistry(t, nil)
	testutil.RegisterMainTestBot(t, b.Shared, testutil.TestToken, testutil.TestBotID, 777)

	if !b.IsMainBot() {
		t.Fatal("标记之后 IsMainBot 应为 true")
	}

	if err := reg.SetBotEnabled(b.BotID(), false); err == nil {
		t.Error("停用主 bot 应被拒绝")
	}
	if err := reg.Unregister(b.BotID()); err == nil {
		t.Error("移除主 bot 应被拒绝")
	}

	rec := b.Cache.Snap().Bots[b.BotID()]
	if rec == nil || !rec.Enabled || !rec.IsMain {
		t.Errorf("被拒绝后主 bot 的记录应原样保留，得到 %+v", rec)
	}
}
