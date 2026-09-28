package main

import (
	"log/slog"
	"strings"

	"menshen/internal/antiad"
	"menshen/internal/core"
	"menshen/internal/panel"
	"menshen/internal/tg"
)

// dispatch 是 Update 的业务分发入口，由 main 注入给每个 Bot 实例。
//
// 它住在 main 而不是 core：core 被 antiad 与 panel 依赖，反过来 import
// 它们就成了循环。这里是唯一同时看得见三层的地方，分流规则也就只能写在这。

func dispatch(b *core.Bot, u *tg.Update) {
	switch {
	case u.CallbackQuery != nil && u.CallbackQuery.From != nil:
		handleCallback(b, u.CallbackQuery)
	case u.Message != nil && u.Message.From != nil && u.Message.Chat != nil:
		handleMessage(b, u.Message)
	case u.EditedMessage != nil && u.EditedMessage.From != nil && u.EditedMessage.Chat != nil:
		// 只接群里的编辑：私聊里编辑一条旧消息不该被当成又一次面板输入。
		if m := u.EditedMessage; m.Chat.Type != "" && m.Chat.Type != "private" {
			// 主 bot 不入群、不判定：即使它因退群失败还留在某个群里，
			// 群里的编辑也不该进反广告链路。
			if b.IsMainBot() {
				return
			}
			antiad.HandleGroupMessage(b, m)
		}
	case u.ChatMember != nil:
		if b.IsMainBot() {
			return
		}
		antiad.HandleChatMemberUpdate(b, u.ChatMember)
	case u.MyChatMember != nil:
		if b.IsMainBot() {
			handleMainBotChatMember(b, u.MyChatMember)
			return
		}
		antiad.HandleMyChatMemberUpdate(b, u.MyChatMember)
	}
}

// handleMainBotChatMember 让主 bot 静默退出被拉进的群/频道。
//
// 主 bot 只做配置管理与接入其他 bot，不入群、不判定广告。Telegram 侧
// 没有「禁止别人拉我」的开关，自动退出是唯一防线。静默：不通知、不回复。
func handleMainBotChatMember(b *core.Bot, cu *tg.ChatMemberUpdated) {
	if cu == nil || cu.Chat == nil || cu.NewChatMember == nil {
		return
	}
	if cu.Chat.Type == "private" {
		return
	}
	if !tgMemberPresent(cu.NewChatMember) {
		return // left / kicked：本来就不在群里
	}
	if ok, desc := b.CallOK("leaveChat", map[string]any{"chat_id": cu.Chat.ID}); !ok {
		slog.Warn("主 bot 退出群/频道失败", "chat", cu.Chat.ID,
			"type", cu.Chat.Type, "tg_error", desc)
		return
	}
	slog.Info("主 bot 已自动退出群/频道", "chat", cu.Chat.ID, "type", cu.Chat.Type)
}

// tgMemberPresent 报告这次成员变更之后 bot 是否在群里。
//
// member / administrator / creator 没有 is_member 字段，一律算在群里；
// restricted 要额外看 is_member —— 被踢走的人也会留下受限状态。
func tgMemberPresent(cm *tg.ChatMemberInfo) bool {
	switch cm.Status {
	case "member", "administrator", "creator":
		return true
	case "restricted":
		return cm.IsMember
	}
	return false
}

func handleMessage(b *core.Bot, m *tg.Message) {
	// 群聊消息交给反广告链路。它自带全部守门（总开关、群白名单），
	// 未启用时立即返回，行为与「直接忽略群消息」完全一致。
	if m.Chat.Type != "" && m.Chat.Type != "private" {
		// 主 bot 只做配置管理与接入其他 bot：群里的消息一律不判、
		// 不留底、不花钱。它不该在群里，被拉进去由 my_chat_member
		// 自动退出；这里守的是退出完成前的窗口期与退群失败的情况。
		if b.IsMainBot() {
			return
		}
		antiad.HandleGroupMessage(b, m)
		return
	}

	text := strings.TrimSpace(m.Text)

	// deep link 必须排在权限判断之前：被限制发言的普通用户正是靠它
	// 从群里跳进私聊来申诉的，按「非管理员一律忽略」处理会把整条
	// 自助解除通道堵死。
	if payload, ok := strings.CutPrefix(text, "/start "); ok {
		if antiad.HandleStartPayload(b, m, strings.TrimSpace(payload)) {
			return
		}
	}

	// 其余私聊只服务管理面板。非管理员一句话打发，不暴露任何入口。
	if !b.IsStaff(m.From.ID) {
		if strings.HasPrefix(text, "/start") {
			b.Send(m.Chat.ID,
				"本 bot 是群组反广告助手，把它拉进群并设为管理员即可工作。", nil)
		}
		return
	}
	// 启动时若因会话不存在而注册失败，这里补上（只成功一次）
	b.EnsureAdminCommands(m.From.ID)

	// 待输入会话优先于命令：管理员点了「改阈值」后直接发数字即生效。
	if p, ok := b.TakePending(m.From.ID); ok {
		panel.HandlePendingInput(b, m, p)
		return
	}

	if text == "/start" || strings.HasPrefix(text, "/start ") {
		panel.ShowMainMenu(b, m.Chat.ID, 0, m.From.ID)
		return
	}
	// 其余消息忽略
}

func handleCallback(b *core.Bot, q *tg.CallbackQuery) {
	if q.Message == nil || q.Message.Chat == nil {
		b.AnswerCallback(q.ID, "")
		return
	}
	// 主 bot 不该在群里，它名下也不会有群内告警 —— 群里来的按钮
	// 一律只回个空响应，不进面板逻辑。
	if b.IsMainBot() && q.Message.Chat.Type != "private" {
		b.AnswerCallback(q.ID, "")
		return
	}
	if !strings.HasPrefix(q.Data, "a:") {
		b.AnswerCallback(q.ID, "")
		return
	}
	// 自助解除的验证码按钮对被限制的普通用户开放，先于权限判断分流。
	if strings.HasPrefix(q.Data, "a:cap:") {
		antiad.HandleCaptchaCallback(b, q)
		return
	}

	if !b.IsStaff(q.From.ID) {
		// 反广告的处置按钮贴在群里，该群的 TG 管理员也要能点。
		// 这里只放行**处置类**回调，真正的权限判断在 adDispositionAllowed
		// 里按流水的 chat_id 做；面板导航与配置类回调仍然只有管理员能进。
		if !panel.IsAdDispositionCallback(q.Data) {
			// 非管理员静默：不暴露管理面板的存在
			b.AnswerCallback(q.ID, "")
			return
		}
	}
	panel.HandleAdminCallback(b, q)
}
