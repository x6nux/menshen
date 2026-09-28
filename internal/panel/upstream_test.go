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
