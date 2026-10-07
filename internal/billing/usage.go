package billing

import (
	"encoding/json"

	"menshen/internal/upstream"
)

// Usage 是两种协议归一化后的结果，四个字段互不重叠：
// 四者相加等于该请求的真实 token 总量，任何一类都不被重复计入或遗漏。
type Usage struct {
	PromptTokens     int // 非缓存输入
	CompletionTokens int
	CacheReadTokens  int // 命中缓存的输入
	CacheWriteTokens int // 写入缓存的输入
}

func nonNeg(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// ExtractUsage 从非流式响应体里取出用量。
//
// 这里只认非流式的形状：systemone 本就没有流式形态；复判虽然走流式，
// 但 antiad 读流时已把内容与流末的 usage 拼回非流式的形状（readChatStream），
// 所以这里不需要 SSE 扫描器。
// 解析失败返回零值：用量只影响面板上的开销数字，不该让一次统计失败
// 把判定结果一起丢掉。
func ExtractUsage(ep upstream.Endpoint, body []byte) Usage {
	if ep == upstream.EPSystemOne {
		// systemone 的 usage 是扁平的 input_tokens / output_tokens，
		// 没有缓存明细——缺失的 cached_tokens 归零后减法恒等，结果精确。
		var resp struct {
			Usage *responsesUsage `json:"usage"`
		}
		if json.Unmarshal(body, &resp) != nil || resp.Usage == nil {
			return Usage{}
		}
		return resp.Usage.normalize()
	}

	var resp struct {
		Usage *openAIUsage `json:"usage"`
	}
	if json.Unmarshal(body, &resp) != nil || resp.Usage == nil {
		return Usage{}
	}
	return resp.Usage.normalize()
}

// ---- OpenAI chat/completions ----

type openAIUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"` // newapi 扩展字段
	} `json:"prompt_tokens_details"`
}

// OpenAI 的 prompt_tokens **包含** cached_tokens，必须减掉才能保证四类不重叠。
func (o openAIUsage) normalize() Usage {
	return Usage{
		PromptTokens:     nonNeg(o.PromptTokens - o.PromptTokensDetails.CachedTokens),
		CompletionTokens: nonNeg(o.CompletionTokens),
		CacheReadTokens:  nonNeg(o.PromptTokensDetails.CachedTokens),
		CacheWriteTokens: nonNeg(o.PromptTokensDetails.CacheWriteTokens),
	}
}

// ---- TypeSafe systemone（与 OpenAI responses 同构）----

type responsesUsage struct {
	InputTokens       int `json:"input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	InputTokenDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

// input_tokens 同样包含 cached_tokens，因此同样要减。
func (o responsesUsage) normalize() Usage {
	return Usage{
		PromptTokens:     nonNeg(o.InputTokens - o.InputTokenDetails.CachedTokens),
		CompletionTokens: nonNeg(o.OutputTokens),
		CacheReadTokens:  nonNeg(o.InputTokenDetails.CachedTokens),
	}
}

// MergeUsage 累加两级调用的用量，面板上的开销才是真实总额。
func MergeUsage(a, b Usage) Usage {
	return Usage{
		PromptTokens:     a.PromptTokens + b.PromptTokens,
		CompletionTokens: a.CompletionTokens + b.CompletionTokens,
		CacheReadTokens:  a.CacheReadTokens + b.CacheReadTokens,
		CacheWriteTokens: a.CacheWriteTokens + b.CacheWriteTokens,
	}
}
