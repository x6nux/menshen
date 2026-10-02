package antiad

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/upstream"
)

// cfDecisionOK 是一条成功的 Clef 响应，套在 Cloudflare 信封里。
const cfDecisionOK = `{"result":{` +
	`"model":"clef",` +
	`"answers":{` +
	`"is_ad":{"type":"choice","choice":"ad","probabilities":{"ad":0.9,"clean":0.1},"confidence":0.9},` +
	`"ad_kind":{"type":"choice","choice":"scam","probabilities":{"scam":1},"confidence":1},` +
	`"ad_scope":{"type":"choice","choice":"account","probabilities":{"account":1},"confidence":1},` +
	`"severity":{"type":"score","score":2.5,"legend":{},"probabilities":{"0":0.1,"1":0.4},"confidence":0.8}},` +
	`"usage":{"input_tokens":100,"output_tokens":10}},` +
	`"success":true,"errors":[],"messages":[]}`

// registerCF 登记一个 Cloudflare 渠道：base_url 指到假服务器，模型名是
// 完整 CF 模型 ID，价格 0.24/M。
func registerCF(t *testing.T, b *core.Bot, base string) {
	t.Helper()
	if _, err := b.Store.Write.Exec(`INSERT INTO upstreams
		(name,base_url,api_key,weight,status,supports_chat,supports_systemone,kind)
		VALUES ('cfc',?,'cf-key',1,1,0,1,'cloudflare')`, base); err != nil {
		t.Fatalf("登记 Cloudflare 上游失败: %v", err)
	}
	if _, err := b.Store.Write.Exec(`INSERT INTO models
		(name,prompt_price,completion_price,cache_read_price,cache_write_price,enabled)
		VALUES ('cfc/@cf/cloudflare/clef',0.24,0,0,0,1)`); err != nil {
		t.Fatalf("登记模型失败: %v", err)
	}
	if err := b.PutSetting("antiad_so_model", "cfc/@cf/cloudflare/clef"); err != nil {
		t.Fatalf("putSetting: %v", err)
	}
}

// TestCloudflareDecisionChannel：Cloudflare 渠道的主判定要走
// {base}/ai/run/{模型ID}；请求体的 model 只带 Clef 选择子（clef），
// 响应里的 CF 信封要被拆掉，answers / usage 照常解析进判定与计费。
func TestCloudflareDecisionChannel(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)

	var gotPath, gotAuth, gotBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath.Store(r.URL.Path)
		gotAuth.Store(r.Header.Get("Authorization"))
		raw, _ := io.ReadAll(r.Body)
		gotBody.Store(string(raw))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, cfDecisionOK)
	}))
	defer srv.Close()

	registerCF(t, b, srv.URL+"/client/v4/accounts/acc123")

	v, err := judgeSystemOne(b, b.Cache.Snap(), adState{}, soInstructions)
	if err != nil {
		t.Fatalf("CF 渠道判定失败: %v", err)
	}
	if !v.IsAd || v.Kind != "scam" || v.Scope != "account" || v.Severity != 2.5 {
		t.Errorf("判定结果不对: %+v", v)
	}
	if v.Model != "cfc/@cf/cloudflare/clef" {
		t.Errorf("模型名不对: %q", v.Model)
	}
	// 用量从 result.usage 里来，价格 0.24/M × 100 token = 12 quota。
	if v.Usage.PromptTokens != 100 || v.Usage.CompletionTokens != 10 {
		t.Errorf("用量应来自 result.usage: %+v", v.Usage)
	}
	if v.Cost != 12 {
		t.Errorf("成本 = %d quota，期望 12", v.Cost)
	}

	if p := gotPath.Load().(string); p != "/client/v4/accounts/acc123/ai/run/@cf/cloudflare/clef" {
		t.Errorf("请求路径 = %q", p)
	}
	if a := gotAuth.Load().(string); a != "Bearer cf-key" {
		t.Errorf("鉴权头 = %q", a)
	}
	var req struct {
		Model     string                     `json:"model"`
		State     json.RawMessage            `json:"state"`
		Questions map[string]json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal([]byte(gotBody.Load().(string)), &req); err != nil {
		t.Fatalf("请求体不是 JSON: %v", err)
	}
	if req.Model != "clef" {
		t.Errorf("请求体 model 应为 clef，得到 %q", req.Model)
	}
	if len(req.Questions) != 4 {
		t.Errorf("应带 4 个问题，得到 %d", len(req.Questions))
	}

	// Cloudflare 渠道对 chat 端点不可见：不会被复判链路选中。
	if ups := upstreamFor(b.Cache.Snap(), upstream.EPChat, "cfc/@cf/cloudflare/clef"); len(ups) != 0 {
		t.Errorf("CF 渠道不该支持 chat 端点，得到 %d 个候选", len(ups))
	}
}

// TestCloudflareEnvelopeFailure：HTTP 200 但 success=false 时必须报错，
// 不能把空 result 当成「判定为正常」静默放行。
func TestCloudflareEnvelopeFailure(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"result":null,"success":false,`+
			`"errors":[{"code":5004,"message":"Invalid data type"}],"messages":[]}`)
	}))
	defer srv.Close()
	registerCF(t, b, srv.URL)

	if _, err := judgeSystemOne(b, b.Cache.Snap(), adState{}, soInstructions); err == nil ||
		!strings.Contains(err.Error(), "Invalid data type") {
		t.Fatalf("success=false 应报出可读错误，得到 %v", err)
	}
}

// TestCloudflareHTTPErrorUsesEnvelopeMessage:CF 的 4xx/5xx 错误正文也是
// 信封，错误文案应取 errors[0].message 而不是整段 JSON。
func TestCloudflareHTTPErrorUsesEnvelopeMessage(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"result":null,"success":false,`+
			`"errors":[{"code":10000,"message":"Authentication error"}],"messages":[]}`)
	}))
	defer srv.Close()
	registerCF(t, b, srv.URL)

	if _, err := judgeSystemOne(b, b.Cache.Snap(), adState{}, soInstructions); err == nil ||
		!strings.Contains(err.Error(), "Authentication error") {
		t.Fatalf("401 应报出信封里的原因，得到 %v", err)
	}
}
