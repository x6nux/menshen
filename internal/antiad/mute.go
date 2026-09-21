package antiad

import (
	"log/slog"

	"menshen/internal/core"
)

// 禁言与解除禁言的权限集。它们是处置动作本身，不是面板的一部分 ——
// 判定链路（applyAction）、冷判定与自助解除都要用。

// mutedPermissions 是禁言用的全 false 权限集。
// 抽出来供 applyAction 与人工禁言共用，避免两处写歪一个字段。
func MutedPermissions() map[string]any {
	return map[string]any{
		"can_send_messages":         false,
		"can_send_audios":           false,
		"can_send_documents":        false,
		"can_send_photos":           false,
		"can_send_videos":           false,
		"can_send_video_notes":      false,
		"can_send_voice_notes":      false,
		"can_send_polls":            false,
		"can_send_other_messages":   false,
		"can_add_web_page_previews": false,
	}
}

// unmute 恢复默认权限。必须逐项给 true —— 再发一次全 false
// 等于又禁言了一次，这是误判处置里最容易写反的一处。
// 返回 TG 调用是否成功，调用方要据此决定给管理员的提示措辞。
func Unmute(b *core.Bot, chatID, uid int64) (bool, string) {
	ok, desc := b.CallOK("restrictChatMember", map[string]any{
		"chat_id": chatID, "user_id": uid,
		"permissions": map[string]any{
			"can_send_messages":         true,
			"can_send_audios":           true,
			"can_send_documents":        true,
			"can_send_photos":           true,
			"can_send_videos":           true,
			"can_send_video_notes":      true,
			"can_send_voice_notes":      true,
			"can_send_polls":            true,
			"can_send_other_messages":   true,
			"can_add_web_page_previews": true,
		},
	})
	if !ok {
		slog.Warn("反广告：解除限制失败", "chat", chatID, "uid", uid, "tg_error", desc)
	}
	return ok, desc
}
