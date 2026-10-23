package antiad

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"menshen/internal/testutil"
)

// TestAILogRecordsEveryAttempt：每次模型调用都要在 ai_log 里留一行流水，
// 输入列剥掉固定提示词（soInstructions 对所有请求都一样，重复落库只烧
// 空间），输出列与用量照实存 —— 换模型校准阈值时第一手证据在这里。
func TestAILogRecordsEveryAttempt(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	var bodies []string
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		bs := make([]byte, 1<<20)
		n, _ := r.Body.Read(bs)
		bodies = append(bodies, string(bs[:n]))
		fakeAIOK(w, r)
	})

	if _, err := judgeSystemOne(b, b.Cache.Snap(),
		adState{Chat: chatInfoForTest("压测群")}, soInstructions); err != nil {
		t.Fatalf("判定失败: %v", err)
	}

	var id, ts int64
	var endpoint, model, status, input, output string
	var latency, pt, ct, cached, cost int64
	err := b.Store.Read.QueryRow(`SELECT id,ts,endpoint,model,upstream,status,latency_ms,
		prompt_tokens,completion_tokens,cached_tokens,quota_cost,input,output
		FROM ai_log ORDER BY id DESC LIMIT 1`).
		Scan(&id, &ts, &endpoint, &model, new(string), &status, &latency,
			&pt, &ct, &cached, &cost, &input, &output)
	if err != nil {
		t.Fatalf("ai_log 里查不到流水: %v", err)
	}
	if id == 0 || ts == 0 {
		t.Errorf("id/ts 应有值: id=%d ts=%d", id, ts)
	}
	if endpoint != "systemone" || !strings.Contains(model, "so-model") {
		t.Errorf("endpoint/model = %s/%s，应为 systemone/…so-model", endpoint, model)
	}
	if status != "ok" {
		t.Errorf("status = %q，应为 ok", status)
	}
	if !strings.Contains(output, "is_ad") {
		t.Errorf("output 应是响应体，got %q", output)
	}

	// 输入列：动态 state（正文/群名）必须在，固定提示词必须只剩占位符。
	if !strings.Contains(input, "压测群") {
		t.Errorf("input 应含动态字段（群名），got %q", input)
	}
	if strings.Contains(input, "Is this group message advertising") {
		t.Errorf("固定提示词不应落库，got %q", input)
	}
	if !strings.Contains(input, "固定提示词已省略") {
		t.Errorf("input 应有占位符标记，got %q", input)
	}
	// 真实发出去的请求不受落库剥离影响：提示词原文必须在 body 里。
	if len(bodies) == 0 || !strings.Contains(bodies[0], "Is this group message advertising") {
		t.Errorf("真实请求应含完整提示词，got %q", bodies)
	}
}

// TestAILogStripLeavesShortInstructions：没到剥离线的动态 instructions
// 原样落库 —— 剥离只针对固定大提示词，不是一刀切。
func TestAILogStripLeavesShortInstructions(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	fakeAI(t, b, nil)

	if _, err := judgeSystemOne(b, b.Cache.Snap(), adState{}, "自定义短提示词"); err != nil {
		t.Fatalf("判定失败: %v", err)
	}
	var input string
	if err := b.Store.Read.QueryRow(`SELECT input FROM ai_log ORDER BY id DESC LIMIT 1`).
		Scan(&input); err != nil {
		t.Fatalf("查流水失败: %v", err)
	}
	if !strings.Contains(input, "自定义短提示词") {
		t.Errorf("短 instructions 应原样落库，got %q", input)
	}
	var payload struct {
		Questions struct {
			IsAd struct {
				Instructions string `json:"instructions"`
			} `json:"is_ad"`
		} `json:"questions"`
	}
	if err := json.Unmarshal([]byte(input), &payload); err != nil {
		t.Fatalf("input 不是合法 JSON: %v", err)
	}
	if payload.Questions.IsAd.Instructions != "自定义短提示词" {
		t.Errorf("instructions 应原样保留，got %q", payload.Questions.IsAd.Instructions)
	}
}

// chatInfoForTest 造一个带群名的 adState，供断言「动态字段必须落库」用。
func chatInfoForTest(title string) (c adChatInfo) {
	c.Title = title
	return
}
