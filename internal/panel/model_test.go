package panel

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"menshen/internal/testutil"
)

// TestParseModelListValidates 验证模型列表输入的校验：顺序保留、
// 去重、每个都必须存在且启用——配一个不存在的名字，判定链路会在
// 每条消息上拿 404，而表现只是判定失败后放行。
func TestParseModelListValidates(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	if _, err := b.Store.Write.Exec(`INSERT INTO models (name,prompt_price,
		completion_price,cache_read_price,cache_write_price,enabled) VALUES
		('a/m1',0,0,0,0,1),('b/m2',0,0,0,0,1)`); err != nil {
		t.Fatal(err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}

	got, err := parseModelList(b.Cache.Snap(), " a/m1 , b/m2 ,a/m1")
	if err != nil {
		t.Fatalf("合法列表被拒: %v", err)
	}
	if !slices.Equal(got, []string{"a/m1", "b/m2"}) {
		t.Errorf("应保留顺序并去重，得到 %v", got)
	}

	if _, err := parseModelList(b.Cache.Snap(), "a/m1, nope"); err == nil {
		t.Error("不存在的模型应被拒")
	}
	if _, err := parseModelList(b.Cache.Snap(), "  "); err == nil {
		t.Error("空列表应被拒")
	}

	if _, err := b.Store.Write.Exec(
		`UPDATE models SET enabled=0 WHERE name='b/m2'`); err != nil {
		t.Fatal(err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := parseModelList(b.Cache.Snap(), "b/m2"); err == nil {
		t.Error("停用的模型应被拒")
	}
}

// TestModelUpstreamPick：新增模型必须先选上游（模型名前缀的唯一来源）。
// 一个可用上游都没有时要给出明确的下一步，而不是让人对着空列表点。
func TestModelUpstreamPick(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	fake := b.TG.(*testutil.FakeTG)

	showModelUpstreamPick(b, 777, 0)
	text := fake.LastCall("sendMessage")["text"].(string)
	if !strings.Contains(text, "还没有可用的上游") {
		t.Errorf("没有上游时应提示先去添加:\n%s", text)
	}

	if _, err := b.Store.Write.Exec(`INSERT INTO upstreams
		(name,base_url,api_key,weight,status,supports_chat,supports_systemone)
		VALUES ('alpha','http://x','k',1,1,1,1)`); err != nil {
		t.Fatal(err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	showModelUpstreamPick(b, 777, 0)
	last := fake.LastCall("sendMessage")
	if !strings.Contains(last["text"].(string), "先选这个模型走哪个上游") {
		t.Errorf("应进入上游选择:\n%v", last["text"])
	}
	if kb := fmt.Sprint(last["reply_markup"]); !strings.Contains(kb, "a:md:newu:") {
		t.Errorf("应给出上游选择按钮，得到 %s", kb)
	}
}
