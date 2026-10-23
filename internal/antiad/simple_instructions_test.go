package antiad

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"menshen/internal/testutil"
)

// TestSoSimpleInstructions：antiad_so_simple_instructions 打开时，初判请求
// 的 is_ad instructions 换成一句话（微调初判模型不吃长口径，10k 字符的
// 提示词对它是纯载荷负担）；关闭时维持完整提示词。复判提示词不受影响。
func TestSoSimpleInstructions(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	var mu sync.Mutex
	var bodies []string
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/systemone") {
			mu.Lock()
			bodies = append(bodies, string(raw))
			mu.Unlock()
			w.Write([]byte(`{"answers":{"is_ad":{"choice":"clean","confidence":0.9}}}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":` +
			`"{\"is_ad\":false,\"confidence\":0.9,\"reason\":\"正常\"}"}}]}`))
	})
	setGlobal(t, b, "antiad_so_models", `["fake/jev-1.13"]`)
	setGlobal(t, b, "antiad_llm_models", `["fake/jev-1.13"]`)

	assertLast := func(wantSimple bool) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if len(bodies) == 0 {
			t.Fatal("没有捕获到初判请求")
		}
		body := bodies[len(bodies)-1]
		simple := strings.Contains(body, "Is this group message or account advertising")
		long := strings.Contains(body, "Judge it together with the sender profile")
		if wantSimple && (!simple || long) {
			t.Errorf("开关打开时 instructions 应为一句简版，got simple=%v long=%v body=%s",
				simple, long, truncateTestBody(body))
		}
		if !wantSimple && (simple || !long) {
			t.Errorf("开关关闭时应为完整提示词，got simple=%v long=%v body=%s",
				simple, long, truncateTestBody(body))
		}
	}

	// 默认关：完整提示词。
	if _, err := judgeSystemOne(b, b.Cache.Snap(), adState{}, soInstructions); err != nil {
		t.Fatalf("judgeSystemOne: %v", err)
	}
	assertLast(false)

	// 打开：一句话。
	setGlobal(t, b, "antiad_so_simple_instructions", "1")
	if _, err := judgeSystemOne(b, b.Cache.Snap(), adState{}, soInstructions); err != nil {
		t.Fatalf("judgeSystemOne: %v", err)
	}
	assertLast(true)

	// 复判不受影响：llmSystemPrompt 原样。
	if _, err := judgeLLM(b, b.Cache.Snap(), adState{}, adVerdict{}, llmSystemPrompt); err != nil {
		t.Fatalf("judgeLLM: %v", err)
	}
}

// truncateTestBody 把请求体裁短到可读长度，错误信息里不刷屏。
func truncateTestBody(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
