package antiad

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// dispatch 是本包测试用的 Update 分发函数。
//
// 真正的分发位于 main 包（需同时访问 antiad 与 panel），内部包的测试
// 无法引用，只能复制与本包相关的分支。私聊与回调分支刻意不实现：
// 它们通向 panel，本包测试不应依赖。
//
// 跨层的完整分发由顶层的 flow_test.go 覆盖。
func dispatch(b *core.Bot, u *tg.Update) {
	switch {
	case u.Message != nil && u.Message.From != nil && u.Message.Chat != nil:
		if u.Message.Chat.Type != "" && u.Message.Chat.Type != "private" {
			HandleGroupMessage(b, u.Message)
		}
	case u.EditedMessage != nil && u.EditedMessage.From != nil && u.EditedMessage.Chat != nil:
		if u.EditedMessage.Chat.Type != "" && u.EditedMessage.Chat.Type != "private" {
			HandleGroupMessage(b, u.EditedMessage)
		}
	case u.ChatMember != nil:
		HandleChatMemberUpdate(b, u.ChatMember)
	case u.MyChatMember != nil:
		HandleMyChatMemberUpdate(b, u.MyChatMember)
	}
}

// fakeAI 起一个假 AI 上游，登记成 chat 与 systemone 两个端点都开的渠道，
// 并把两级模型配成 so-model / llm-model。h 为 nil 时用 fakeAIOK。
//
// 走真实的 aiCall → HTTP 往返，而不是替换判定函数：选路、重试、响应解析
// 这几段恰恰是最容易静默出错的地方。
func fakeAI(t *testing.T, b *core.Bot, h http.HandlerFunc) {
	t.Helper()
	if h == nil {
		h = fakeAIOK
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// 同一个测试里换一种响应时，旧的假上游要先撤掉，否则两个一起参与选路。
	if _, err := b.Store.Write.Exec(`DELETE FROM upstreams WHERE name='fake'`); err != nil {
		t.Fatalf("撤掉旧的假上游失败: %v", err)
	}
	if _, err := b.Store.Write.Exec(`INSERT INTO upstreams
		(name,base_url,api_key,weight,status,supports_chat,supports_systemone)
		VALUES ('fake',?,'k',1,1,1,1)`, srv.URL); err != nil {
		t.Fatalf("登记假上游失败: %v", err)
	}
	// PutSetting 自带 reload，上面那行上游也随之进快照。
	for k, v := range map[string]string{
		"antiad_so_model": "so-model", "antiad_llm_model": "llm-model"} {
		if err := b.PutSetting(k, v); err != nil {
			t.Fatalf("putSetting %s: %v", k, err)
		}
	}
}

// fakeAIOK 是假上游的默认响应：systemone 判 93% 广告，大模型判 90% 广告。
func fakeAIOK(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/systemone") {
		w.Write([]byte(`{"answers":{"is_ad":{"choice":"ad","confidence":0.93},` +
			`"ad_kind":{"choice":"scam"},"severity":{"score":2}}}`))
		return
	}
	w.Write([]byte(llmReply(true, 0.9, "scam", "message")))
}

// waitIdle 等判定队列与复判队列都跑空。判定是异步的，断言前必须等它落地。
func waitIdle(t *testing.T, b *core.Bot) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for b.AdBusy() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("判定队列 5 秒内没跑空（剩 %d）", b.AdBusy())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// soReply 造一条 systemone 响应。
func soReply(choice string, conf float64, kind, scope string) string {
	return soReplySev(choice, conf, kind, scope, 2)
}

// soReplySev 同 soReply，但自定义危害度：高危害不受资历豁免那条要用它区分
// 普通广告与色情/诈骗这类高危害。
func soReplySev(choice string, conf float64, kind, scope string, sev float64) string {
	return `{"answers":{"is_ad":{"choice":"` + choice + `","confidence":` +
		strconv.FormatFloat(conf, 'f', -1, 64) + `},"ad_kind":{"choice":"` + kind +
		`"},"ad_scope":{"choice":"` + scope + `"},"severity":{"score":` +
		strconv.FormatFloat(sev, 'f', -1, 64) + `}}}`
}

// llmReply 造一条大模型（非流式形状）响应。
//
// 判广告时带上 evidence：取证词是测试群标题，必然出现在判定对象的
// 语料里（见 evidenceCorpus）。否则一致性门会把所有测试结论降级，
// 测试的就不是目标链路。门本身的拦截与改正重试由
// evidence_gate_test.go 用自造响应专测。
func llmReply(isAd bool, conf float64, kind, scope string) string {
	inner, _ := json.Marshal(map[string]any{"is_ad": isAd, "confidence": conf,
		"kind": kind, "scope": scope, "reason": "测试", "evidence": testEvidence})
	out, _ := json.Marshal(map[string]any{"choices": []any{
		map[string]any{"message": map[string]any{"content": string(inner)}}}})
	return string(out)
}

// testEvidence 是测试罐头响应里的证据词：testutil 建的群标题，必然在语料里。
var testEvidence = []string{"测试群"}

// testAdState 是直接调判定函数（judgeLLM / judgeSystemOne）的测试用的 state：
// 只带一个群标题，让罐头响应里的 testEvidence 能通过一致性门。走完整链路的
// 测试用 testutil.GroupMsg 造消息，标题同样会进 state。
func testAdState() adState { return adState{Chat: adChatInfo{Title: "测试群"}} }

// fakeAIWith 起假上游：systemone 与大模型各回固定内容，并各自计数。
func fakeAIWith(t *testing.T, b *core.Bot, so, llm string) (soN, llmN *atomic.Int32) {
	t.Helper()
	soN, llmN = new(atomic.Int32), new(atomic.Int32)
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/systemone") {
			soN.Add(1)
			w.Write([]byte(so))
			return
		}
		llmN.Add(1)
		w.Write([]byte(llm))
	})
	return soN, llmN
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// checkEdits 取 /check 多步响应里 editMessageText 的全部文本（按编辑顺序）。
// 初始那条 sendMessage 不在这里：它只带当前状态，判定小节靠逐步编辑追加。
func checkEdits(fake *testutil.FakeTG) []string {
	var out []string
	for _, p := range fake.Calls("editMessageText") {
		out = append(out, fmt.Sprint(p["text"]))
	}
	return out
}
