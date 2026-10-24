package antiad

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"menshen/internal/testutil"
	"menshen/internal/upstream"
)

// TestChatDropsRejectedCacheControl：cache_control 标记默认启用；上游以
// 4xx 拒绝该字段时，记下这个上游并立即去掉标记重发（与 temperature 同款
// 自愈），判定不多花一个往返以上的代价。
func TestChatDropsRejectedCacheControl(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	upstream.ClearCacheControlUnsupported("hotcc")

	var withMark, without atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), `"cache_control"`) {
			withMark.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"cache_control is not supported by this relay"}}`))
			return
		}
		without.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(llmReply(true, 0.9, "scam", "message")))
	}))
	defer srv.Close()

	if _, err := b.Store.Write.Exec(`INSERT INTO upstreams
		(name,base_url,api_key,weight,status,supports_chat,supports_systemone)
		VALUES ('hotcc',?,'k',1,1,1,0)`, srv.URL); err != nil {
		t.Fatalf("登记上游失败: %v", err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	payload := map[string]any{
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": llmSystemPrompt},
			{"role": "user", "content": "{}"},
		},
	}
	if _, err := aiCall(b.Shared, upstream.EPChat, []string{"hotcc/claude"}, payload, nil); err != nil {
		t.Fatalf("被拒 cache_control 后应去掉该标记重发成功: %v", err)
	}
	if withMark.Load() != 1 || without.Load() != 1 {
		t.Errorf("应 1 次带标记（被拒）+ 1 次不带标记，得到 %d / %d",
			withMark.Load(), without.Load())
	}

	// 第二次：上游已被记下，请求直接不带标记。
	if _, err := aiCall(b.Shared, upstream.EPChat, []string{"hotcc/claude"}, payload, nil); err != nil {
		t.Fatalf("第二次应直接成功: %v", err)
	}
	if withMark.Load() != 1 || without.Load() != 2 {
		t.Errorf("被拒过的上游不该再带标记，得到 %d / %d",
			withMark.Load(), without.Load())
	}
}
