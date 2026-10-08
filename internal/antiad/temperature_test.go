package antiad

// 判定链路遇到「模型不接受 temperature」时的一次自愈：上游以 400 拒绝该
// 参数，本服务记下这个模型并立即重发（不带该参数），后续请求也不再带。

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

// TestChatDropsRejectedTemperature：第一次被 400 拒绝 temperature，去掉该
// 参数重发成功；第二次同一模型直接不带它（已记住），不再浪费一次 400。
func TestChatDropsRejectedTemperature(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	const model = "hot/no-temp-model"

	var withTemp, without atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), `"temperature"`) {
			withTemp.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"` + "`temperature`" +
				` is deprecated for this model"}}`))
			return
		}
		without.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(llmReply(true, 0.9, "scam", "message")))
	}))
	defer srv.Close()

	if _, err := b.Store.Write.Exec(`INSERT INTO upstreams
		(name,base_url,api_key,weight,status,supports_chat,supports_systemone)
		VALUES ('hot',?,'k',1,1,1,0)`, srv.URL); err != nil {
		t.Fatalf("登记上游失败: %v", err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	payload := map[string]any{
		"temperature": 0,
		"messages":    []map[string]string{{"role": "user", "content": "ping"}},
	}
	if _, err := aiCall(b.Shared, upstream.EPChat, []string{model}, payload, nil); err != nil {
		t.Fatalf("被拒 temperature 后应去掉该参数重发成功: %v", err)
	}
	if withTemp.Load() != 1 || without.Load() != 1 {
		t.Errorf("应 1 次带参数（被拒）+ 1 次不带参数，得到 %d / %d",
			withTemp.Load(), without.Load())
	}

	// 第二次：模型已被记下，请求直接不带 temperature。
	if _, err := aiCall(b.Shared, upstream.EPChat, []string{model}, payload, nil); err != nil {
		t.Fatalf("第二次应直接成功: %v", err)
	}
	if withTemp.Load() != 1 {
		t.Errorf("被拒过的模型不该再带 temperature，又带了 %d 次", withTemp.Load())
	}
	if without.Load() != 2 {
		t.Errorf("应共 2 次不带参数的请求，得到 %d", without.Load())
	}
}
