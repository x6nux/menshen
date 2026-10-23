package antiad

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"menshen/internal/testutil"
)

// TestSoSimpleInstructionsByModel：初判提示词简化按模型标记（models 表的
// simple_instructions），不按 bot —— 判定列表里标没标混着时，重试换模型
// 各按各的生效，不会把 A 模型的开关带到 B 上（questions 是共享 map，
// 替换若不拷贝就会污染后续尝试）。
func TestSoSimpleInstructionsByModel(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	var mu sync.Mutex
	var bodies []string
	var failFirst atomic.Bool
	failFirst.Store(true)
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		// 第一次请求失败（可重试），驱动 aiCall 换到列表里的下一个模型。
		if failFirst.CompareAndSwap(true, false) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"answers":{"is_ad":{"choice":"clean","confidence":0.9}}}`))
	})
	if _, err := b.Store.Write.Exec(`INSERT INTO models
		(name,prompt_price,completion_price,cache_read_price,cache_write_price,enabled,simple_instructions)
		VALUES ('fake/so-s',0,0,0,0,1,1), ('fake/so-l',0,0,0,0,1,0)`); err != nil {
		t.Fatalf("登记模型失败: %v", err)
	}
	setGlobal(t, b, "antiad_so_models", `["fake/so-s","fake/so-l"]`)

	if _, err := judgeSystemOne(b, b.Cache.Snap(), adState{}, soInstructions); err != nil {
		t.Fatalf("judgeSystemOne: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) < 2 {
		t.Fatalf("应有一次失败重试共两路请求，得到 %d", len(bodies))
	}
	// 第一路：标记过的 so-s → 一句话。
	if !strings.Contains(bodies[0], "Is this group message or account advertising") {
		t.Errorf("标记过的模型应发简版提示词: %s", truncateTestBody(bodies[0]))
	}
	// 第二路：没标记的 so-l → 完整提示词；若替换污染了共享 map，这里会变短。
	if !strings.Contains(bodies[1], "Judge it together with the sender profile") {
		t.Errorf("未标记的模型应发完整提示词（共享 map 被污染？）: %s", truncateTestBody(bodies[1]))
	}
}

// TestSoSimpleInstructionsOffByDefault：没标记的模型维持完整提示词。
func TestSoSimpleInstructionsOffByDefault(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	var mu sync.Mutex
	var bodies []string
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		w.Write([]byte(`{"answers":{"is_ad":{"choice":"clean","confidence":0.9}}}`))
	})
	if _, err := b.Store.Write.Exec(`INSERT INTO models
		(name,prompt_price,completion_price,cache_read_price,cache_write_price,enabled,simple_instructions)
		VALUES ('fake/jev-1.13',0,0,0,0,1,0)`); err != nil {
		t.Fatalf("登记模型失败: %v", err)
	}
	setGlobal(t, b, "antiad_so_models", `["fake/jev-1.13"]`)

	if _, err := judgeSystemOne(b, b.Cache.Snap(), adState{}, soInstructions); err != nil {
		t.Fatalf("judgeSystemOne: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) == 0 || !strings.Contains(bodies[0], "Judge it together with the sender profile") {
		t.Errorf("未标记的模型应发完整提示词: %s", truncateTestBody(strings.Join(bodies, "")))
	}
}

// truncateTestBody 把请求体裁短到可读长度，错误信息里不刷屏。
func truncateTestBody(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
