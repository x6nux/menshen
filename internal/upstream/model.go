package upstream

import (
	"fmt"
	"hash/fnv"
	"strings"
)

// Kind 是渠道类型：决定与上游通信的方式——端点路径、鉴权头、
// 请求体与响应信封的形态。supports_chat / supports_systemone 决定该渠道
// 用在哪条链路上，与 kind 正交。
type Kind string

const (
	// KindOpenAI：OpenAI 兼容 chat/completions；systemone 走 TypeSafe 原生
	// /v1/systemone（newapi 这类同时代理两条路径的网关）。
	KindOpenAI Kind = "openai"
	// KindOpenAIResp：OpenAI Responses API（/v1/responses）。只有对话
	// 能力，流式事件与整包响应都由适配层与 chat/completions 互转。
	KindOpenAIResp Kind = "openai-responses"
	// KindAnthropic：Anthropic Messages API（/v1/messages）。只有对话
	// 能力，流式事件与整包响应由适配层互转。
	KindAnthropic Kind = "anthropic"
	// KindGemini：Google Gemini generateContent（chat 用
	// :streamGenerateContent?alt=sse）。只有对话能力。
	KindGemini Kind = "gemini"
	// KindCloudflare：Cloudflare Workers AI（/ai/run/{模型ID}）。主判定
	// 直调 Clef；开启 chat 时同样走 ai/run，流式帧由适配层翻译。
	KindCloudflare Kind = "cloudflare"
)

var kindLabels = map[Kind]string{
	KindOpenAI:     "OpenAI Completions",
	KindOpenAIResp: "OpenAI Responses",
	KindAnthropic:  "Anthropic Messages",
	KindGemini:     "Google Gemini",
	KindCloudflare: "Cloudflare Workers AI",
}

// ParseKind 解析渠道类型；空串按 openai 处理（老库默认值与旧数据）。
func ParseKind(s string) (Kind, error) {
	k := Kind(strings.TrimSpace(strings.ToLower(s)))
	if k == "" {
		return KindOpenAI, nil
	}
	if _, ok := kindLabels[k]; ok {
		return k, nil
	}
	return "", fmt.Errorf("未知渠道类型 %q", s)
}

// Label 是面板上的显示名。
func (k Kind) Label() string {
	if l, ok := kindLabels[k]; ok {
		return l
	}
	return kindLabels[KindOpenAI]
}

// ChatOnly 报告该类型能否用于主判定（systemone）。只有 OpenAI Completions
// 与 Cloudflare 能承载 state + questions 形式的决策请求，其余都是对话协议，
// 只能做复判与形态总结。
func (k Kind) ChatOnly() bool {
	return k != KindOpenAI && k != KindCloudflare
}

// DefaultCaps 是该类型新增渠道时的默认能力开关。
func (k Kind) DefaultCaps() (chat, systemone bool) {
	if k == KindCloudflare {
		return false, true
	}
	return true, false
}

// ResolveCaps 统一能力开关：chat-only 类型强制只开 chat；explicit=false
// （调用方没提供开关）时用类型默认值。
func (k Kind) ResolveCaps(chat, systemone, explicit bool) (bool, bool) {
	if k.ChatOnly() {
		return true, false
	}
	if !explicit {
		return k.DefaultCaps()
	}
	return chat, systemone
}

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
// 命名规范 <上游名>/<模型ID>：为区分多个上游，模型名前缀上游名。
// **按第一个 "/" 切** —— 模型 ID 自身可以带 "/"，因此只能切第一个。
// 没有前缀时上游名为空串，调用方走任选可用上游的路径。
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
	// Kind 是渠道类型。零值按 KindOpenAI 处理（见 EffectiveKind），
	// 老数据与手工构造的测试对象不用显式赋值。
	Kind Kind
	// 两个端点各自独立开关。少一个开关就会把请求发给不认这条路径的渠道，
	// 拿回 404 —— 而 404 在反广告侧只表现为判定失败并放行，功能静默失效，
	// 不易察觉。
	SupportsChat      bool
	SupportsSystemOne bool
}

