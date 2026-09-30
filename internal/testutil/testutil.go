package testutil

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"menshen/internal/config"
	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// TestToken 是格式合法但完全虚构的 token。
// 真实 token 等同于 bot 的完整控制权，任何测试里都不得出现。
const TestToken = "123456789:AAEEfaketoken_ForUnitTestsOnly1234567"

// FakeTG 是 tg.Transport 的假实现：记录全部调用，按方法名返回预设响应。
type FakeTG struct {
	mu    sync.Mutex
	calls []fakeCall
	// Resp 按方法名给定响应体，缺省返回 {"ok":true,"result":{}}。
	Resp map[string]string
	// RespFunc 优先于 Resp：需要按调用参数给不同响应时用它（比如按
	// chat_id 返回不同的成员状态）。返回 false 表示交给 Resp。
	RespFunc func(method string, payload map[string]any) (string, bool)
}

type fakeCall struct {
	method  string
	payload map[string]any
}

func NewFakeTG() *FakeTG { return &FakeTG{Resp: map[string]string{}} }

func (f *FakeTG) Call(method string, payload any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// 把 payload 归一化成 map，断言时不必关心它原本是什么类型。
	var m map[string]any
	if payload != nil {
		if raw, err := json.Marshal(payload); err == nil {
			json.Unmarshal(raw, &m)
		}
	}
	f.calls = append(f.calls, fakeCall{method: method, payload: m})

	if f.RespFunc != nil {
		if r, ok := f.RespFunc(method, m); ok {
			return json.RawMessage(r), nil
		}
	}
	if r, ok := f.Resp[method]; ok {
		return json.RawMessage(r), nil
	}
	return json.RawMessage(`{"ok":true,"result":{"message_id":1,"id":42}}`), nil
}

// Reset 清空调用记录（需要重新计数的测试用）。
func (f *FakeTG) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

// CountCalls 返回某方法被调用的次数。
func (f *FakeTG) CountCalls(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.method == method {
			n++
		}
	}
	return n
}

// Calls 返回某方法全部调用的载荷，按调用顺序。
func (f *FakeTG) Calls(method string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c.payload)
		}
	}
	return out
}

// LastCall 返回某方法最后一次调用的载荷，未调用过则返回 nil。
func (f *FakeTG) LastCall(method string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].method == method {
			return f.calls[i].payload
		}
	}
	return nil
}

// TestBotID 是测试 bot 的 user_id，与 FakeTG 默认 getMe 响应里的 id 一致。
const TestBotID int64 = 42

// NewTestBot 建一个挂着临时库、已注册好的 bot。adminID 是主管理员，
// 同时也是这个 bot 的归属人。
func NewTestBot(t *testing.T, adminID int64) (*core.Bot, *FakeTG) {
	t.Helper()
	b, tg, _ := NewTestBotOwned(t, adminID, adminID)
	return b, tg
}

// NewTestBotOwned 建一个归属指定 owner 的 bot，用于多租户隔离的测试。
func NewTestBotOwned(t *testing.T, adminID, ownerID int64) (*core.Bot, *FakeTG, *core.Shared) {
	t.Helper()
	return NewTestBotDispatch(t, adminID, ownerID, func(*core.Bot, *tg.Update) {})
}

// NewTestBotDispatch 同上，但可指定 Update 分发函数。
//
// 需要它的是那些走完整分发链的测试（队列串行性、端到端流程）：分发住在
// main 包，内部包的测试拿不到，只能由调用方把等价的那一份传进来。
func NewTestBotDispatch(t *testing.T, adminID, ownerID int64,
	d core.Dispatcher) (*core.Bot, *FakeTG, *core.Shared) {
	t.Helper()
	return newTestBotDispatch(t, adminID, ownerID, d, false)
}

// NewTestMainBotDispatch 建一个**主 bot** 实例（is_main=1），
// 用于主 bot 专属行为的测试：群消息守卫、自动退群、面板只读。
func NewTestMainBotDispatch(t *testing.T, adminID, ownerID int64,
	d core.Dispatcher) (*core.Bot, *FakeTG, *core.Shared) {
	t.Helper()
	return newTestBotDispatch(t, adminID, ownerID, d, true)
}

