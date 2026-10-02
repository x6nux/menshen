package antiad

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"menshen/internal/testutil"
)

// TestGeminiChatChannel：chat-only 渠道经协议适配层跑通复判 —— 请求体是
// generateContent 形状、鉴权走 x-goog-api-key、强制非流式；响应翻译回
// chat/completions 形状后，复判解析与用量计提照常工作。
func TestGeminiChatChannel(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)

	const verdict = `{"is_ad":true,"confidence":0.9,"kind":"scam","scope":"message","severity":2,"reason":"测试"}`
	var gotPath, gotKey, gotBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath.Store(r.URL.Path)
		gotKey.Store(r.Header.Get("x-goog-api-key"))
		raw, _ := io.ReadAll(r.Body)
		gotBody.Store(string(raw))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":`+strconv.Quote(verdict)+
			`}]}}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20}}`)
	}))
	defer srv.Close()

	if _, err := b.Store.Write.Exec(`INSERT INTO upstreams
		(name,base_url,api_key,weight,status,supports_chat,supports_systemone,kind)
		VALUES ('gem',?,'gk',1,1,1,0,'gemini')`, srv.URL); err != nil {
		t.Fatalf("登记 Gemini 上游失败: %v", err)
	}
	if _, err := b.Store.Write.Exec(`INSERT INTO models
		(name,prompt_price,completion_price,cache_read_price,cache_write_price,enabled)
		VALUES ('gem/gemini-2.5-flash',0.1,0.4,0,0,1)`); err != nil {
		t.Fatalf("登记模型失败: %v", err)
	}
	if err := b.PutSetting("antiad_llm_model", "gem/gemini-2.5-flash"); err != nil {
		t.Fatalf("putSetting: %v", err)
	}

	v, err := judgeLLM(b, b.Cache.Snap(), adState{}, adVerdict{}, soInstructions)
	if err != nil {
		t.Fatalf("Gemini 渠道复判失败: %v", err)
	}
	if !v.IsAd || v.Kind != "scam" || v.Model != "gem/gemini-2.5-flash" {
		t.Errorf("复判结果不对: %+v", v)
	}
	// 100 × $0.1/M + 20 × $0.4/M = $0.000018 → 9 quota。
	if v.Usage.PromptTokens != 100 || v.Usage.CompletionTokens != 20 || v.Cost != 9 {
		t.Errorf("用量/成本不对: usage=%+v cost=%d", v.Usage, v.Cost)
	}

	if p := gotPath.Load().(string); p != "/models/gemini-2.5-flash:generateContent" {
		t.Errorf("请求路径 = %q", p)
	}
	if k := gotKey.Load().(string); k != "gk" {
		t.Errorf("x-goog-api-key = %q", k)
	}
	body := gotBody.Load().(string)
	if !strings.Contains(body, `"systemInstruction"`) {
		t.Errorf("请求体应带 systemInstruction: %s", body)
	}
	if strings.Contains(body, `"stream"`) {
		t.Errorf("请求体不该带 stream: %s", body)
	}
	if !strings.Contains(body, `"temperature":0`) {
		t.Errorf("判定请求的 temperature 应为 0: %s", body)
	}
}
