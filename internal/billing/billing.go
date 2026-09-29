package billing

import (
	"menshen/internal/upstream"

	"fmt"
	"math"
)

// ponytail: 与 newapi 的 QuotaPerUnit 一致，写死。
// 除本文件的换算函数外，代码任何位置都不得出现这个字面量。
const QuotaPerUSD = 500000

func quotaToUSD(q int64) float64 { return float64(q) / QuotaPerUSD }

// FormatUSDFine 用 8 位小数展示单次判定的开销。
// 一次群消息判定常常只有几十万分之一美元，6 位小数会把它显示成 $0.000000。
func FormatUSDFine(q int64) string {
	if q < 0 {
		return fmt.Sprintf("-$%.8f", -quotaToUSD(q))
	}
	return fmt.Sprintf("$%.8f", quotaToUSD(q))
}

// ComputeCost 四类 token 各乘各的单价，无任何折扣系数——
// 定价全部来自 models 表，代码中不存在缓存折扣的硬编码。
// 中间用 float64 计算，最终 ceil 为 int64 quota：向上取整避免零成本请求。
func ComputeCost(u Usage, m *upstream.Model) int64 {
	usd := (float64(u.PromptTokens)*m.PromptPrice +
		float64(u.CompletionTokens)*m.CompletionPrice +
		float64(u.CacheReadTokens)*m.CacheReadPrice +
		float64(u.CacheWriteTokens)*m.CacheWritePrice) / 1_000_000

	if usd <= 0 {
		return 0
	}
	return int64(math.Ceil(usd * QuotaPerUSD))
}