func newTestBotDispatch(t *testing.T, adminID, ownerID int64,
	d core.Dispatcher, isMain bool) (*core.Bot, *FakeTG, *core.Shared) {

	t.Helper()

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	cache, err := store.NewCache(db)
	if err != nil {
		t.Fatalf("store.NewCache: %v", err)
	}

	cfg := &config.Config{BotToken: TestToken, AdminIDs: []int64{adminID},
		TGAPIBase: "https://example.invalid", DBPath: "test.db"}
	fake := NewFakeTG()
	sh := core.NewShared(cfg, db, cache)
	// 所有 token 都用同一个假传输层：测试里不需要区分。
	sh.TransportFor = func(string) tg.Transport { return fake }

	registerTestBot(t, sh, TestToken, TestBotID, ownerID, isMain)

	b := core.NewBot(fake, sh, TestToken, d)
	b.SelfID.Store(TestBotID)
	b.OwnerID.Store(ownerID)
	b.Username = "testbot"
	return b, fake, sh
}

// RegisterTestBot 直接写 bots 表，绕开注册流程里的 getMe。
func RegisterTestBot(t *testing.T, sh *core.Shared, token string, botID, ownerID int64) {
	t.Helper()
	registerTestBot(t, sh, token, botID, ownerID, false)
}

// RegisterMainTestBot 同上，但标记为主 bot（is_main=1）。
func RegisterMainTestBot(t *testing.T, sh *core.Shared, token string, botID, ownerID int64) {
	t.Helper()
	registerTestBot(t, sh, token, botID, ownerID, true)
}

func registerTestBot(t *testing.T, sh *core.Shared, token string, botID, ownerID int64, isMain bool) {
	t.Helper()
	main := 0
	if isMain {
		main = 1
	}
	// 允许对同一个 bot 再调一次改标记：token 与 bot_id 上都有唯一约束，
	// 冲突时只更新 is_main。
	if _, err := sh.Store.Write.Exec(`INSERT INTO bots
		(token,bot_id,username,owner_id,so_model,llm_model,enabled,created_at,is_main)
		VALUES (?,?,'testbot',?,'','',1,0,?)
		ON CONFLICT(bot_id) DO UPDATE SET is_main=excluded.is_main`,
		token, botID, ownerID, main); err != nil {
		t.Fatalf("注册测试 bot 失败: %v", err)
	}
	if err := sh.Cache.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
}

// EnableAntiad 打开总开关并把 chatID 挂到这个 bot 名下（正式模式）。
func EnableAntiad(t *testing.T, b *core.Bot, chatID int64) {
	t.Helper()
	EnableAntiadMode(t, b, chatID, false)
}

