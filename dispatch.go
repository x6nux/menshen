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

	// 私聊 /start 的深链参数只在点击按钮的那一下出现，出问题（「参数读不到」）
	// 时没有别的证据可查：到没到、到的是什么、走哪一支、点在哪个 bot 上，
	// 全看这一行。
	if text == "/start" || strings.HasPrefix(text, "/start ") {
		slog.Info("私聊 /start", "bot", b.BotID(), "uid", m.From.ID,
			"payload", strings.TrimSpace(strings.TrimPrefix(text, "/start")),
			"staff", b.IsStaff(m.From.ID))
	}

	// 非管理员的私聊全部交给申诉通道：deep link、申诉理由输入、解禁码提示。
	// 它必须排在权限判断之前：被限制发言的正是普通用户，按「非管理员
	// 一律忽略」处理会把整条申诉通道堵死。
	if !b.IsStaff(m.From.ID) {
		if !antiad.HandleNonStaffPrivate(b, m, text) && strings.HasPrefix(text, "/start") {
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

	// 管理员私聊发解禁码 = 兑换（范围按身份分级，见 HandleDirectRedeem）。
	if code, ok := antiad.FindUnlockCode(text); ok {
		antiad.HandleDirectRedeem(b, m, code)
		return
	}

	// /log、/user、/white：记录查询与豁免名单。
	switch {
	case text == "/log" || strings.HasPrefix(text, "/log ") ||
		strings.HasPrefix(text, "/log@"):
		panel.HandleLogCommand(b, m, text)
		return
	case text == "/user" || strings.HasPrefix(text, "/user ") ||
		strings.HasPrefix(text, "/user@"):
		panel.HandleUserCommand(b, m, text)
		return
	case text == "/white" || strings.HasPrefix(text, "/white ") ||
		strings.HasPrefix(text, "/white@"):
		panel.HandleWhiteDM(b, m, text)
		return
	}

	if text == "/start" || strings.HasPrefix(text, "/start ") {
		// 两种群内按钮都带记录号：告警的「打开 bot 处理」是 log<记录号>，
		// 冷判定通知的「📝 申诉」是 ub<记录号>。管理员点进来直接落到那条
		// 记录卡片（权限由 ShowLogCard 再查一次）；不带记录号才是主菜单。
		payload := strings.TrimSpace(strings.TrimPrefix(text, "/start"))
		if id, ok := antiad.ParseLogPayload(payload); ok {
			panel.ShowLogCard(b, m.Chat.ID, m.From.ID, id)
			return
		}
		if id, ok := antiad.ParseUnbanPayload(payload); ok {
			panel.ShowLogCard(b, m.Chat.ID, m.From.ID, id)
			return
		}
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
	// 申诉按钮对被限制的普通用户开放，先于权限判断分流。
	if strings.HasPrefix(q.Data, "a:ap:") {
		antiad.HandleAppealCallback(b, q)
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
