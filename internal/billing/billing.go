package billing

import (
	"menshen/internal/upstream"

	"fmt"
	"math"
)

// QuotaPerUSD 是每美元折算的 quota 数，取值与 newapi 的 QuotaPerUnit 一致。
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

// ComputeCost 按四类 token 各自单价累加成本，向上取整为 int64 quota。
// 定价全部来自 models 表；中间以 float64 计算，最终 ceil，避免零成本请求。
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
