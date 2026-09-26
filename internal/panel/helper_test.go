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
// 真正的分发住在 main 包（它要同时看见 antiad 与 panel），内部包的测试
// 够不着，只能复刻与面板相关的那一条分支：面板的入口就是回调。
func dispatch(b *core.Bot, u *tg.Update) {
	if u.CallbackQuery != nil && u.CallbackQuery.From != nil {
		HandleAdminCallback(b, u.CallbackQuery)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// hashOf 与 antiad 的内容哈希同算法（空白归一后 sha256 取前 16 字节）。
// 面板测试造数据用；算法一旦分叉，TestFalsePositiveForgetsHashAndUnbans 会变红。
func hashOf(text string) string {
	sum := sha256.Sum256([]byte(strings.Join(strings.Fields(text), " ")))
	return hex.EncodeToString(sum[:16])
}
