package antiad

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"menshen/internal/testutil"
)

// memberJSON 造 getChatMember 的响应：canSend 为 nil 表示不带该字段
// （普通成员就是这样，不能当成被禁言）。
func memberJSON(status string, canSend *bool, until int64, member bool) string {
	cs := "null"
	if canSend != nil {
		cs = fmt.Sprintf("%v", *canSend)
	}
	return fmt.Sprintf(
		`{"ok":true,"result":{"status":%q,"can_send_messages":%s,"until_date":%d,"is_member":%v}}`,
		status, cs, until, member)
}

// TestResidualSweepFixesStuckRestrictions：库里查不到限制时，复查要按
// Telegram 的真实状态修：被踢的解封、永久禁言的解禁言、限时禁言不动。
func TestResidualSweepFixesStuckRestrictions(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	fake := b.TG.(*testutil.FakeTG)
	for _, chat := range []int64{-100, -200, -300, -400} {
		testutil.EnableAntiad(t, b, chat)
	}
	if _, err := b.Store.Write.Exec(`UPDATE bot_chats SET title='一号群' WHERE chat_id=-100;
		UPDATE bot_chats SET title='二号群' WHERE chat_id=-200;
		UPDATE bot_chats SET title='三号群' WHERE chat_id=-300;
		UPDATE bot_chats SET title='四号群' WHERE chat_id=-400`); err != nil {
		t.Fatal(err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}

	yes, no := true, false
	fake.RespFunc = func(method string, p map[string]any) (string, bool) {
		if method != "getChatMember" {
			return "", false
		}
		switch int64(p["chat_id"].(float64)) {
		case -100:
			return memberJSON("kicked", nil, 0, false), true
		case -200:
			return memberJSON("restricted", &no, 0, true), true
		case -300:
			return memberJSON("restricted", &no, time.Now().Unix()+3600, true), true
		}
		return memberJSON("member", &yes, 0, true), true
	}

	res := residualSweep(b, 555)
	if res.Checked != 4 {
		t.Errorf("四个群都该问到状态，得到 %d", res.Checked)
	}
	if !slices.Contains(res.Fixed, "一号群") || !slices.Contains(res.Fixed, "二号群") {
		t.Errorf("被踢与被永久禁言的群都该修好，得到 %v", res.Fixed)
	}
	if len(res.Fixed) != 2 {
		t.Errorf("只该修这两个群，得到 %v", res.Fixed)
	}
	if len(res.Pending) != 1 || !strings.Contains(res.Pending[0], "三号群") {
		t.Errorf("限时禁言该记为待到期，得到 %v", res.Pending)
	}

	// 被踢的：解封必须带 only_if_banned（否则等于把人踢出去再解封）。
	un := fake.LastCall("unbanChatMember")
	if un == nil || un["only_if_banned"] != true ||
		int64(un["chat_id"].(float64)) != -100 {
		t.Errorf("被踢的群应发 only_if_banned 的解封，得到 %v", un)
	}
	// 永久禁言的：发全开权限；限时那个群一个字都不该动。
	sawUnmute, touchedPending := false, false
	for _, p := range fake.Calls("restrictChatMember") {
		if int64(p["chat_id"].(float64)) == -300 {
			touchedPending = true
		}
		if perms, ok := p["permissions"].(map[string]any); ok &&
			perms["can_send_messages"] == true {
			sawUnmute = true
		}
	}
	if !sawUnmute {
		t.Error("永久禁言该发全开权限")
	}
	if touchedPending {
		t.Error("限时禁言还在窗口里，不该动手")
	}

	// 摘要要念得清楚：修好了哪些、哪些在等到期。
	sum := residualSummary(res)
	for _, want := range []string{"一号群", "二号群", "三号群", "已经解除"} {
		if !strings.Contains(sum, want) {
			t.Errorf("摘要里应提到 %q：\n%s", want, sum)
		}
	}
}

// TestResidualSweepSkipsActivePenalty：本人另有生效处罚的群不是残留，
// 不去问、也不动手。
func TestResidualSweepSkipsActivePenalty(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	fake := b.TG.(*testutil.FakeTG)
	testutil.EnableAntiad(t, b, -100)
	if _, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,prompt_tokens,completion_tokens,quota_cost,created_at,bot_id)
		VALUES (-100,556,9,'加微信','ad',0.99,'llm','scam','deleted_muted','自己的广告',
		 0,0,0,1700000000,?)`, b.BotID()); err != nil {
		t.Fatal(err)
	}

	res := residualSweep(b, 556)
	if res.Checked != 0 || len(res.Skipped) != 1 {
		t.Errorf("应记为跳过而不是核对，得到 %+v", res)
	}
	if n := fake.CountCalls("getChatMember"); n != 0 {
		t.Errorf("有生效处罚时不必问状态，问了 %d 次", n)
	}
	if !strings.Contains(residualSummary(res), "另有生效中的处罚") {
		t.Error("摘要里该说明这个群不在复查范围")
	}
}

// TestAppealEntrySweepsWhenNoPenalty：解封之后再来申诉（库里没有限制）
// 不该只回一句「没有限制」——顺手把各群核对一遍，有残留就解掉。
func TestAppealEntrySweepsWhenNoPenalty(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	fake := b.TG.(*testutil.FakeTG)
	testutil.EnableAntiad(t, b, -100)
	no := false
	fake.RespFunc = func(method string, p map[string]any) (string, bool) {
		if method == "getChatMember" {
			return memberJSON("restricted", &no, 0, true), true
		}
		return "", false
	}
	residualSwept.Delete(777)

	if !showAppealEntry(b, 777, 777, "appeal") {
		t.Fatal("带 payload 的申诉入口应被接管")
	}
	waitIdle(t, b)

	var texts []string
	for _, p := range fake.Calls("sendMessage") {
		texts = append(texts, fmt.Sprint(p["text"]))
	}
	joined := strings.Join(texts, "\n---\n")
	if !strings.Contains(joined, "核对一遍") {
		t.Errorf("先要告诉他去核对了：\n%s", joined)
	}
	if !strings.Contains(joined, "已经解除") || !strings.Contains(joined, "测试群") {
		t.Errorf("复查结果要说明哪个群修好了：\n%s", joined)
	}
	sawUnmute := false
	for _, p := range fake.Calls("restrictChatMember") {
		if perms, ok := p["permissions"].(map[string]any); ok &&
			perms["can_send_messages"] == true {
			sawUnmute = true
		}
	}
	if !sawUnmute {
		t.Error("复查发现残留禁言时应当场解开")
	}
}
