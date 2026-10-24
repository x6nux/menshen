package upstream

import (
	"encoding/json"
	"testing"
)

// TestBuildBodyMarksSystemForCache：chat 请求默认给第一条 system 打
// cache_control（Claude 系网关必须显式标记才缓存前缀），其余消息原样；
// 上游拒绝过该字段后（CacheControlUnsupported）不再标记。
func TestBuildBodyMarksSystemForCache(t *testing.T) {
	t.Cleanup(func() { ClearCacheControlUnsupported("o") })
	payload := map[string]any{
		"messages": []map[string]string{
			{"role": "system", "content": "SYS"},
			{"role": "user", "content": "USER"},
		},
	}
	o := &Upstream{Name: "o"}
	body := o.BuildBody(EPChat, "o/gpt-4o", payload)

	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Fatalf("messages 应保留两条: %v", body["messages"])
	}
	sys, ok := msgs[0].(map[string]any)
	if !ok || sys["role"] != "system" {
		t.Fatalf("第一条应是原角色的 system: %v", msgs[0])
	}
	parts, ok := sys["content"].([]map[string]any)
	if !ok || len(parts) != 1 || parts[0]["text"] != "SYS" {
		t.Fatalf("system 内容应改写为带标记的 parts: %v", sys["content"])
	}
	if cc, ok := parts[0]["cache_control"].(map[string]string); !ok || cc["type"] != "ephemeral" {
		t.Fatalf("parts 应带 ephemeral cache_control: %v", parts[0])
	}
	if msgs[1].(map[string]string)["content"] != "USER" {
		t.Errorf("user 消息应原样保留: %v", msgs[1])
	}
	// 载荷本体不得被污染：原 messages[0] 仍是纯文本。
	if payload["messages"].([]map[string]string)[0]["content"] != "SYS" {
		t.Errorf("原始载荷不应被改写")
	}

	// 上游拒绝过：不再标记，形态回到原始的 []map[string]string 纯文本。
	MarkCacheControlUnsupported("o")
	body = o.BuildBody(EPChat, "o/gpt-4o", payload)
	plain, ok := body["messages"].([]map[string]string)
	if !ok || plain[0]["content"] != "SYS" {
		t.Fatalf("被拒过的上游应回到原始纯文本消息: %v", body["messages"])
	}

	// []any 在途形态（识图路径）同样标记首条 system。
	ClearCacheControlUnsupported("o")
	anyPayload := map[string]any{"messages": []any{
		map[string]any{"role": "system", "content": "VSYS"},
		map[string]any{"role": "user", "content": "VUSER"},
	}}
	body = o.BuildBody(EPChat, "o/gpt-4o", anyPayload)
	anyMsgs := body["messages"].([]any)
	sysMap := anyMsgs[0].(map[string]any)
	if _, ok := sysMap["content"].([]map[string]any); !ok {
		t.Fatalf("[]any 形态的 system 也应改写为 parts: %v", sysMap)
	}
	// 原切片不得被原地改写（识图两次尝试共用同一载荷）。
	if _, ok := anyPayload["messages"].([]any)[0].(map[string]any)["content"].(string); !ok {
		t.Errorf("[]any 原载荷不应被改写")
	}
}

// TestCacheControlRejected：判据是 4xx 且错误正文提到该字段；5xx 与
// 无关错误不算。
func TestCacheControlRejected(t *testing.T) {
	if !CacheControlRejected(400, `{"error":{"message":"cache_control is not allowed"}}`) {
		t.Errorf("4xx + cache_control 应判为拒绝")
	}
	if CacheControlRejected(500, "cache_control exploded") {
		t.Errorf("5xx 不算")
	}
	if CacheControlRejected(400, "invalid model id") {
		t.Errorf("无关错误不算")
	}
}

// TestBuildBodyAnthropicSystemCacheBlocks：Anthropic 原生渠道的 system
// 组装为带 cache_control 的 blocks（原生必须显式打标才缓存）。
func TestBuildBodyAnthropicSystemCacheBlocks(t *testing.T) {
	a := &Upstream{Name: "a", Kind: KindAnthropic}
	body := a.BuildBody(EPChat, "a/claude", map[string]any{
		"messages": []map[string]string{
			{"role": "system", "content": "SYS"},
			{"role": "user", "content": "USER"},
		},
	})
	raw, _ := json.Marshal(body["system"])
	var blocks []struct {
		Type         string            `json:"type"`
		Text         string            `json:"text"`
		CacheControl map[string]string `json:"cache_control"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil || len(blocks) != 1 {
		t.Fatalf("system 应是单个 block: %s", raw)
	}
	if blocks[0].Text != "SYS" || blocks[0].CacheControl["type"] != "ephemeral" {
		t.Errorf("system block 应带原文与 ephemeral 标记: %s", raw)
	}
}
