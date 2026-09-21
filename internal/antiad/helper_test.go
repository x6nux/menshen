package antiad

import (
	"menshen/internal/core"
	"menshen/internal/tg"
)

// dispatch 是本包测试用的 Update 分发函数。
//
// 真正的分发住在 main 包（它要同时看见 antiad 与 panel），内部包的测试
// 够不着，只能复刻与本包相关的那几条分支。私聊与回调分支刻意不接：
// 它们通向 panel，本包的测试也不该依赖它。
//
// 跨层的完整分发由顶层的 flow_test.go 覆盖。
func dispatch(b *core.Bot, u *tg.Update) {
	switch {
	case u.Message != nil && u.Message.From != nil && u.Message.Chat != nil:
		if u.Message.Chat.Type != "" && u.Message.Chat.Type != "private" {
			HandleGroupMessage(b, u.Message)
		}
	case u.ChatMember != nil:
		HandleChatMemberUpdate(b, u.ChatMember)
	case u.MyChatMember != nil:
		HandleMyChatMemberUpdate(b, u.MyChatMember)
	}
}
