package antiad

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"menshen/internal/testutil"
)

// TestPunishedMessageLeavesContext：被判广告处置过的消息必须退出后续判定的
// recent_context。它已从群里删除，却会一直被模型当成此人刚发的广告，
// 使新消息被反复处罚 —— 同一条消息只应计入一次。
// /check 复查不受影响：全量历史里仍能看到判过的消息，那是账号证据。
func TestPunishedMessageLeavesContext(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)

	var mu sync.Mutex
	var soBodies [][]byte
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/systemone") {
			mu.Lock()
			soBodies = append(soBodies, body)
			n := len(soBodies)
			mu.Unlock()
			// 第一次判广告（危害度 0：不进联合封禁名单，第二条消息才能
			// 正常进入判定），第二次判正常。
			if n == 1 {
				w.Write([]byte(soReplySev("ad", 0.93, "scam", "message", 0)))
			} else {
				w.Write([]byte(soReplySev("clean", 0.96, "none", "message", 0)))
			}
			return
		}
		w.Write([]byte(llmReply(true, 0.93, "scam", "message")))
	})

	// 第一条：广告，删除 + 禁言，留底行标成已处罚。
	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 1, "日入过万 加微信详谈"))
	waitIdle(t, b)

	var punished int64
	if err := b.Store.Read.QueryRow(`SELECT punished FROM group_messages
		WHERE chat_id=-100 AND message_id=1`).Scan(&punished); err != nil {
		t.Fatalf("第一条消息没有留底: %v", err)
	}
	if punished != 1 {
		t.Error("处置过的消息应标成 punished=1")
	}
	if fake.CountCalls("deleteMessage") == 0 {
		t.Error("广告消息应已被删除")
	}

	// 第二条：普通发言。判定载荷里的 recent_context 不该再出现第一条。
	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 2, "群里有人懂软路由吗"))
	waitIdle(t, b)

	mu.Lock()
	defer mu.Unlock()
	if len(soBodies) != 2 {
		t.Fatalf("应有两次 systemone 判定，实际 %d 次", len(soBodies))
	}
	var req struct {
		State struct {
			RecentContext []struct {
				Text string `json:"text"`
			} `json:"recent_context"`
		} `json:"state"`
	}
	if err := json.Unmarshal(soBodies[1], &req); err != nil {
		t.Fatalf("第二次判定请求无法解析: %v", err)
	}
	for _, c := range req.State.RecentContext {
		if strings.Contains(c.Text, "日入过万") {
			t.Errorf("已处罚的消息不该再进 recent_context，得到 %q", c.Text)
		}
	}
	// 复查口径不过滤：判过的消息仍是这个账号的历史证据。
	if n := countRows(t, b, `SELECT COUNT(*) FROM group_messages
		WHERE chat_id=-100 AND user_id=42`); n != 2 {
		t.Errorf("留底应仍是 2 条，实际 %d", n)
	}
}

// TestMarkPunishedFiltersContext：markPunished 的批量与零值行为，以及
// loadUserMessages 两种口径 —— 复查全量、上下文排除已处罚。
func TestMarkPunishedFiltersContext(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	for i := int64(1); i <= 3; i++ {
		recordMessage(b, -100, i, 42, fmt.Sprintf("第%d条", i), 1700000000+i, "")
	}

	if n := len(loadUserMessages(b.Store, -100, 42, 10, false)); n != 3 {
		t.Fatalf("复查口径应有 3 条，实际 %d", n)
	}
	if n := len(loadUserMessages(b.Store, -100, 42, 10, true)); n != 3 {
		t.Fatalf("未处罚时上下文口径也应有 3 条，实际 %d", n)
	}

	// 连带删除/相册形状的批量标记；0 是没有消息可删的占位，必须跳过
	// 而不是拼出非法 SQL。
	markPunished(b, -100, 0, 2)
	if got := len(loadUserMessages(b.Store, -100, 42, 10, true)); got != 2 {
		t.Errorf("标记一条后上下文口径应剩 2 条，实际 %d", got)
	}
	if got := len(loadUserMessages(b.Store, -100, 42, 10, false)); got != 3 {
		t.Errorf("复查口径不受标记影响，应仍 3 条，实际 %d", got)
	}

	// recentOwn 走的就是排除口径：当前消息是第 4 条，历史里只剩两条未处罚的。
	recordMessage(b, -100, 4, 42, "第四条", 1700000004, "")
	ctx := recentOwn(b.Store, testutil.GroupMsg(-100, 42, 4, "第四条"), 6)
	if len(ctx) != 2 {
		t.Fatalf("recentOwn 应只剩未处罚的 2 条，实际 %d", len(ctx))
	}
	for _, c := range ctx {
		if strings.Contains(c.Text, "第二条") {
			t.Errorf("已处罚的消息不该出现在 recentOwn，得到 %q", c.Text)
		}
	}
}

// TestPurgeMarksKeptMessages：连带删除（账号判成广告号）会把此人近期的消息
// 全部删掉，这些留底同样要标成已处罚 —— 否则其解禁后，这些消息仍留在上下文里。
func TestPurgeMarksKeptMessages(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	now := time.Now().Unix()
	for i := int64(1); i <= 3; i++ {
		recordMessage(b, -100, i, 42, fmt.Sprintf("历史第%d条", i), now-int64(4-i)*60, "")
	}

	m := testutil.GroupMsg(-100, 42, 2, "广告正文")
	ApplyAction(b, m, adAction{Delete: true, Purge: true, Name: "deleted_muted"}, false)

	if n := fake.CountCalls("deleteMessages"); n == 0 {
		t.Error("连带删除应发起批量删除")
	}
	if got := len(loadUserMessages(b.Store, -100, 42, 10, true)); got != 0 {
		t.Errorf("连带删除后上下文口径应为 0 条，实际 %d", got)
	}
	if got := len(loadUserMessages(b.Store, -100, 42, 10, false)); got != 3 {
		t.Errorf("复查口径仍应看到全部 3 条，实际 %d", got)
	}
}