// EffectiveKind 把未知/零值的 kind 归一成 openai。
func (u *Upstream) EffectiveKind() Kind {
	if k, err := ParseKind(string(u.Kind)); err == nil {
		return k
	}
	return KindOpenAI
}

// Supports 报告这个上游是否提供某个端点。绑定模型（<上游名>/<模型ID>）
// 选路时也要用它，所以导出。
func (u *Upstream) Supports(ep Endpoint) bool {
	if u.EffectiveKind().ChatOnly() && ep == EPSystemOne {
		return false
	}
	switch ep {
	case EPChat:
		return u.SupportsChat
	case EPSystemOne:
		return u.SupportsSystemOne
	}
	return false
}

// URL 返回该端点在给定上游上的请求地址。model 是模型全名
// <上游名>/<模型ID>。
//
// 各类型的 base_url 约定：
//   - OpenAI Completions / Responses：到域名（或网关前缀），如
//     https://api.openai.com；路径自动接 /v1/chat/completions 或 /v1/responses。
//   - Anthropic：https://api.anthropic.com，路径自动接 /v1/messages。
//   - Gemini：https://generativelanguage.googleapis.com/v1beta，
//     chat 路径自动接 /models/<模型ID>:streamGenerateContent?alt=sse。
//   - Cloudflare：到 /client/v4/accounts/<账号ID>（直连官方或兼容网关），
//     模型 ID 填完整 CF 模型名 @cf/cloudflare/clef，路径自动接 /ai/run/<模型ID>。
func (u *Upstream) URL(ep Endpoint, model string) (string, error) {
	base := strings.TrimRight(u.BaseURL, "/")
	if base == "" {
		return "", fmt.Errorf("上游 %s 未配置 base_url", u.Name)
	}
	_, id := SplitModelName(model)
	switch u.EffectiveKind() {
	case KindCloudflare:
		if id == "" {
			return "", fmt.Errorf("模型 %q 缺少模型 ID", model)
		}
		return base + "/ai/run/" + id, nil
	case KindOpenAIResp:
		if ep != EPChat {
			return "", fmt.Errorf("上游 %s（OpenAI Responses）不支持 %s 端点", u.Name, ep)
		}
		return base + "/v1/responses", nil
	case KindAnthropic:
		if ep != EPChat {
			return "", fmt.Errorf("上游 %s（Anthropic）不支持 %s 端点", u.Name, ep)
		}
		return base + "/v1/messages", nil
	case KindGemini:
		if ep != EPChat {
			return "", fmt.Errorf("上游 %s（Gemini）不支持 %s 端点", u.Name, ep)
		}
		if id == "" {
			return "", fmt.Errorf("模型 %q 缺少模型 ID", model)
		}
		return base + "/models/" + id + ":streamGenerateContent?alt=sse", nil
	}
	p, ok := Paths[ep]
	if !ok {
		return "", fmt.Errorf("未知端点 %s", ep)
	}
	return base + p, nil
}

type Endpoint int

const (
	EPChat Endpoint = iota
	// EPSystemOne 是 TypeSafe 的决策端点（jev 系列）。它不是对话协议：
	// 请求体是 state + questions，响应是带 confidence 的定型答案，且没有流式。
	// 反广告的主判定就建立在这个带置信度的结构化答案上。
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

// Paths 是各端点在上游侧的路径（仅 OpenAI Completions 渠道使用）。
// systemone 固定走 TypeSafe 原生形态 /v1/systemone。
var Paths = map[Endpoint]string{
	EPChat:      "/v1/chat/completions",
	EPSystemOne: "/v1/systemone",
}

// maxPickWeight 是参与加权选择时单条权重的上限。写入侧校验遗漏或库中存在
// 超大值时，物化权重的实现会按 weight 重复填充切片，造成超大分配。选择
// 算法本身不物化，此处再做一次钳制作为兜底。
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
// 不物化加权切片：区间算术等价于按 weight 重复填充后取模，但不会因某个
// 超大 weight 分配出巨大切片。
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

	// 从 sticky 起点展开去重，得到首选 + 其余候选。
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
