package tg

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRichTextFlatten：富文本同一位置可能是字符串、节点数组或对象，并且
// 会互相嵌套。三种形状都要摊平成文字，链接、@名、卡号这些载体一个都不能
// 丢 —— 引流载荷正是藏在这些格式化层里。
func TestRichTextFlatten(t *testing.T) {
	cases := []struct {
		name string
		js   string
		want []string
	}{
		{"纯字符串", `"一句话"`, []string{"一句话"}},
		{"节点数组", `[{"type":"bold","text":"加粗"},{"type":"italic","text":"斜体"}]`,
			[]string{"加粗", "斜体"}},
		{"嵌套格式", `{"type":"bold","text":[{"type":"italic","text":"里层"}]}`, []string{"里层"}},
		{"链接载体", `{"type":"url","text":"点这里","url":"https://ad.example"}`,
			[]string{"点这里", "https://ad.example"}},
		{"裸链接", `{"type":"url","url":"https://ad.example/x"}`, []string{"https://ad.example/x"}},
		{"@名与卡号", `{"type":"bank_card_number","text":"1234","bank_card_number":"6222 0000"}`,
			[]string{"1234", "6222 0000"}},
		{"自定义表情", `{"type":"custom_emoji","custom_emoji_id":"1","alternative_text":"🔥"}`,
			[]string{"🔥"}},
		{"空节点", `null`, nil},
	}
	for _, c := range cases {
		var rt RichText
		if err := json.Unmarshal([]byte(c.js), &rt); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := rt.Line()
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: Line() = %q，缺 %q", c.name, got, w)
			}
		}
		if len(c.want) == 0 && got != "" {
			t.Errorf("%s: Line() = %q，期望空", c.name, got)
		}
	}
}

// TestRichTextCarrierNotRepeated：链接与文字相同时只留一份。
// 判定正文要的是可读，不是把同一条链接复读两遍。
func TestRichTextCarrierNotRepeated(t *testing.T) {
	var rt RichText
	json.Unmarshal([]byte(`{"type":"url","text":"https://ad.example","url":"https://ad.example"}`), &rt)
	if got := rt.Line(); got != "https://ad.example" {
		t.Errorf("Line() = %q，期望只出现一次链接", got)
	}
}

// TestMessageKeepsRaw：原始报文要留下来。载荷全不可识别时判定只能放行，
// 日志里那份原始报文的字段名是补类型的唯一依据（见 antiad.noteUnknownPayload）。
func TestMessageKeepsRaw(t *testing.T) {
	const js = `{"message_id":7,"mystery_media":{"x":1}}`
	var m Message
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if m.MessageID != 7 {
		t.Errorf("message_id = %d，期望 7", m.MessageID)
	}
	if !strings.Contains(string(m.Raw), "mystery_media") {
		t.Errorf("原始报文没留下：%s", m.Raw)
	}
}

// TestRichMessageBlockTags：块里的字段是错落定义的（同样的键在不同块里
// 含义不同），按类型取不到就整块丢。这里逐类钉住取到的文字。
func TestRichMessageBlockTags(t *testing.T) {
	var m Message
	json.Unmarshal([]byte(`{"rich_message":{"blocks":[
		{"type":"heading","text":"标题"},
		{"type":"list","items":[{"label":"条目","blocks":[{"type":"paragraph","text":"子段"}]}]},
		{"type":"table","cells":[[{"text":"格一"},{"text":"格二"}]]},
		{"type":"buttons","buttons":[{"text":"进入","url":"https://ad.example"}]}
	]}}`), &m)
	if m.RichMessage == nil || len(m.RichMessage.Blocks) != 4 {
		t.Fatalf("块没解出来：%+v", m.RichMessage)
	}
	if got := m.RichMessage.Blocks[0].Text.Line(); got != "标题" {
		t.Errorf("标题块 = %q", got)
	}
	if got := m.RichMessage.Blocks[1].Items[0].Label; got != "条目" {
		t.Errorf("列表项标签 = %q", got)
	}
	if got := m.RichMessage.Blocks[1].Items[0].Blocks[0].Text.Line(); got != "子段" {
		t.Errorf("列表项子块 = %q", got)
	}
	if got := m.RichMessage.Blocks[2].Cells[0][1].Text.Line(); got != "格二" {
		t.Errorf("表格单元格 = %q", got)
	}
	b := m.RichMessage.Blocks[3].Buttons[0]
	if b.Text.Line() != "进入" || b.URL != "https://ad.example" {
		t.Errorf("按钮 = %q / %q", b.Text.Line(), b.URL)
	}
}
