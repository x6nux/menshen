package antiad

import (
	"net/http"
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
