package antiad

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"menshen/internal/testutil"
)

// TestRunAdDigestSingleFlight：面板「立即重新总结」每次点击都会起一轮，
// 重复点击/定时任务撞上时不得并发跑两轮（会双倍调大模型并竞态写摘要与游标）。
func TestRunAdDigestSingleFlight(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	var calls atomic.Int32
	release := make(chan struct{})
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-release
		w.Write([]byte(`{"choices":[{"message":{"content":` +
			`"【近期广告形态】\n① 测试形态"}}]}`))
	})
	if _, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,created_at,bot_id)
		VALUES (-100,555,1,'加微信','ad',0.9,'so','scam','deleted','',?,?)`,
		time.Now().Unix(), b.BotID()); err != nil {
		t.Fatal(err)
	}

	first := make(chan struct{})
	go func() {
		RunAdDigest(b.Shared, true)
		close(first)
	}()
	// 等第一轮真的进了上游。
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("第一轮应发出一次请求，得到 %d", got)
	}

	// 第二轮应被单飞挡下并立即返回。
	second := make(chan struct{})
	go func() {
		RunAdDigest(b.Shared, true)
		close(second)
	}()
	select {
	case <-second:
	case <-time.After(2 * time.Second):
		t.Fatal("重复触发应被单飞挡下并立即返回")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("重复触发不该再发请求，得到 %d", got)
	}

	close(release)
	select {
	case <-first:
	case <-time.After(3 * time.Second):
		t.Fatal("第一轮没有跑完")
	}
	// 单飞标记必须释放，否则此后永远不会再总结。
	deadline = time.Now().Add(2 * time.Second)
	for digestRunning.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if digestRunning.Load() {
		t.Error("单飞标记没有在结束后释放")
	}
}

// TestRunAdDigestAppliesFixText：修正文本要进总结的**系统**提示词 ——
// 与样本分开：样本是攻击者可控的数据（围栏内，明确不得当指令），
// 修正文本是主管理员写的口径说明，是真正要执行的那部分。
func TestRunAdDigestAppliesFixText(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	var req atomic.Value
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		req.Store(string(raw))
		w.Write([]byte(`{"choices":[{"message":{"content":` +
			`"【近期广告形态】\n① 测试形态"}}]}`))
	})
	if _, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,created_at,bot_id)
		VALUES (-100,555,1,'加微信','ad',0.9,'so','scam','deleted','',?,?)`,
		time.Now().Unix(), b.BotID()); err != nil {
		t.Fatal(err)
	}
	const fix = "技术讨论里出现的 GitHub、npm 链接不算广告"
	if err := b.PutSetting("antiad_digest_fix", fix); err != nil {
		t.Fatal(err)
	}

	RunAdDigest(b.Shared, true)

	raw, _ := req.Load().(string)
	if raw == "" {
		t.Fatal("总结没有发出请求")
	}
	var sent struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(raw), &sent); err != nil {
		t.Fatalf("解析请求失败: %v\n%s", err, raw)
	}
	if len(sent.Messages) < 2 || sent.Messages[0].Role != "system" {
		t.Fatalf("应有 system + user 两条消息，得到 %+v", sent.Messages)
	}
	if !strings.Contains(sent.Messages[0].Content, fix) {
		t.Errorf("修正文本应在系统提示词里：\n%s", sent.Messages[0].Content)
	}
	if strings.Contains(sent.Messages[1].Content, fix) {
		t.Error("修正文本不该混进样本（那是数据区，会被围栏隔离）")
	}
	// 摘要照常落库：修正文本不该挤掉输出。
	if d := b.Cache.Snap().Setting("antiad_digest"); !strings.Contains(d, "测试形态") {
		t.Errorf("摘要应照常写入，得到 %q", d)
	}
}
