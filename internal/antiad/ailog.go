package antiad

// ai_log 落库：每次大模型调用的请求体（剥离固定提示词后）、响应体、用量
// 与成本、时延与失败阶段，一行一次尝试。读侧在面板（panel/ailog.go），
// 保留期在 CleanupData（ai_log_days）。

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"menshen/internal/core"
	"menshen/internal/upstream"
)

const (
	// aiLogInputCap / aiLogOutputCap 是流水两列的单行字节上限。请求体主要
	// 由状态载荷构成（正文与引用受 adStateTextLimit=2000 rune 约束，加上
	// recent_context 也就几 KB），64KB 已经是观测外的极端；响应体里推理
	// 模型的思考内容可能很长，32KB 起截断。截断只影响调试还原，不影响判定。
	aiLogInputCap  = 64 << 10
	aiLogOutputCap = 32 << 10

	// aiPromptStripRunes：超过它的「固定提示词」按占位符落库。判定载荷里
	// 最长的动态文本是 2000 rune 的正文/引用，3500 rune 以上的只可能是
	// soInstructions / llmSystemPrompt 这类对每次请求都一样的系统提示词
	// （soInstructions 约 1 万字符）——它们重复落库只烧空间，占位符注明
	// 原长，排查提示词版本时仍能对上。
	aiPromptStripRunes = 3500

	// aiLogErrorCap 是 error 列的上限：上游错误体里截过 200 字节，加上
	// 包装的上下文足够定位；再长只会在查询时拖慢面板。
	aiLogErrorCap = 500
)

// stripFixedPrompts 返回可落库的请求体：把固定系统提示词换成占位符。
//
// chat 端点剥 messages 里 system/developer 角色的长消息（llmSystemPrompt、
// ruleAgentSystemPrompt 都挂在这里）；systemone 端点剥 questions.*.instructions
// （soInstructions 在 is_ad 那一题里）。其余字段——正文、引用、资料、上下文、
// known_ad_patterns——是每次判定真正不同的部分，原样保留。
//
// 载荷是判定链路反复复用的 map，这里绝不原地改：命中的字段在浅拷贝的
// 容器上替换，未命中时直接返回原 map。
func stripFixedPrompts(ep upstream.Endpoint, payload map[string]any) map[string]any {
	switch ep {
	case upstream.EPChat:
		switch msgs := payload["messages"].(type) {
		case []map[string]string:
			stripped := false
			out := make([]map[string]string, len(msgs))
			copy(out, msgs)
			for i, m := range out {
				switch m["role"] {
				case "system", "developer":
					if n := len([]rune(m["content"])); n >= aiPromptStripRunes {
						out[i] = map[string]string{"role": m["role"], "content": promptPlaceholder(n)}
						stripped = true
					}
				}
			}
			if stripped {
				p := cloneMap(payload)
				p["messages"] = out
				return p
			}
		case []any:
			stripped := false
			out := make([]any, len(msgs))
			copy(out, msgs)
			for i, mv := range out {
				m, ok := mv.(map[string]any)
				if !ok {
					continue
				}
				switch m["role"] {
				case "system", "developer":
					if s, ok := m["content"].(string); ok {
						if n := len([]rune(s)); n >= aiPromptStripRunes {
							out[i] = map[string]any{"role": m["role"], "content": promptPlaceholder(n)}
							stripped = true
						}
					}
				}
			}
			if stripped {
				p := cloneMap(payload)
				p["messages"] = out
				return p
			}
		}
	case upstream.EPSystemOne:
		qs, ok := payload["questions"].(map[string]any)
		if !ok {
			return payload
		}
		stripped := false
		out := make(map[string]any, len(qs))
		for k, qv := range qs {
			out[k] = qv
			q, ok := qv.(map[string]any)
			if !ok {
				continue
			}
			if s, ok := q["instructions"].(string); ok {
				if n := len([]rune(s)); n >= aiPromptStripRunes {
					qc := make(map[string]any, len(q))
					for kk, vv := range q {
						qc[kk] = vv
					}
					qc["instructions"] = promptPlaceholder(n)
					out[k] = qc
					stripped = true
				}
			}
		}
		if stripped {
			p := cloneMap(payload)
			p["questions"] = out
			return p
		}
	}
	return payload
}

// cloneMap 浅拷贝载荷：只发生在命中固定提示词的路径上（罕见），外层
// 容器换副本，未命中的键与原载荷共享，不影响判定链路正在用的 map。
func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

// promptPlaceholder 是固定提示词落库后的占位符：注明原字符数，提示词
// 改版时靠它对上「这条流水当时用的是哪一版」。
func promptPlaceholder(n int) string {
	return fmt.Sprintf("«固定提示词已省略，原文 %d 字符»", n)
}

// logAICall 把一次尝试落进 ai_log。判定热路径上的写入：任何失败只记运行
// 日志，绝不影响判定本身；表缺列等结构问题靠 Cleanup/迁移兜住。
func logAICall(sh *core.Shared, ep upstream.Endpoint, model, up string,
	payload map[string]any, r aiResult) {

	if sh == nil || sh.Store == nil {
		return
	}
	input, err := json.Marshal(payload)
	if err != nil {
		// 载荷都是本服务组装的 map，到这里还序列化失败只能整体放弃输入列。
		input = []byte(`{"_error":"序列化失败"}`)
	}
	output := capBytes(r.reply.Raw, aiLogOutputCap)

	status, errText := "ok", ""
	if r.err != nil {
		status, errText = r.stage, core.TruncateRunes(r.err.Error(), aiLogErrorCap)
		if status == "" {
			status = "error"
		}
	}

	_, werr := sh.Store.Write.Exec(`INSERT INTO ai_log
		(ts,endpoint,model,upstream,status,latency_ms,
		 prompt_tokens,completion_tokens,cached_tokens,quota_cost,input,output,error)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		time.Now().Unix(), ep.String(), model, up, status, r.elapsed.Milliseconds(),
		r.reply.Usage.PromptTokens, r.reply.Usage.CompletionTokens,
		r.reply.Usage.CacheReadTokens, r.reply.Cost,
		capBytes(input, aiLogInputCap), output, errText)
	if werr != nil {
		slog.Error("反广告：模型调用流水落库失败", "err", werr)
	}
}

// capBytes 按 UTF-8 安全地截到 n 字节并加省略标记：响应体是给人看的
// 调试材料，截断处不追求合法 JSON，标记说明即可。
func capBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	cut := n
	for cut > 0 && b[cut]&0xC0 == 0x80 {
		cut-- // 回退到 UTF-8 首字节，避免截出半个字符
	}
	return string(b[:cut]) + "\n…（已截断，原文 " + fmt.Sprint(len(b)) + " 字节）"
}
