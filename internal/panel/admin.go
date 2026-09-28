package panel

import (
	"fmt"
	"strconv"
	"strings"

	"menshen/internal/antiad"
	"menshen/internal/core"
	"menshen/internal/tg"
)

// showMainMenu 渲染管理主菜单。本 bot 没有面向普通用户的功能，
// 主菜单就是管理面板本身，按角色决定露出哪些入口。
func ShowMainMenu(b *core.Bot, chatID, msgID, uid int64) {
	snap := b.Cache.Snap()
	main := b.IsMain(uid)

	var sb strings.Builder
	sb.WriteString("🛡 <b>群组反广告</b>\n\n")

	if snap.SettingInt("antiad_enabled", 0) == 1 {
		sb.WriteString("总开关: ✅ 已启用\n")
	} else {
		sb.WriteString("总开关: ⛔ <b>已关闭</b> —— 所有群都不判定\n")
	}

	mine := snap.BotsOwnedBy(uid, main)
	chats, workers := 0, 0
	for _, r := range mine {
		if !r.IsMain {
			workers++
		}
		for _, c := range snap.ChatsOf(r.BotID) {
			if c.Enabled {
				chats++
			}
		}
	}
	if main {
		// 主 bot 不入群、不判定：和「生效群 0 个」混在一起报，会让人以为
		// 是没配完，而不是设计如此。
		fmt.Fprintf(&sb, "已接入机器人: %d 个（主 bot %d · 工作 bot %d）｜ 生效群: %d 个\n",
			len(mine), len(mine)-workers, workers, chats)
	} else {
		fmt.Fprintf(&sb, "你的机器人: %d 个 ｜ 生效群: %d 个\n", len(mine), chats)
	}

	if main && antiad.GbanEnabled(b.Shared) {
		fmt.Fprintf(&sb, "联合封禁: ✅ 已启用 ｜ 名单 %d 人\n", len(snap.Gban))
	}

	if miss := bootstrapHint(b, uid, main); miss != "" {
		sb.WriteString("\n⚠️ <b>尚未配置完成</b>\n" + miss)
	}
	if !main {
		sb.WriteString("\n<i>你是次级管理员：可以接入自己的机器人、管理它们的群组" +
			"与判定参数。上游渠道与模型由主管理员统一配置。</i>")
	}

	rows := [][][2]string{{{"🤖 我的机器人", "a:mb"}}}
	if main {
		rows = append(rows,
			[][2]string{{"👥 管理员", "a:ga"}, {"🚫 联合封禁", "a:gb"}},
			[][2]string{{"🔌 上游渠道", "a:up"}, {"🤖 模型定价", "a:md"}},
			[][2]string{{"⚙️ 全局设置", "a:st"}},
		)
	}
	b.EditOrSend(chatID, msgID, sb.String(), tg.InlineKB(rows...))
}

// bootstrapHint 返回还缺什么配置，全部就绪时返回空串。
//
// 判定链路有四个前置条件，缺任何一个都表现为「一条都没拦到」而不报错，
// 所以要逐条点名——否则新部署的人只能靠猜。次级管理员看不到上游与模型
// 那两条：他也改不了，说了只会让他去找主管理员，而那正是要说的。
func bootstrapHint(b *core.Bot, uid int64, main bool) string {
	snap := b.Cache.Snap()
	var miss []string

	if main {
		if len(snap.Upstreams) == 0 {
			miss = append(miss, "• 未配置上游渠道，判定无法发出请求")
		}
		hasEnabled := false
		for _, m := range snap.Models {
			if m.Enabled {
				hasEnabled = true
				break
			}
		}
		if !hasEnabled {
			miss = append(miss, "• 未配置已启用的模型，开销无法核算")
		}
		if snap.Setting("antiad_so_model") == "" && snap.Setting("antiad_llm_model") == "" {
			miss = append(miss, "• 未选定默认判定模型，链路不会工作")
		}
	} else if snap.Setting("antiad_so_model") == "" && snap.Setting("antiad_llm_model") == "" {
		miss = append(miss, "• 主管理员尚未配置判定模型，链路不会工作")
	}

	bots := snap.BotsOwnedBy(uid, main)
	if len(bots) == 0 {
		miss = append(miss, "• 还没接入任何机器人")
	} else {
		// 主 bot 不入群、不判定，它名下没有群是设计如此，不能拿它判断
		// 「配完了没有」——否则只有主 bot 的新部署会一直看到一条永远
		// 消不掉的提示。真正要检查的是工作 bot。
		workers, hasChat := 0, false
		for _, r := range bots {
			if r.IsMain {
				continue
			}
			workers++
			if len(snap.ChatsOf(r.BotID)) > 0 {
				hasChat = true
			}
		}
		switch {
		case workers == 0 && !b.Cfg.UseWebhook():
			miss = append(miss, "• 还没有工作 bot：主 bot 只做配置管理、不判定。"+
				"本服务是长轮询模式，接不了工作 bot，需先配 public_url 切到 webhook 模式")
		case workers == 0:
			miss = append(miss, "• 还没有工作 bot：主 bot 只做配置管理、不判定。"+
				"到「🤖 我的机器人」接入一个，再把它拉进群")
		case !hasChat:
			miss = append(miss, "• 工作 bot 名下还没有群组，不会处理任何消息")
		}
	}
	return strings.Join(miss, "\n")
}

