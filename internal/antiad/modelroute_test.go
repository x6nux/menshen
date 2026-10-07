package antiad

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"menshen/internal/core"
	"menshen/internal/testutil"
)

// addUpstream 登记一个只支持 systemone 的上游。
func addUpstream(t *testing.T, b *core.Bot, name, baseURL string) {
	t.Helper()
	if _, err := b.Store.Write.Exec(`INSERT INTO upstreams
		(name,base_url,api_key,weight,status,supports_chat,supports_systemone)
		VALUES (?,?,'k',1,1,0,1)`, name, baseURL); err != nil {
		t.Fatalf("登记上游 %s: %v", name, err)
	}
}

// addModel 登记一个价格全 0 的模型（价格只影响开销核算）。
func addModel(t *testing.T, b *core.Bot, name string) {
	t.Helper()
	if _, err := b.Store.Write.Exec(`INSERT INTO models (name,prompt_price,
		completion_price,cache_read_price,cache_write_price,enabled)
		VALUES (?,0,0,0,0,1)`, name); err != nil {
		t.Fatalf("登记模型 %s: %v", name, err)
	}
}

// TestBoundModelStripsUpstreamPrefix：模型名 <上游名>/<模型ID> 用于门神内部
// 标识，发给上游的 model 字段必须是剥掉前缀的模型 ID —— 上游不认我们的
// 命名前缀。判定结果里则记全名（换模型校准阈值时要知道是谁答的）。
func TestBoundModelStripsUpstreamPrefix(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	var body atomic.Value
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body.Store(string(raw))
		fakeAIOK(w, r)
	})
	setGlobal(t, b, "antiad_so_models", `["fake/so-model"]`)

	v, err := judgeSystemOne(b, b.Cache.Snap(), adState{}, soInstructions)
	if err != nil || !v.IsAd {
		t.Fatalf("绑定前缀的模型应能判定: v=%+v err=%v", v, err)
	}
	if v.Model != "fake/so-model" {
		t.Errorf("判定应记录全名，得到 %q", v.Model)
	}
	req, _ := body.Load().(string)
	if !strings.Contains(req, `"model":"so-model"`) {
		t.Errorf("发给上游的 model 应是剥掉前缀的 ID: %s", req)
	}
	if strings.Contains(req, "fake/so-model") {
		t.Errorf("上游不该看到带前缀的模型名: %s", req)
	}
}

// TestModelListTriesNextOn4xx：4xx 对同一个模型是配置问题，但列表里还有
// 未试过的模型时，换一个可能成功 —— 不应因一条 4xx 让整次判定失败。
func TestModelListTriesNextOn4xx(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	var hits1, hits2 atomic.Int32
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		hits1.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv1.Close()
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits2.Add(1)
		fakeAIOK(w, r)
	}))
	defer srv2.Close()

	addUpstream(t, b, "up1", srv1.URL)
	addUpstream(t, b, "up2", srv2.URL)
	addModel(t, b, "up1/m1")
	addModel(t, b, "up2/m2")
	setGlobal(t, b, "antiad_so_models", `["up1/m1","up2/m2"]`)

	v, err := judgeSystemOne(b, b.Cache.Snap(), adState{}, soInstructions)
	if err != nil {
		t.Fatalf("第二个模型应兜住: %v", err)
	}
	if v.Model != "up2/m2" {
		t.Errorf("命中模型应为 up2/m2，得到 %q", v.Model)
	}
	if n := hits1.Load(); n != 1 {
		t.Errorf("up1 应只试一次，实际 %d 次", n)
	}
	if n := hits2.Load(); n != 1 {
		t.Errorf("up2 应只试一次，实际 %d 次", n)
	}
}

// TestMissingBoundUpstreamSkipsToNext：绑定的上游不存在/停用/不支持该端点时，
// 这一路配置上就走不通，应立刻切下一个模型，不消耗退避时间。
func TestMissingBoundUpstreamSkipsToNext(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	fakeAI(t, b, nil)
	addModel(t, b, "ghost/m0")
	addModel(t, b, "fake/so-model")
	setGlobal(t, b, "antiad_so_models", `["ghost/m0","fake/so-model"]`)

	v, err := judgeSystemOne(b, b.Cache.Snap(), adState{}, soInstructions)
	if err != nil {
		t.Fatalf("缺失上游的模型应被跳过: %v", err)
	}
	if v.Model != "fake/so-model" {
		t.Errorf("命中模型应为 fake/so-model，得到 %q", v.Model)
	}
}

// TestUpstreamTroubleAlertThrottled：连续失败达到阈值告警一次，冷却期内
// 不重复；任何一次成功清零计数 —— 否则偶发抖动会持续累积，最终集中
// 触发一条难以解读的告警。
func TestUpstreamTroubleAlertThrottled(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	var fail atomic.Bool
	fail.Store(true)
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if fail.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fakeAIOK(w, r)
	})
	setGlobal(t, b, "antiad_so_models", `["fake/so-model"]`)
	setGlobal(t, b, "antiad_upstream_alert_after", "2")
	setGlobal(t, b, "antiad_upstream_alert_minutes", "30")

	judge := func() error {
		_, err := judgeSystemOne(b, b.Cache.Snap(), adState{}, soInstructions)
		return err
	}

	if err := judge(); err == nil {
		t.Fatal("上游 401 时应当失败")
	}
	if n := fake.CountCalls("sendMessage"); n != 0 {
		t.Errorf("第一次失败不该告警，发了 %d 条", n)
	}
	if err := judge(); err == nil {
		t.Fatal("上游 401 时应当失败")
	}
	if n := fake.CountCalls("sendMessage"); n != 1 {
		t.Errorf("连续两次失败应告警一条，发了 %d 条", n)
	}
	if err := judge(); err == nil {
		t.Fatal("上游 401 时应当失败")
	}
	if n := fake.CountCalls("sendMessage"); n != 1 {
		t.Errorf("冷却期内不该重复告警，发了 %d 条", n)
	}

	// 成功一次清零：恢复后再失败一次，不到阈值就不该有新告警。
	fail.Store(false)
	if err := judge(); err != nil {
		t.Fatalf("上游恢复后应判定成功: %v", err)
	}
	fail.Store(true)
	if err := judge(); err == nil {
		t.Fatal("上游 401 时应当失败")
	}
	if n := fake.CountCalls("sendMessage"); n != 1 {
		t.Errorf("成功应清零连续失败计数，发了 %d 条", n)
	}
}
