package panel

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"menshen/internal/core"
	"menshen/internal/tg"
)

// dispatch 是本包测试用的 Update 分发函数。
//
// 实际分发位于 main 包（需同时引用 antiad 与 panel），内部包测试无法访问，
// 因此只复刻与面板相关的分支：面板入口即回调。
func dispatch(b *core.Bot, u *tg.Update) {
	if u.CallbackQuery != nil && u.CallbackQuery.From != nil {
		HandleAdminCallback(b, u.CallbackQuery)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// hashOf 与 antiad 的内容哈希同算法（空白归一后 sha256 取前 16 字节），
// 供面板测试造数据；算法若不一致，TestFalsePositiveForgetsHashAndUnbans 会失败。
func hashOf(text string) string {
	sum := sha256.Sum256([]byte(strings.Join(strings.Fields(text), " ")))
	return hex.EncodeToString(sum[:16])
}
