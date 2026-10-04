package panel

import (
	"fmt"
	"strings"
	"testing"

	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// TestUpstreamDeleteRefusedWhenModelsBound：模型名里嵌着上游名，删掉
// 有模型在用的上游会留下一批「绑定的上游不存在」的死引用 —— 判定每次
// 都失败，而面板上看不出原因。删除必须被挡下。
func TestUpstreamDeleteRefusedWhenModelsBound(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	if _, err := b.Store.Write.Exec(`INSERT INTO upstreams
		(name,base_url,api_key,weight,status,supports_chat,supports_systemone)
		VALUES ('alpha','http://x','k',1,1,1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Store.Write.Exec(`INSERT INTO models (name,prompt_price,
		completion_price,cache_read_price,cache_write_price,enabled)
		VALUES ('alpha/m1',0,0,0,0,1)`); err != nil {
		t.Fatal(err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := b.Store.Read.QueryRow(
		`SELECT id FROM upstreams WHERE name='alpha'`).Scan(&id); err != nil {
		t.Fatal(err)
	}

	HandleAdminCallback(b, &tg.CallbackQuery{
		ID: "cb1", Data: fmt.Sprintf("a:up:%d:d:y", id),
		From: &tg.TGUser{ID: 777},
		Message: &tg.Message{MessageID: 5,
			Chat: &tg.Chat{ID: 777, Type: "private"}},
	})

	var n int
	if err := b.Store.Read.QueryRow(
		`SELECT COUNT(*) FROM upstreams WHERE id=?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("名下有模型时不该删掉上游")
	}
	if b.Cache.Snap().Models["alpha/m1"] == nil {
		t.Error("模型不该被连带删除")
	}
	fake := b.TG.(*testutil.FakeTG)
	if ans := fake.LastCall("answerCallbackQuery"); ans == nil ||
		!strings.Contains(fmt.Sprint(ans["text"]), "名下有模型") {
		t.Errorf("应回一个说明原因的空/提示响应，得到 %v", ans)
	}
}

// newUpstreamCallback 造一个主管理员的私聊回调。
func newUpstreamCallback(data string) *tg.CallbackQuery {
	return &tg.CallbackQuery{
		ID: "cb1", Data: data,
		From: &tg.TGUser{ID: 777},
		Message: &tg.Message{MessageID: 5,
			Chat: &tg.Chat{ID: 777, Type: "private"}},
	}
}

// TestUpstreamNewCloudflareFlow：新增流程里选 Cloudflare 后直接落库，
// 能力固定为「主判定」（chat=0 / systemone=1），草稿用完即清。
func TestUpstreamNewCloudflareFlow(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	b.UpstreamNewDraft.Store(int64(777),
		"cf\x00https://api.cloudflare.com/client/v4/accounts/acc\x00cf-token")

	HandleAdminCallback(b, newUpstreamCallback("a:up:new:k:777:cloudflare"))

	var kind string
	var chat, so, status, weight int64
	if err := b.Store.Read.QueryRow(`SELECT kind,supports_chat,supports_systemone,
		status,weight FROM upstreams WHERE name='cf'`).Scan(
		&kind, &chat, &so, &status, &weight); err != nil {
		t.Fatalf("Cloudflare 上游没落库: %v", err)
	}
	if kind != "cloudflare" || chat != 0 || so != 1 || status != 1 || weight != 1 {
		t.Errorf("落库结果不对: kind=%q chat=%d so=%d status=%d weight=%d",
			kind, chat, so, status, weight)
	}
	if ups := b.Cache.Snap().Upstreams; len(ups) != 1 || ups[0].EffectiveKind().Label() != "Cloudflare Workers AI" {
		t.Errorf("快照里的渠道类型不对: %+v", ups)
	}
	if _, ok := b.UpstreamNewDraft.Load(int64(777)); ok {
		t.Error("创建后草稿应被清掉")
	}
}

// TestUpstreamNewOpenAIKindGoesToEndpointPicker：选 OpenAI 兼容后进入端点
// 勾选，还不能落库，草稿要留到确认那一步。
func TestUpstreamNewOpenAIKindGoesToEndpointPicker(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	b.UpstreamNewDraft.Store(int64(777), "oa\x00http://x\x00k")

	HandleAdminCallback(b, newUpstreamCallback("a:up:new:k:777:openai"))

	var n int
	if err := b.Store.Read.QueryRow(`SELECT COUNT(*) FROM upstreams`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("选类型阶段不该落库，得到 %d 行", n)
	}
	if _, ok := b.UpstreamNewDraft.Load(int64(777)); !ok {
		t.Error("草稿应保留到端点确认")
	}
}

// TestUpstreamKindCapsPolicy：切换渠道类型时能力按类型重置；chat-only
// 类型（如 Gemini）的端点开关回调会被挡下，不能把主判定打开。
func TestUpstreamKindCapsPolicy(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	if _, err := b.Store.Write.Exec(`INSERT INTO upstreams
		(name,base_url,api_key,weight,status,supports_chat,supports_systemone)
		VALUES ('oa','http://x','k',1,1,1,1)`); err != nil {
		t.Fatal(err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := b.Store.Read.QueryRow(
		`SELECT id FROM upstreams WHERE name='oa'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	read := func() (kind string, chat, so int64) {
		t.Helper()
		if err := b.Store.Read.QueryRow(`SELECT kind,supports_chat,supports_systemone
			FROM upstreams WHERE id=?`, id).Scan(&kind, &chat, &so); err != nil {
			t.Fatal(err)
		}
		return
	}

	HandleAdminCallback(b, newUpstreamCallback(fmt.Sprintf("a:up:%d:k:cloudflare", id)))
	if kind, chat, so := read(); kind != "cloudflare" || chat != 0 || so != 1 {
		t.Errorf("切到 Cloudflare 应默认主判定（0/1），得到 kind=%q chat=%d so=%d", kind, chat, so)
	}
	// Cloudflare 支持 chat：开关可以打开，变成 chat+主判定。
	HandleAdminCallback(b, newUpstreamCallback(fmt.Sprintf("a:up:%d:t:chat", id)))
	if kind, chat, so := read(); kind != "cloudflare" || chat != 1 || so != 1 {
		t.Errorf("Cloudflare 的 chat 开关应生效，得到 kind=%q chat=%d so=%d", kind, chat, so)
	}

	// Gemini 是 chat-only：切过去强制 1/0，主判定开关必须被挡下。
	HandleAdminCallback(b, newUpstreamCallback(fmt.Sprintf("a:up:%d:k:gemini", id)))
	if kind, chat, so := read(); kind != "gemini" || chat != 1 || so != 0 {
		t.Errorf("切到 Gemini 应强制 chat（1/0），得到 kind=%q chat=%d so=%d", kind, chat, so)
	}
	HandleAdminCallback(b, newUpstreamCallback(fmt.Sprintf("a:up:%d:t:so", id)))
	if kind, chat, so := read(); kind != "gemini" || chat != 1 || so != 0 {
		t.Errorf("Gemini 的 systemone 开关不该生效，得到 kind=%q chat=%d so=%d", kind, chat, so)
	}
	if ans := b.TG.(*testutil.FakeTG).LastCall("answerCallbackQuery"); ans == nil ||
		!strings.Contains(fmt.Sprint(ans["text"]), "Gemini") {
		t.Errorf("应提示该类型只能用于 chat，得到 %v", ans)
	}

	// 切回 OpenAI Completions：按默认 chat=1 / systemone=0。
	HandleAdminCallback(b, newUpstreamCallback(fmt.Sprintf("a:up:%d:k:openai", id)))
	if kind, chat, so := read(); kind != "openai" || chat != 1 || so != 0 {
		t.Errorf("切回 openai 应用默认能力（1/0），得到 kind=%q chat=%d so=%d", kind, chat, so)
	}
}
