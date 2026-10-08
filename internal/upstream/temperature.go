package upstream

// 本文件处理「模型不接受 temperature 参数」这一类上游差异。
//
// 判定与规则发现都以 temperature=0 发请求（判定要可复现，见
// antiad/judge.go）。但一部分模型直接拒绝该参数，回 400
// "temperature is deprecated for this model"（新近的推理模型，以及网关
// 上转的 Claude 系模型都见过这种回法）。硬带着它发，这些模型整条链路都
// 用不了：判定失败放行、规则发现整轮失败。
//
// 处置办法是**见过一次就记下来**（进程级，重启清空）：记下之后请求侧不再
// 带该参数 —— 判定在 BuildBody 里去掉，规则发现由两个传输层
// RoundTripper 去掉。第一次仍要多花一个 400 往返，进程重启后每模型再花
// 一次；temperature 只影响采样，去掉不会让请求失败，所以这个代价可以接受。

import (
	"strings"
	"sync"
)

// temperatureUnsupported 是已被上游明确拒绝过 temperature 的模型 ID 集合。
// 键是发给上游的模型 ID（不带上游前缀）—— 判定与规则发现两边的请求体里
// 都是这个形态。
var temperatureUnsupported sync.Map

// TemperatureUnsupported 报告该模型 ID 是否已被上游拒绝过 temperature。
// modelID 是发给上游的模型名（upstream.SplitModelName 的第二段）。
func TemperatureUnsupported(modelID string) bool {
	if modelID == "" {
		return false
	}
	_, ok := temperatureUnsupported.Load(modelID)
	return ok
}

// MarkTemperatureUnsupported 记下一个不接受 temperature 的模型 ID。
func MarkTemperatureUnsupported(modelID string) {
	if modelID != "" {
		temperatureUnsupported.Store(modelID, struct{}{})
	}
}

// TemperatureRejected 判断一次上游错误是否为「模型不接受 temperature」。
//
// 判据只有两条：4xx（参数不认识、参数已废弃、字段不允许都是这一档）且
// 消息里提到 temperature。不额外比对措辞：各网关的文案千差万别，而误判
// 的后果只是这次请求少带一个采样参数 —— 不会让请求失败，所以宁可放宽。
// status 传 0 表示只看消息文本。
func TemperatureRejected(status int, msg string) bool {
	if status >= 500 || msg == "" {
		return false
	}
	return strings.Contains(strings.ToLower(msg), "temperature")
}