// handleAdminCallback 是 a:* 回调的总分发。
//
// 这里只做路由，权限判断在各分支内部按「这个操作动的是谁的东西」来做：
// callback_data 是客户端发上来的，路由层放行不等于操作层放行。
func HandleAdminCallback(b *core.Bot, q *tg.CallbackQuery) {
	parts := strings.Split(q.Data, ":")
	if len(parts) < 2 {
		b.AnswerCallback(q.ID, "")
		return
	}

	switch parts[1] {
	case "main":
		b.AnswerCallback(q.ID, "")
		ShowMainMenu(b, q.Message.Chat.ID, q.Message.MessageID, q.From.ID)

	// 纯展示用的占位按钮（如分页里的「2 / 5」），点了什么也不做
	case "noop":
		b.AnswerCallback(q.ID, "")

	case "mb": // a:mb —— 我的机器人（主管与次管都能进，内部按归属过滤）
		handleMyBotsCallback(b, q)

	case "ad": // a:ad —— 形态摘要与告警上的人工处置
		handleAntiAdCallback(b, q)

	// 以下四棵树只有主管理员能进：它们要么花的是他的钱（上游、模型），
	// 要么影响全平台（管理员、联合封禁、全局设置）。
	case "ga":
		handleAdminsCallback(b, q)
	case "gb":
		handleGbanCallback(b, q)
	case "st":
		handleSettingsCallback(b, q)
	case "up":
		if !b.IsMain(q.From.ID) {
			b.AnswerCallback(q.ID, "上游渠道由主管理员配置")
			return
		}
		handleUpstreamCallback(b, q)
	case "md":
		if !b.IsMain(q.From.ID) {
			b.AnswerCallback(q.ID, "模型定价由主管理员配置")
			return
		}
		handleModelCallback(b, q)

	default:
		b.AnswerCallback(q.ID, "")
	}
}

// handlePendingInput 把管理员补发的那条文本交给对应的处理器。
//
// 入口处只验「是不是管理员」，具体到「能不能动这个 bot」由各处理器
// 自己判断：从登记到输入之间有 5 分钟窗口，权限必须在执行点复查。
func HandlePendingInput(b *core.Bot, m *tg.Message, p core.PendingInput) {
	if !b.IsStaff(m.From.ID) {
		b.DropPending(m.From.ID)
		return
	}
	text := strings.TrimSpace(m.Text)

	switch p.Op {
	case "up_new_name", "up_new_url", "up_new_key":
		mainOnlyInput(b, m, p, text, handleUpstreamNewInput)

	case "up_edit_url", "up_edit_key", "up_edit_weight", "up_edit_name":
		mainOnlyInput(b, m, p, text, handleUpstreamEditInput)

	case "md_new_name", "md_new_pp", "md_new_cp", "md_new_crp", "md_new_cwp":
		mainOnlyInput(b, m, p, text, handleModelNewInput)

	case "md_edit_pp", "md_edit_cp", "md_edit_crp", "md_edit_cwp":
		mainOnlyInput(b, m, p, text, handleModelEditInput)

	case "st_edit", "st_model", "ad_dg_e", "bot_add", "bot_chat_add",
		"bot_ex_add", "bot_model_so", "bot_model_llm", "admin_add", "gban_add":
		handleSettingsInput(b, m, p, text)

	default:
		b.DropPending(m.From.ID)
	}
}

// mainOnlyInput 把一条输入交给只有主管理员能用的处理器。
func mainOnlyInput(b *core.Bot, m *tg.Message, p core.PendingInput, text string,
	fn func(*core.Bot, *tg.Message, core.PendingInput, string)) {

	if !b.IsMain(m.From.ID) {
		b.DropPending(m.From.ID)
		return
	}
	fn(b, m, p, text)
}

// btnValue 截断按钮上的当前值。模型名、群标题可以很长，
// 原样塞进按钮会把整个键盘撑得没法看。
func btnValue(v string) string {
	const max = 24
	r := []rune(v)
	if len(r) <= max {
		return v
	}
	return string(r[:max-1]) + "…"
}

// sortStrings 按字典序排序，避免菜单每次刷新顺序抖动。
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// parseIntDefault 解析一个整数，失败时返回默认值。
func parseIntDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}
