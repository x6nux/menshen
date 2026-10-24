package upstream

// 本文件处理「上游不接受 cache_control 缓存标记」这一类上游差异，以及
// 缓存标记本身的拼装。
//
// 复判/识图/规则发现的 chat 请求都是同构的：常量 system 提示词在前、
// 变化的判定载荷在后，前缀缓存本该稳定命中。但 Claude 系网关不像
// DeepSeek/OpenAI 那样自动缓存，必须显式打 cache_control 标记——走
// OpenAI 兼容形态时只能把 system 的文本内容改写成 content parts。
// 这对认该字段的网关是纯增益；个别严格校验的中转会回 4xx，处置与
// temperature 同款：见过一次就按上游记下，去掉标记立即重发（见
// antiad.aiAttempt），判定不多花一个往返以上的代价。

import (
	"strings"
	"sync"
)

// cacheControlUnsupported 是已明确拒绝过 cache_control 的上游名集合
// （进程级，重启清空）。BuildBody 据此跳过标记拼装。
var cacheControlUnsupported sync.Map

// CacheControlUnsupported 报告该上游是否已拒绝过 cache_control。
// upName 用上游名：标记来自中转对请求体的校验，与具体模型无关。
func CacheControlUnsupported(upName string) bool {
	if upName == "" {
		return false
	}
	_, ok := cacheControlUnsupported.Load(upName)
	return ok
}

// MarkCacheControlUnsupported 记下一个不接受 cache_control 的上游。
func MarkCacheControlUnsupported(upName string) {
	if upName != "" {
		cacheControlUnsupported.Store(upName, struct{}{})
	}
}

// ClearCacheControlUnsupported 清掉某个上游的标记。测试用：go test
// -count=2 同进程重复跑时，上一轮记下的会让下一轮拿不到「第一次带标记」
// 的形状；网关侧恢复支持后也可以调它重新探测。
func ClearCacheControlUnsupported(upName string) {
	if upName != "" {
		cacheControlUnsupported.Delete(upName)
	}
}

// CacheControlRejected 判断一次上游错误是否为「不接受 cache_control」。
// 判据与 TemperatureRejected 同款：4xx 且错误正文提到该字段。
func CacheControlRejected(status int, msg string) bool {
	if status >= 500 || msg == "" {
		return false
	}
	return strings.Contains(strings.ToLower(msg), "cache_control")
}

// CacheControlParts 把常量文本包成带 cache_control 的 content parts：
// Anthropic 系网关按块上的标记缓存「到该块为止」的前缀，system 消息
// 恰是判定请求里唯一跨请求不变的一段。
func CacheControlParts(text string) []map[string]any {
	return []map[string]any{
		{"type": "text", "text": text, "cache_control": map[string]string{"type": "ephemeral"}},
	}
}
