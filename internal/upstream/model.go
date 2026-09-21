package upstream

import "hash/fnv"

type Model struct {
	Name            string
	PromptPrice     float64 // USD / 1M tokens
	CompletionPrice float64
	CacheReadPrice  float64
	CacheWritePrice float64
	Enabled         bool
}

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

func (u *Upstream) supports(ep Endpoint) bool {
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

// Pick 返回候选上游列表，首个为 sticky 选中者，其余按顺序供失败重试。
// 按 hash(key) 做 sticky，weight 通过重复填充体现。
func Pick(all []*Upstream, ep Endpoint, key string) []*Upstream {
	var pool []*Upstream
	for _, u := range all {
		if u.Status == 1 && u.supports(ep) {
			pool = append(pool, u)
		}
	}
	if len(pool) <= 1 {
		return pool
	}

	var weighted []*Upstream
	for _, u := range pool {
		w := u.Weight
		if w < 1 {
			w = 1
		}
		for i := int64(0); i < w; i++ {
			weighted = append(weighted, u)
		}
	}

	h := fnv.New32a()
	h.Write([]byte(key))
	start := int(h.Sum32()) % len(weighted)

	// 从 sticky 起点展开去重，得到「首选 + 其余候选」
	out := make([]*Upstream, 0, len(pool))
	seen := map[int64]bool{}
	for i := 0; i < len(weighted); i++ {
		u := weighted[(start+i)%len(weighted)]
		if !seen[u.ID] {
			seen[u.ID] = true
			out = append(out, u)
		}
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