// EnableAntiadMode 同上，但可以指定演练还是正式。
func EnableAntiadMode(t *testing.T, b *core.Bot, chatID int64, dryrun bool) {
	t.Helper()
	if err := b.PutSetting("antiad_enabled", "1"); err != nil {
		t.Fatalf("putSetting: %v", err)
	}
	dry := 0
	if dryrun {
		dry = 1
	}
	if _, err := b.Store.Write.Exec(`INSERT INTO bot_chats
		(bot_id,chat_id,title,enabled,dryrun,group_alert,created_at)
		VALUES (?,?,'测试群',1,?,0,0)
		ON CONFLICT(bot_id,chat_id) DO UPDATE SET enabled=1, dryrun=excluded.dryrun`,
		b.BotID(), chatID, dry); err != nil {
		t.Fatalf("添加生效群失败: %v", err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
}

// ChatConfOf 取测试里那个群的配置，找不到就让测试失败。
func ChatConfOf(t *testing.T, b *core.Bot, chatID int64) store.BotChat {
	t.Helper()
	c, ok := b.Cache.Snap().ChatConf(b.BotID(), chatID)
	if !ok {
		t.Fatalf("群 %d 不在 bot 名下", chatID)
	}
	return c
}

// GroupMsg 造一条群消息。
func GroupMsg(chatID, uid, msgID int64, text string) *tg.Message {
	return &tg.Message{
		MessageID: msgID, Date: 1700000000, Text: text,
		From: &tg.TGUser{ID: uid, Username: "someone"},
		Chat: &tg.Chat{ID: chatID, Type: "supergroup", Title: "测试群"},
	}
}

// NewTestRegistry 建一个 registry，bots 表里已有一个登记好的测试 bot。
// 实例由 LoadAll 从表里拉起，走的就是启动时的那条路径。
//
// d 是注入给每个实例的分发函数：真正的分发住在 main 包，内部包的测试
// 拿不到，需要走完整链路的用例自己传一份等价的进来。
func NewTestRegistry(t *testing.T, d core.Dispatcher) (*core.Registry, *core.Bot) {
	t.Helper()
	_, _, sh := NewTestBotDispatch(t, 777, 777, d)
	// registry 本来就是 webhook 模式的产物：长轮询下只有主 bot 能跑，
	// 子 bot 连注册都会被拦掉。专门测长轮询的用例自己清空这一项。
	sh.Cfg.PublicURL = "https://test.invalid"

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	reg := core.NewRegistry(sh, stop, d)
	reg.LoadAll()
	b, ok := reg.LookupToken(TestToken)
	if !ok {
		t.Fatal("已登记的 bot 没能被 LoadAll 拉起来")
	}
	return reg, b
}

// SetChatEnabled 直接改某个群对该 bot 的启用状态。
//
// 这是造状态的手段，不是被测行为：面板上那条路径（panel.setChatFlag）
// 有自己的测试，内部包的测试只是需要一个「已停用的群」。
func SetChatEnabled(t *testing.T, b *core.Bot, chatID int64, on bool) {
	t.Helper()
	v := 0
	if on {
		v = 1
	}
	if _, err := b.Store.Write.Exec(
		`UPDATE bot_chats SET enabled=? WHERE bot_id=? AND chat_id=?`,
		v, b.BotID(), chatID); err != nil {
		t.Fatalf("改群启用状态失败: %v", err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
}

// AddRegistryBot 往 registry 里再挂一个 bot，并给它一个独立的假传输层，
// 用于「同群多 bot」的测试：各自发了哪些请求要能分开数。
func AddRegistryBot(t *testing.T, reg *core.Registry, sh *core.Shared,
	botID, ownerID int64) (*core.Bot, *FakeTG) {

	t.Helper()
	token := strconv.FormatInt(botID, 10) + ":AAEEfaketoken_ForUnitTestsOnly1234567"
	fake := NewFakeTG()
	prev := sh.TransportFor
	sh.TransportFor = func(tok string) tg.Transport {
		if tok == token {
			return fake
		}
		return prev(tok)
	}
	RegisterTestBot(t, sh, token, botID, ownerID)
	reg.LoadAll()
	b, ok := reg.LookupID(botID)
	if !ok {
		t.Fatalf("bot %d 没能被 LoadAll 拉起来", botID)
	}
	return b, fake
}

// SetChatPunish 把某个群的处罚方式改成 punish（1 = 封禁出群，0 = 禁言，
// -1 = 跟随 bot）。联合封禁按每群自己的配置执行，测试要覆盖封禁路径时
// 得先把它设上。
func SetChatPunish(t *testing.T, b *core.Bot, chatID, punish int64) {
	t.Helper()
	if _, err := b.Store.Write.Exec(
		`UPDATE bot_chats SET punish=? WHERE bot_id=? AND chat_id=?`,
		punish, b.BotID(), chatID); err != nil {
		t.Fatalf("设置处罚方式失败: %v", err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
}

// SetChatDryrun 把某个群切到演练模式（判定照跑、不处置）。
func SetChatDryrun(t *testing.T, b *core.Bot, chatID int64, on bool) {
	t.Helper()
	v := 0
	if on {
		v = 1
	}
	if _, err := b.Store.Write.Exec(
		`UPDATE bot_chats SET dryrun=? WHERE bot_id=? AND chat_id=?`,
		v, b.BotID(), chatID); err != nil {
		t.Fatalf("设置演练开关失败: %v", err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
}
