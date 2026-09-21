package panel

import (
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
