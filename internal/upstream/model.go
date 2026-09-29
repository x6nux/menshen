package upstream

import (
	"hash/fnv"
	"strings"
)

type Model struct {
	Name            string
	PromptPrice     float64 // USD / 1M tokens
	CompletionPrice float64
	CacheReadPrice  float64
	CacheWritePrice float64
	Enabled         bool
}

// SplitModelName 把模型名切成（上游名, 模型ID）。
//
// 命名规范 <上游名>/<模型ID>：为了区分多个上游，模型名前缀上游名。
// **按第一个 "/" 切** —— 模型 ID 自身可以带 "/"（OpenRouter 形态
// openrouter/openai/gpt-4o），所以只能切第一个。
// 没有前缀（旧格式）时上游名为空串，调用方走「任选可用上游」的旧路径。
func SplitModelName(name string) (upstreamName, modelID string) {
	if i := strings.IndexByte(name, '/'); i > 0 && i < len(name)-1 {
		return name[:i], name[i+1:]
	}
	return "", name
}

// UpstreamName / ModelID 是 SplitModelName 的便捷读法。
func (m *Model) UpstreamName() string { n, _ := SplitModelName(m.Name); return n }
func (m *Model) ModelID() string      { _, id := SplitModelName(m.Name); return id }

type Upstream struct {
	ID      int64
	Name    string
	BaseURL string
	APIKey  string
	Weight  int64
	Status  int64
	// 两个端点各自独立开关。少一个开关就会把请求发给不认这条路径的渠道，
	// 拿回 404 —— 而 404 在反广告侧只表现为「判定失败 → 放行」，
	// 功能静默失效，运维完全看不见。
	SupportsChat      bool
	SupportsSystemOne bool
}

// Supports 报告这个上游是否提供某个端点。绑定模型（<上游名>/<模型ID>）
// 选路时也要用它，所以导出。
func (u *Upstream) Supports(ep Endpoint) bool {
	switch ep {
	case EPChat:
		return u.SupportsChat
	case EPSystemOne:
		return u.SupportsSystemOne
	}
	return false
}

type Endpoint int

const (
	EPChat Endpoint = iota
	// EPSystemOne 是 TypeSafe 的决策端点（jev 系列）。它不是对话协议：
	// 请求体是 state + questions，响应是带 confidence 的定型答案，且没有流式。
	// 反广告的主判定就建立在这个「带置信度的结构化答案」上。
	EPSystemOne
)

func (e Endpoint) String() string {
	switch e {
	case EPChat:
		return "chat"
	case EPSystemOne:
		return "systemone"
	}
	return "unknown"
}

// Paths 是各端点在上游侧的路径。
// systemone 固定走 TypeSafe 原生形态 /v1/systemone。
var Paths = map[Endpoint]string{
	EPChat:      "/v1/chat/completions",
	EPSystemOne: "/v1/systemone",
}

// maxPickWeight 是参与加权选择时单条权重的上限。写入侧校验漏了或老库里
// 有超大值（手改、脚本导入）时，物化权重的实现会按 weight 重复填充切片，
// 1e9 就是一次 ~8GB 分配。选择算法本身不物化，这里再钳一道纯属兜底。
const maxPickWeight = 1000

func pickWeight(u *Upstream) int64 {
	w := u.Weight
	if w < 1 {
		w = 1
	}
	if w > maxPickWeight {
		w = maxPickWeight
	}
	return w
}

// Pick 返回候选上游列表，首个为 sticky 选中者，其余按顺序供失败重试。
// 按 hash(key) 做 sticky，weight 通过区间宽度体现。
//
// 不物化加权切片：区间算术等价于「按 weight 重复填充后取模」，但不会因为
// 一个超大 weight 就分配出天文数字的切片。
func Pick(all []*Upstream, ep Endpoint, key string) []*Upstream {
	var pool []*Upstream
	var total int64
	for _, u := range all {
		if u.Status == 1 && u.Supports(ep) {
			pool = append(pool, u)
			total += pickWeight(u)
		}
	}
	if len(pool) <= 1 {
		return pool
	}

	h := fnv.New32a()
	h.Write([]byte(key))
	pos := int64(h.Sum32()) % total

	// 找到 sticky 点落在谁的区间里：它就是加权序列里第一个出现的上游。
	start := 0
	for i, u := range pool {
		if w := pickWeight(u); pos < w {
			start = i
			break
		} else {
			pos -= w
		}
	}

	// 从 sticky 起点展开去重，得到「首选 + 其余候选」。
	out := make([]*Upstream, 0, len(pool))
	for i := 0; i < len(pool); i++ {
		out = append(out, pool[(start+i)%len(pool)])
	}
	return out
}

// MaskKey 遮蔽上游 api_key，供面板展示。
func MaskKey(k string) string {
	if len(k) <= 12 {
		return "sk-****"
	}
	return k[:7] + "..." + k[len(k)-4:]
}
