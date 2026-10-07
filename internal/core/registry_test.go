package core_test

import (
	"encoding/json"
	"fmt"
	"slices"
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

// TestSetBotModelsAndLegacyFallback：per-bot 覆盖写进新 JSON 列；旧单值
// 列在列表为空时仍作为单元素列表读出来，无需管理员重配。
func TestSetBotModelsAndLegacyFallback(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)

	if err := b.SetBotModels(testutil.TestBotID, "so", []string{"a/m1", "b/m2"}); err != nil {
		t.Fatalf("SetBotModels: %v", err)
	}
	so, _ := b.Cache.Snap().ModelsFor(testutil.TestBotID)
	if !slices.Equal(so, []string{"a/m1", "b/m2"}) {
		t.Fatalf("per-bot 模型列表没落上，得到 %v", so)
	}

	// 模拟老库：只有旧单值列。
	if _, err := b.Store.Write.Exec(
		`UPDATE bots SET so_models='', so_model='legacy/x' WHERE bot_id=?`,
		testutil.TestBotID); err != nil {
		t.Fatal(err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	so, _ = b.Cache.Snap().ModelsFor(testutil.TestBotID)
	if !slices.Equal(so, []string{"legacy/x"}) {
		t.Errorf("旧单值列应回退成单元素列表，得到 %v", so)
	}
}

// TestRenameUpstreamRewritesModelRefs：模型名里嵌着上游名，改名必须
// 连带改写模型名、默认模型设置与 bot 覆盖 —— 漏掉任何一处，那批模型
// 都会变成绑定的上游不存在，判定静默失效。
func TestRenameUpstreamRewritesModelRefs(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	w := b.Store.Write
	if _, err := w.Exec(`INSERT INTO upstreams
		(name,base_url,api_key,weight,status,supports_chat,supports_systemone)
		VALUES ('up1','http://x','k',1,1,1,1)`); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"up1/m1", "other/m2"} {
		if _, err := w.Exec(`INSERT INTO models (name,prompt_price,
			completion_price,cache_read_price,cache_write_price,enabled)
			VALUES (?,0,0,0,0,1)`, n); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Exec(`INSERT INTO settings (k,v) VALUES
		('antiad_so_models','["up1/m1","other/m2"]'),
		('antiad_so_model','up1/m1'),
		('antiad_rule_model','up1/m1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`UPDATE bots SET so_models='["up1/m1"]',
		so_model='up1/legacy' WHERE bot_id=?`, testutil.TestBotID); err != nil {
		t.Fatal(err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}

	var id int64
	if err := b.Store.Read.QueryRow(
		`SELECT id FROM upstreams WHERE name='up1'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := b.RenameUpstream(id, "up2"); err != nil {
		t.Fatalf("RenameUpstream: %v", err)
	}

	snap := b.Cache.Snap()
	if snap.Models["up2/m1"] == nil || snap.Models["up1/m1"] != nil {
		t.Errorf("模型名没跟着改名: %v", snap.Models)
	}
	if snap.Models["other/m2"] == nil {
		t.Error("不属于该上游的模型不该被动")
	}
	if got := snap.SettingStrings("antiad_so_models"); !slices.Equal(got,
		[]string{"up2/m1", "other/m2"}) {
		t.Errorf("默认模型列表没改写: %v", got)
	}
	if got := snap.Setting("antiad_so_model"); got != "up2/m1" {
		t.Errorf("旧单值设置没改写: %q", got)
	}
	if got := snap.Setting("antiad_rule_model"); got != "up2/m1" {
		t.Errorf("规则发现模型设置没改写: %q", got)
	}
	rec := snap.Bots[testutil.TestBotID]
	if !slices.Equal(rec.SoModels, []string{"up2/m1"}) {
		t.Errorf("bot 覆盖列表没改写: %v", rec.SoModels)
	}
	var legacy string
	if err := b.Store.Read.QueryRow(
		`SELECT so_model FROM bots WHERE bot_id=?`, testutil.TestBotID).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if legacy != "up2/legacy" {
		t.Errorf("bot 旧单值列没改写: %q", legacy)
	}

	// 重名与非法名要挡住
	if err := b.RenameUpstream(id, "up1/bad"); err == nil {
		t.Error("名称含 / 应被拒绝")
	}
}

// TestRegisterMarksMainBot 确认 isMain 参数落到 bots.is_main：
// 主 bot 标记是不入群、不判定的唯一依据，注册时就该带上。
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

// TestSetBotOwnerSyncsLiveInstance：改派归属后运行中的实例必须同步 ——
// 判定豁免、专属联合封禁账本与上游告警都读实例上的 owner，不同步的话
// 改派要等到下次重启才真正生效，而界面上一切正常。
func TestSetBotOwnerSyncsLiveInstance(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	if err := b.AddAdmin(888, "接手人", 777); err != nil {
		t.Fatal(err)
	}
	if got := b.Owner(); got != 777 {
		t.Fatalf("前置：实例归属应为 777，得到 %d", got)
	}

	if err := b.SetBotOwner(testutil.TestBotID, 888); err != nil {
		t.Fatalf("SetBotOwner: %v", err)
	}
	if got := b.Cache.Snap().Bots[testutil.TestBotID].OwnerID; got != 888 {
		t.Errorf("快照归属应为 888，得到 %d", got)
	}
	if got := b.Owner(); got != 888 {
		t.Errorf("实例上的归属没同步，得到 %d", got)
	}
}

// TestMainBotCannotBeDisabledOrRemoved 验证两个会让面板失联的动作被拒绝：
// 停用主 bot 后没人能再启用它，移除它则会在下次启动时被 ensureMainBot
// 加回来，表现为删了又复活。
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
