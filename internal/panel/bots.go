package panel

import (
	"fmt"
	"html"
	"slices"
	"strconv"
	"strings"
	"time"

	"menshen/internal/antiad"
	"menshen/internal/billing"
	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// ---- 我的机器人 ----

// showMyBots 列出此人能管的 bot。主管理员看全部，次管只看自己的。
func showMyBots(b *core.Bot, chatID, msgID, uid int64) {
	snap := b.Cache.Snap()
	main := b.IsMain(uid)
	list := snap.BotsOwnedBy(uid, main)

	var sb strings.Builder
	sb.WriteString("🤖 <b>机器人</b>\n\n")
	if main {
		sb.WriteString("<i>你是主管理员，这里列出全部已接入的 bot。</i>\n\n")
	}

	if len(list) == 0 {
		sb.WriteString("还没有接入任何 bot。\n\n" +
			"点下面的按钮，把 BotFather 给你的 token 发进来即可接入。")
	}
	for _, r := range list {
		mark := "✅"
		if !r.Enabled {
			mark = "⛔"
		}
		fmt.Fprintf(&sb, "%s <b>%s</b>", mark, html.EscapeString(r.Label()))
		if r.IsMain {
			// 主 bot 没有生效群、没有阈值、没有流水，列「0 个群」只会
			// 让人以为没配完。
			sb.WriteString(" 🔧 主 bot")
			if main {
				fmt.Fprintf(&sb, " ｜ 归属 <code>%d</code>", r.OwnerID)
			}
			sb.WriteString("\n   仅配置管理与接入其他 bot，不入群、不判定\n")
			continue
		}
		chats := snap.ChatsOf(r.BotID)
		on := 0
		for _, c := range chats {
			if c.Enabled {
				on++
			}
		}
		fmt.Fprintf(&sb, "\n  生效群 %d/%d", on, len(chats))
		if main {
			fmt.Fprintf(&sb, " ｜ 归属 <code>%d</code>", r.OwnerID)
		}
		sb.WriteString("\n")
	}

	rows := [][][2]string{}
	for _, r := range list {
		rows = append(rows, [][2]string{{
			"⚙️ " + btnValue(r.Label()), fmt.Sprintf("a:mb:%d", r.BotID)}})
	}
	rows = append(rows,
		[][2]string{{"➕ 接入新机器人", "a:mb:add"}},
		[][2]string{{"◀️ 返回主菜单", "a:main"}})
	b.EditOrSend(chatID, msgID, sb.String(), tg.InlineKB(rows...))
}

// addBotAndReport 在后台完成接入，并把结果写回那条「正在验证」的消息。
//
// 整条链路要打三次 TG（getMe、setMyCommands、setWebhook），最坏要等到
// 传输层超时，所以它必须跑在独立 goroutine 上：更新处理是串行的，
// 卡在这里会让整个 bot 停摆，包括别人正在用的面板。
//
// 成功失败都编辑同一条消息而不是另发：接入本来就只是一件事，
// 刷三条消息只会把刚才的面板挤出屏幕。
func addBotAndReport(b *core.Bot, chatID, uid int64, token string, progressMsg int64) {
	rec, err := b.Reg.Register(token, uid, false)
	if err != nil {
		// 接入失败最常见的原因是 token 复制少了几个字符。续上会话让他
		// 直接重发，而不是逼他回面板重点一遍按钮。
		b.KeepInput(uid, "bot_add", "")
		b.EditOrSend(chatID, progressMsg,
			"❌ "+err.Error()+"\n\n请重新发送 token：", nil)
		return
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "✅ <b>%s</b> 已接入\n", html.EscapeString(rec.Label()))
	fmt.Fprintf(&sb, "ID: <code>%d</code>\n\n", rec.BotID)

	// 走到这里必然是 webhook 模式：长轮询下 precheck 就把它拦掉了。
	if inst, ok := b.Reg.LookupID(rec.BotID); !ok {
		sb.WriteString("⚠️ 实例未能启动，请联系主管理员。\n\n")
	} else if err := inst.FinishSetup(); err != nil {
		sb.WriteString("⚠️ 回调地址注册失败，它暂时收不到消息。\n" +
			"多半是公网地址不通。修好后在下面「停用」再「启用」一次即可重试。\n\n")
	} else {
		sb.WriteString("回调地址已自动注册，它现在就能收消息了。\n\n")
	}

	sb.WriteString("下一步：把它拉进群并设为<b>管理员</b>" +
		"（需要删除消息与封禁用户权限），再回来添加该群的 chat_id。")

	b.EditOrSend(chatID, progressMsg, sb.String(), nil)
	showBotDetail(b, chatID, 0, uid, rec.BotID)
}

// showBotDetail 渲染单个 bot 的管理页。
func showBotDetail(b *core.Bot, chatID, msgID, uid, botID int64) {
	snap := b.Cache.Snap()
	rec := snap.Bots[botID]
	if rec == nil {
		b.EditOrSend(chatID, msgID, "该 bot 不存在，可能已被移除。",
			tg.InlineKB([][2]string{{"◀️ 返回", "a:mb"}}))
		return
	}
	main := b.IsMain(uid)

	if rec.IsMain {
		showMainBotDetail(b, chatID, msgID, uid, rec)
		return
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "🤖 <b>%s</b>\n\n", html.EscapeString(rec.Label()))
	if rec.Enabled {
		sb.WriteString("状态: ✅ 已启用\n")
		// 「已启用」是表里的状态，「在运行」是进程里的事实。切到长轮询
		// 模式之后，子 bot 的记录还在、面板还写着已启用，但没有实例去
		// 收它的更新 —— 不点破的话，这是个查不出来的失效。
		if b.Reg != nil {
			if _, live := b.Reg.LookupID(botID); !live {
				sb.WriteString("⚠️ <b>但它没有在运行</b>：本服务是长轮询模式，" +
					"只有配置里那个 bot 能收到更新。\n" +
					"请联系主管理员配置 <code>public_url</code> 切到 webhook 模式。\n")
			}
		}
	} else {
		sb.WriteString("状态: ⛔ 已停用（不再接收任何更新）\n")
	}
	fmt.Fprintf(&sb, "ID: <code>%d</code>\n", rec.BotID)
	if main {
		fmt.Fprintf(&sb, "归属: <code>%d</code>\n", rec.OwnerID)
	}

	so, llm := snap.ModelsFor(botID)
	fmt.Fprintf(&sb, "\n判定模型: %s\n复判模型: %s\n", modelListLabel(so), modelListLabel(llm))
	if len(so) == 0 && len(llm) == 0 {
		sb.WriteString("⚠️ 两个模型都没配，判定链路无法工作\n")
	}
	if !main {
		sb.WriteString("<i>模型由主管理员配置。</i>\n")
	}

	chats := snap.ChatsOf(botID)
	fmt.Fprintf(&sb, "\n<b>生效群</b>: %d 个\n", len(chats))
	for _, c := range chats {
		mark := "⛔"
		if c.Enabled {
			mark = "✅"
		}
		mode := "⚔️正式"
		if c.Dryrun {
			mode = "🧪演练"
		}
		title := c.Title
		if title == "" {
			title = "（未命名）"
		}
		fmt.Fprintf(&sb, "%s <code>%d</code> %s · %s\n",
			mark, c.ChatID, html.EscapeString(btnValue(title)), mode)
	}
	if len(chats) == 0 {
		sb.WriteString("（还没添加任何群，本 bot 不会处理任何消息）\n")
	}

	checked, hits, cost := botStats(b, botID)
	fmt.Fprintf(&sb, "\n<b>近 24 小时</b>: 送检 %d · 命中 %d · 开销 %s\n",
		checked, hits, billing.FormatUSDFine(cost))

	rows := [][][2]string{}
	for _, c := range chats {
		label := c.Title
		if label == "" {
			label = strconv.FormatInt(c.ChatID, 10)
		}
		rows = append(rows, [][2]string{{
			"💬 " + btnValue(label), fmt.Sprintf("a:mb:%d:c:%d", botID, c.ChatID)}})
	}
	rows = append(rows, [][2]string{{"➕ 添加群组", fmt.Sprintf("a:mb:%d:c:add", botID)}})

	if main {
		rows = append(rows, [][2]string{
			{"🤖 判定模型", fmt.Sprintf("a:mb:%d:m:so", botID)},
			{"🤖 复判模型", fmt.Sprintf("a:mb:%d:m:llm", botID)},
		})
	}
	rows = append(rows,
		[][2]string{
			{"⚙️ 阈值与参数", fmt.Sprintf("a:mb:%d:cfg", botID)},
			{"🙈 豁免用户", fmt.Sprintf("a:mb:%d:ex", botID)},
		},
		[][2]string{{"📋 拦截记录", fmt.Sprintf("a:mb:%d:log", botID)}},
	)

	onLabel := "⛔ 停用"
	if !rec.Enabled {
		onLabel = "✅ 启用"
	}
	rows = append(rows,
		[][2]string{
			{onLabel, fmt.Sprintf("a:mb:%d:on", botID)},
			{"🗑 移除", fmt.Sprintf("a:mb:%d:del", botID)},
		},
		[][2]string{{"◀️ 返回", "a:mb"}})

	b.EditOrSend(chatID, msgID, sb.String(), tg.InlineKB(rows...))
}

// showMainBotDetail 渲染主 bot 的只读页。
//
// 主 bot 只做配置管理与接入其他 bot：没有生效群、没有阈值、没有流水，
// 也不能停用/移除（停用会让面板失联，移除会在下次启动时被 ensureMainBot
// 加回来）。这一页刻意不挂任何改动状态的按钮。
func showMainBotDetail(b *core.Bot, chatID, msgID, uid int64, rec *store.BotRec) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "🔧 <b>主 bot %s</b>\n\n", html.EscapeString(rec.Label()))
	sb.WriteString("职责：<b>配置管理 + 接入其他 bot</b>。\n" +
		"不入群、不判定广告；被拉进群或频道会自动退出。\n\n")
	status := "✅ 已启用"
	if !rec.Enabled {
		status = "⛔ 已停用"
	}
	fmt.Fprintf(&sb, "状态: %s\nID: <code>%d</code>\n", status, rec.BotID)
	if b.IsMain(uid) {
		fmt.Fprintf(&sb, "归属: <code>%d</code>\n", rec.OwnerID)
	}
	sb.WriteString("\n<i>要在某个群反广告，请在「我的机器人」里接入工作 bot，" +
		"把它拉进群并设为管理员，再为它添加该群。</i>")

	b.EditOrSend(chatID, msgID, sb.String(),
		tg.InlineKB([][2]string{{"◀️ 返回", "a:mb"}}))
}

// botStats 汇总某个 bot 近 24 小时的送检量、命中数与开销。
func botStats(b *core.Bot, botID int64) (checked, hits, cost int64) {
	since := time.Now().Unix() - 86400
	b.Store.Read.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN verdict='ad' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(quota_cost),0)
		FROM antiad_log WHERE bot_id=? AND created_at >= ?`,
		botID, since).Scan(&checked, &hits, &cost)
	return
}

// showChatDetail 渲染某个 bot 在某群的配置页。
func showChatDetail(b *core.Bot, chatID, msgID, botID, targetChat int64) {
	c, ok := b.Cache.Snap().ChatConf(botID, targetChat)
	if !ok {
		b.EditOrSend(chatID, msgID, "该群已不在此 bot 名下。",
			tg.InlineKB([][2]string{{"◀️ 返回", fmt.Sprintf("a:mb:%d", botID)}}))
		return
	}

	var sb strings.Builder
	title := c.Title
	if title == "" {
		title = "（未命名）"
	}
	fmt.Fprintf(&sb, "💬 <b>%s</b>\n<code>%d</code>\n\n",
		html.EscapeString(title), c.ChatID)

	if c.Enabled {
		sb.WriteString("状态: ✅ 已启用\n")
	} else {
		sb.WriteString("状态: ⛔ 已关闭（本群不判定）\n")
	}
	if c.Dryrun {
		sb.WriteString("模式: 🧪 演练 —— 判定照跑、开销照花，但不删不禁言\n")
	} else {
		sb.WriteString("模式: ⚔️ 正式 —— 自动删除 / 禁言\n")
	}
	if c.GroupAlert {
		sb.WriteString("群内展示: 📣 开（该群管理员可直接点按钮处置）\n")
	} else {
		sb.WriteString("群内展示: 🔕 关\n")
	}
	snap := b.Cache.Snap()
	// 显示实际会执行的处罚：只写「禁言」而不写时长，很容易让人以为
	// 永久封禁已经生效（默认 24 小时，而「改为封禁」是另一个开关）。
	muteMinutes := snap.BotSettingInt(b.BotID(), "antiad_mute_minutes", 1440)
	punish := "🔇 " + antiad.MuteLabel(muteMinutes)
	if snap.BanMode(c) {
		punish = "🚫 封禁出群（永久）"
	}
	if c.Punish < 0 {
		punish += "（跟随 bot 设置）"
	}
	sb.WriteString("处罚方式: " + punish + "\n")
	if !snap.BanMode(c) && muteMinutes > 0 {
		sb.WriteString("<i>要改成永久禁言：把本 bot 参数页的「禁言时长」设为 0；" +
			"要改成永久封禁出群：把「禁言改为封禁」设为 1，或把本群处罚方式切成「封禁出群」。</i>\n")
	}

	// 实时查一次权限：bot 不是群管理员的话，整条链路静默失效，
	// 面板一切正常、日志干净、什么都没判。这是最常见的部署事故。
	sb.WriteString("\n权限自检: " + adChatHealth(b, c.ChatID) + "\n")

	enLabel := "⛔ 关闭"
	if !c.Enabled {
		enLabel = "✅ 启用"
	}
	dryLabel := "⚔️ 切到正式"
	if !c.Dryrun {
		dryLabel = "🧪 切到演练"
	}
	gaLabel := "📣 开启群内展示"
	if c.GroupAlert {
		gaLabel = "🔕 关闭群内展示"
	}

	p := fmt.Sprintf("a:mb:%d:c:%d", botID, targetChat)
	b.EditOrSend(chatID, msgID, sb.String(), tg.InlineKB(
		[][2]string{{enLabel, p + ":en"}, {dryLabel, p + ":dry"}},
		[][2]string{{gaLabel, p + ":ga"}, {"⚖️ 切换处罚方式", p + ":pn"}},
		[][2]string{{"🗑 移除该群", p + ":del"}},
		[][2]string{{"◀️ 返回", fmt.Sprintf("a:mb:%d", botID)}},
	))
}

// ---- 回调分发 ----

// handleMyBotsCallback 处理 a:mb:* 全部回调。
//
// 每个会改动状态的分支都单独调 canManageBot，不能只在入口判一次：
// callback_data 是客户端发上来的，任何次级管理员都能把别人 bot 的 id
// 拼进去试一下。
func handleMyBotsCallback(b *core.Bot, q *tg.CallbackQuery) {
	chatID, msgID := q.Message.Chat.ID, q.Message.MessageID
	uid := q.From.ID
	parts := strings.Split(q.Data, ":")

	if len(parts) == 2 { // a:mb
		b.AnswerCallback(q.ID, "")
		showMyBots(b, chatID, msgID, uid)
		return
	}

	if parts[2] == "add" {
		snap := b.Cache.Snap()
		// 长轮询模式下接不了子 bot，在这里就说清楚 —— 让人把 token
		// 发进来再告诉他不行，等于白白让一串凭证过了一遍聊天记录。
		if !b.Cfg.UseWebhook() {
			b.AnswerCallback(q.ID, "本服务是长轮询模式，接不了更多 bot")
			b.EditOrSend(chatID, msgID,
				"⛔ <b>本服务当前是长轮询模式</b>\n\n"+
					"长轮询要为每个 bot 各开一条轮询连接，几个 bot 一起轮询会撞上 "+
					"Telegram 的限速、互相挤掉配额，所以只能服务配置里那一个 bot。\n\n"+
					"接入更多 bot 需要主管理员配置 <code>public_url</code> "+
					"切到 webhook 模式。",
				tg.InlineKB([][2]string{{"◀️ 返回", "a:mb"}}))
			return
		}
		if !b.IsMain(uid) {
			limit := snap.SettingInt("max_bots_per_admin", 5)
			if limit > 0 && int64(len(snap.BotsOwnedBy(uid, false))) >= limit {
				b.AnswerCallback(q.ID, fmt.Sprintf("已达上限（%d 个）", limit))
				return
			}
		}
		b.AnswerCallback(q.ID, "")
		b.AskInput(chatID, uid, "bot_add", "",
			"请把要接入的 bot token 发给我（BotFather 给的那一串，"+
				"形如 <code>123456789:AA...</code>）：\n\n"+
				"我会自动验证它、取回 bot 的身份并注册回调地址，"+
				"接入后它归你管理，你可以为它添加群组、调整阈值。\n"+
				"<i>token 等同于该 bot 的完整控制权，你发的那条消息会被自动删除。</i>")
		return
	}

	botID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		b.AnswerCallback(q.ID, "参数无效")
		return
	}
	if !b.CanManageBot(uid, botID) {
		// 静默：不向无关的人确认这个 bot 是否存在。
		b.AnswerCallback(q.ID, "")
		return
	}

	if len(parts) == 3 { // a:mb:<id>
		b.AnswerCallback(q.ID, "")
		showBotDetail(b, chatID, msgID, uid, botID)
		return
	}

	// 主 bot 只有只读页：详情页上的按钮已经收起来了，这里再挡一道 ——
	// callback_data 是客户端发上来的，任何人都能手工拼出来。
	if rec := b.Cache.Snap().Bots[botID]; rec != nil && rec.IsMain {
		b.AnswerCallback(q.ID, "主 bot 只做配置管理与接入其他 bot")
		return
	}

	switch parts[3] {
	case "c": // 群相关
		handleBotChatCallback(b, q, botID, parts)

	case "cfg": // 阈值：复用设置面板，scope 切到这个 bot
		b.AnswerCallback(q.ID, "")
		showBotConfig(b, chatID, msgID, botID)

	case "ex": // 豁免用户
		b.AnswerCallback(q.ID, "")
		showBotExempt(b, chatID, msgID, botID)

	case "exa": // a:mb:<id>:exa —— 添加豁免
		b.AnswerCallback(q.ID, "")
		b.AskInput(chatID, uid, "bot_ex_add", parts[2],
			"请输入要豁免反广告检查的 <b>TG user_id</b>（纯数字）：")

	case "exd": // a:mb:<id>:exd:<uid> —— 移除豁免
		if len(parts) < 5 {
			b.AnswerCallback(q.ID, "参数缺失")
			return
		}
		target, err := strconv.ParseInt(parts[4], 10, 64)
		if err != nil {
			b.AnswerCallback(q.ID, "参数无效")
			return
		}
		list := b.Cache.Snap().BotSettingInt64List(botID, "antiad_exempt_users")
		out := make([]int64, 0, len(list))
		for _, v := range list {
			if v != target {
				out = append(out, v)
			}
		}
		if err := b.PutBotInt64List(botID, "antiad_exempt_users", out); err != nil {
			b.AnswerCallback(q.ID, "移除失败")
			return
		}
		b.AnswerCallback(q.ID, "已移除")
		showBotExempt(b, chatID, msgID, botID)

	case "m": // a:mb:<id>:m:so|llm —— 模型，仅主管理员
		if !b.IsMain(uid) {
			b.AnswerCallback(q.ID, "模型由主管理员配置")
			return
		}
		if len(parts) < 5 {
			b.AnswerCallback(q.ID, "参数缺失")
			return
		}
		which, label := "so", "判定模型（systemone）"
		if parts[4] == "llm" {
			which, label = "llm", "复判/总结模型（大模型）"
		}
		b.AnswerCallback(q.ID, "")
		b.AskInput(chatID, uid, "bot_model_"+which, parts[2],
			"请输入该 bot 专用的<b>"+label+"</b>列表，按<b>重试顺序</b>用逗号分隔：\n"+
				"<code>a/gpt-5-mini, b/gpt-5-mini</code>\n\n"+
				"每项形如 <code>上游名/模型ID</code>，必须已存在于「模型定价」且为启用状态。\n"+
				"填 <code>-</code> 表示沿用全局默认。")

	case "wd": // a:mb:<botID>:wd:<chat>:<uid> —— 移除一条白名单
		if len(parts) < 6 {
			b.AnswerCallback(q.ID, "参数缺失")
			return
		}
		targetChat, err1 := strconv.ParseInt(parts[4], 10, 64)
		targetUID, err2 := strconv.ParseInt(parts[5], 10, 64)
		if err1 != nil || err2 != nil {
			b.AnswerCallback(q.ID, "参数无效")
			return
		}
		if err := antiad.RemoveWhitelist(b.Shared, botID, targetChat, targetUID); err != nil {
			b.AnswerCallback(q.ID, opText(err, "移除失败"))
			return
		}
		// 全平台白名单只有主管理员能撤。
		if b.IsMain(uid) {
			if err := antiad.RemoveWhitelist(b.Shared, 0, targetChat, targetUID); err != nil {
				b.AnswerCallback(q.ID, opText(err, "移除失败"))
				return
			}
		}
		b.AnswerCallback(q.ID, "已移除")
		showBotExempt(b, chatID, 0, botID)

	case "log": // a:mb:<id>:log[:<page>:<all>]
		page, all := 1, false
		if len(parts) >= 6 {
			page = parseIntDefault(parts[4], 1)
			all = parts[5] == "true"
		}
		b.AnswerCallback(q.ID, "")
		showAntiAdLog(b, chatID, msgID, botID, page, all)

	case "on": // 启停
		rec := b.Cache.Snap().Bots[botID]
		if rec == nil {
			b.AnswerCallback(q.ID, "该 bot 不存在")
			return
		}
		if b.Reg == nil {
			b.AnswerCallback(q.ID, "内部错误")
			return
		}
		if err := b.Reg.SetBotEnabled(botID, !rec.Enabled); err != nil {
			b.AnswerCallback(q.ID, "切换失败")
			return
		}
		b.AnswerCallback(q.ID, "已切换")
		showBotDetail(b, chatID, msgID, uid, botID)

	case "del": // a:mb:<id>:del[:y]
		if len(parts) < 5 || parts[4] != "y" {
			rec := b.Cache.Snap().Bots[botID]
			if rec == nil {
				b.AnswerCallback(q.ID, "该 bot 不存在")
				return
			}
			b.AnswerCallback(q.ID, "")
			b.Edit(chatID, msgID, fmt.Sprintf(
				"⚠️ 确认移除 <b>%s</b>？\n\n它的群配置与阈值会一并删除，"+
					"webhook 也会被撤销。判定流水保留。",
				html.EscapeString(rec.Label())),
				tg.InlineKB(
					[][2]string{{"✅ 确认移除", fmt.Sprintf("a:mb:%d:del:y", botID)}},
					[][2]string{{"◀️ 取消", fmt.Sprintf("a:mb:%d", botID)}},
				))
			return
		}
		if b.Reg == nil {
			b.AnswerCallback(q.ID, "内部错误")
			return
		}
		if err := b.Reg.Unregister(botID); err != nil {
			b.AnswerCallback(q.ID, "移除失败")
			return
		}
		b.AnswerCallback(q.ID, "已移除")
		showMyBots(b, chatID, msgID, uid)

	default:
		b.AnswerCallback(q.ID, "")
	}
}

// handleBotChatCallback 处理 a:mb:<botID>:c:* 这一支。
func handleBotChatCallback(b *core.Bot, q *tg.CallbackQuery, botID int64, parts []string) {
	chatID, msgID := q.Message.Chat.ID, q.Message.MessageID
	uid := q.From.ID

	if len(parts) < 5 {
		b.AnswerCallback(q.ID, "参数缺失")
		return
	}
	if parts[4] == "add" {
		b.AnswerCallback(q.ID, "")
		b.AskInput(chatID, uid, "bot_chat_add", strconv.FormatInt(botID, 10),
			"请输入要启用反广告的群组 <b>chat_id</b>（形如 <code>-1001234567890</code>）：\n\n"+
				"⚠️ bot 必须是该群的<b>管理员</b>并拥有删除消息、封禁用户权限，"+
				"否则它连普通群消息都收不到，反广告完全无法工作。\n"+
				"新添加的群默认是<b>演练模式</b>，观察几天再切正式。")
		return
	}

	targetChat, err := strconv.ParseInt(parts[4], 10, 64)
	if err != nil {
		b.AnswerCallback(q.ID, "参数无效")
		return
	}
	if !b.CanManageChat(uid, botID, targetChat) {
		b.AnswerCallback(q.ID, "")
		return
	}

	if len(parts) == 5 { // a:mb:<id>:c:<chat>
		b.AnswerCallback(q.ID, "")
		showChatDetail(b, chatID, msgID, botID, targetChat)
		return
	}

	c, _ := b.Cache.Snap().ChatConf(botID, targetChat)
	var p core.ChatPatch
	switch parts[5] {
	case "en":
		p.Enabled = ptr(!c.Enabled)
	case "dry":
		p.Dryrun = ptr(!c.Dryrun)
	case "ga":
		p.GroupAlert = ptr(!c.GroupAlert)
	case "pn":
		// 轮换：跟随 bot 设置 → 禁言 → 封禁 → 跟随。
		p.Punish = ptr(map[int64]int64{-1: 0, 0: 1, 1: -1}[c.Punish])
	case "del":
		if err = b.RemoveChat(botID, targetChat); err == nil {
			b.AnswerCallback(q.ID, "已移除")
			showBotDetail(b, chatID, msgID, uid, botID)
			return
		}
	default:
		b.AnswerCallback(q.ID, "")
		return
	}
	if parts[5] != "del" {
		_, err = b.UpdateChats(botID, []int64{targetChat}, p)
	}

	if err != nil {
		b.AnswerCallback(q.ID, "操作失败")
		return
	}
	b.AnswerCallback(q.ID, "已切换")
	showChatDetail(b, chatID, msgID, botID, targetChat)
}

// showBotConfig 渲染某个 bot 的阈值与参数页。
func showBotConfig(b *core.Bot, chatID, msgID, botID int64) {
	snap := b.Cache.Snap()

	var sb strings.Builder
	sb.WriteString("⚙️ <b>阈值与参数</b>\n\n")
	if rec := snap.Bots[botID]; rec != nil && rec.IsMain {
		// 主 bot 不入群、不判定：在它这里改判定参数不会对任何群生效，
		// 用户很容易改完以为已经生效。
		sb.WriteString("⚠️ <b>这是主 bot</b>：它只做配置管理与接入其他 bot，" +
			"不入群、不判定。这里改的参数不会作用于任何群——" +
			"请到工作 bot 的参数页设置。\n\n")
	}
	sb.WriteString("采信线以下的判定会转给大模型复判；\n")
	sb.WriteString("处置线决定删除与禁言的触发点，新人比老人低一档。\n")
	sb.WriteString("<i>带 ✏️ 的是本 bot 的专属值，其余沿用全局默认。</i>\n\n")

	var rows [][][2]string
	for _, g := range specsInSections(func(sp settingSpec) bool {
		return sp.group == "antiad" || sp.group == "both"
	}) {
		sb.WriteString("\n<b>" + html.EscapeString(g.Name) + "</b>\n")
		for _, sp := range g.Specs {
			cur := snap.BotSetting(botID, sp.key)
			mark := ""
			if _, overridden := snap.BotSettings[botID][sp.key]; overridden {
				mark = "✏️"
			}
			fmt.Fprintf(&sb, "• %s: <code>%s</code> %s\n",
				html.EscapeString(sp.label), html.EscapeString(cur), mark)
		}
	}
	for _, sp := range specsInGroup("antiad") {
		cur := snap.BotSetting(botID, sp.key)
		rows = append(rows, [][2]string{{
			fmt.Sprintf("%s (%s)", sp.label, cur),
			fmt.Sprintf("a:st:b:%d:%s", botID, sp.key),
		}})
	}
	rows = append(rows, [][2]string{{"◀️ 返回", fmt.Sprintf("a:mb:%d", botID)}})
	b.EditOrSend(chatID, msgID, sb.String(), tg.InlineKB(rows...))
}

// showBotExempt 渲染某个 bot 的豁免名单。
func showBotExempt(b *core.Bot, chatID, msgID, botID int64) {
	list := b.Cache.Snap().BotSettingInt64List(botID, "antiad_exempt_users")

	var sb strings.Builder
	sb.WriteString("🙈 <b>豁免用户</b>\n\n")
	sb.WriteString("名单里的人发言不进判定，一分钱不花。\n")
	sb.WriteString("<i>群管理员、主管理员与本 bot 的归属人自动豁免，不必加。</i>\n\n")
	if len(list) == 0 {
		sb.WriteString("（名单为空）\n")
	}
	for _, uid := range list {
		fmt.Fprintf(&sb, "• <code>%d</code>\n", uid)
	}

	rows := [][][2]string{{{"➕ 添加豁免", fmt.Sprintf("a:mb:%d:exa", botID)}}}
	for _, uid := range list {
		rows = append(rows, [][2]string{{
			fmt.Sprintf("🗑 移除 %d", uid),
			fmt.Sprintf("a:mb:%d:exd:%d", botID, uid)}})
	}

	// 解禁码 / /white 产生的白名单：群范围或全平台，到期自动失效。
	type wl struct {
		botID, chatID, uid, expiresAt int64
		source                        string
	}
	var whitelist []wl
	rowsW, err := b.Store.Read.Query(`SELECT bot_id,chat_id,user_id,expires_at,source
		FROM ad_whitelist WHERE bot_id=? OR bot_id=0
		ORDER BY user_id, chat_id`, botID)
	if err == nil {
		for rowsW.Next() {
			var w wl
			if rowsW.Scan(&w.botID, &w.chatID, &w.uid, &w.expiresAt, &w.source) == nil {
				whitelist = append(whitelist, w)
			}
		}
		rowsW.Close()
	}
	if len(whitelist) > 0 {
		sb.WriteString("\n<b>白名单</b>（免于反广告检查）\n")
		loc := b.Cache.Snap().Location()
		for _, w := range whitelist {
			scope := fmt.Sprintf("群 <code>%d</code>", w.chatID)
			if w.chatID == 0 {
				scope = "本 bot 所有群"
			}
			if w.botID == 0 {
				scope = "全平台"
			}
			expires := "永久"
			if w.expiresAt != 0 {
				expires = time.Unix(w.expiresAt, 0).In(loc).Format("01-02 15:04")
			}
			fmt.Fprintf(&sb, "• <code>%d</code> ｜ %s ｜ %s ｜ 至 %s\n",
				w.uid, scope, html.EscapeString(w.source), expires)
			rows = append(rows, [][2]string{{
				fmt.Sprintf("🗑 白名单 %d @%d", w.uid, w.chatID),
				fmt.Sprintf("a:mb:%d:wd:%d:%d", botID, w.chatID, w.uid)}})
		}
	}

	rows = append(rows, [][2]string{{"◀️ 返回", fmt.Sprintf("a:mb:%d", botID)}})
	b.EditOrSend(chatID, msgID, sb.String(), tg.InlineKB(rows...))
}

// HandleWhiteDM 处理管理员私聊的 /white <uid>：
//   - 加入本 bot 的豁免名单（这台 bot 名下所有群不进判定）；
//   - 解除联合封禁（全局组对所有管理员开放，专属组只动自己的账本）。
//
// 群里的 /white 是另一回事（本群白名单 + 本群解封，见 antiad.HandleAdwCommand）。
func HandleWhiteDM(b *core.Bot, m *tg.Message, text string) {
	chatID := m.Chat.ID
	// 与面板内所有 a:mb:* 分支同一条权限规则：次管只能管自己名下的 bot。
	// 少了这道门，任何次管都能给别人的 bot 加豁免，把它变成漏判通道。
	if !b.CanManageBot(m.From.ID, b.BotID()) {
		b.Send(chatID, "你只能管理自己名下的 bot。", nil)
		return
	}
	fields := strings.Fields(strings.TrimPrefix(text, "/white"))
	if len(fields) > 0 && strings.HasPrefix(fields[0], "@") {
		fields = fields[1:] // /white@botname <uid>
	}
	usage := "用法：<code>/white &lt;user_id&gt;</code>\n\n" +
		"把某人加入本 bot 的豁免名单（名下所有群不进判定），并解除联合封禁" +
		"（全局组 + 你的专属组）。"
	if len(fields) == 0 {
		b.Send(chatID, usage, nil)
		return
	}
	uid, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || uid == 0 {
		b.Send(chatID, usage, nil)
		return
	}
	var notes []string
	list := b.Cache.Snap().BotSettingInt64List(b.BotID(), "antiad_exempt_users")
	if !slices.Contains(list, uid) {
		if err := b.PutBotInt64List(b.BotID(), "antiad_exempt_users",
			append(list, uid)); err != nil {
			b.Send(chatID, "保存失败。", nil)
			return
		}
		notes = append(notes, "已加入本 bot 的豁免名单")
	}
	if scope := antiad.AdminLiftGban(b.Shared, m.From.ID, uid); scope != "" {
		notes = append(notes, "已解除"+scope+"（含各群解封）")
	}
	if len(notes) == 0 {
		b.Send(chatID, fmt.Sprintf(
			"<code>%d</code> 已经在豁免名单里，也不在任何联合封禁名单里。", uid), nil)
		return
	}
	b.Send(chatID, fmt.Sprintf("✅ <code>%d</code>：%s。", uid,
		strings.Join(notes, "；")), nil)
}

// ---- 管理员面板（仅主管理员）----

func showAdmins(b *core.Bot, chatID, msgID int64) {
	snap := b.Cache.Snap()

	var sb strings.Builder
	sb.WriteString("👥 <b>管理员</b>\n\n<b>主管理员</b>（来自配置文件，面板改不了）\n")
	for _, id := range b.Cfg.AdminIDs {
		fmt.Fprintf(&sb, "• <code>%d</code>\n", id)
	}

	sb.WriteString("\n<b>次级管理员</b>\n")
	if len(snap.Admins) == 0 {
		sb.WriteString("（还没有）\n")
	}
	// 排序后输出，避免每次刷新顺序抖动。
	ids := make([]int64, 0, len(snap.Admins))
	for id := range snap.Admins {
		ids = append(ids, id)
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
	for _, id := range ids {
		a := snap.Admins[id]
		note := a.Note
		if note == "" {
			note = "（无备注）"
		}
		n := len(snap.BotsOwnedBy(id, false))
		fmt.Fprintf(&sb, "• <code>%d</code> %s ｜ %d 个 bot\n",
			id, html.EscapeString(btnValue(note)), n)
	}

	sb.WriteString("\n<i>次级管理员可以接入自己的 bot、管理自己 bot 的群与阈值，" +
		"但碰不到上游渠道、模型定价和别人的 bot。</i>\n")

	rows := [][][2]string{{{"➕ 添加次级管理员", "a:ga:add"}}}
	for _, id := range ids {
		rows = append(rows, [][2]string{{
			fmt.Sprintf("🗑 移除 %d", id), fmt.Sprintf("a:ga:del:%d", id)}})
	}
	rows = append(rows, [][2]string{{"◀️ 返回主菜单", "a:main"}})
	b.EditOrSend(chatID, msgID, sb.String(), tg.InlineKB(rows...))
}

func handleAdminsCallback(b *core.Bot, q *tg.CallbackQuery) {
	chatID, msgID := q.Message.Chat.ID, q.Message.MessageID
	if !b.IsMain(q.From.ID) {
		b.AnswerCallback(q.ID, "")
		return
	}
	parts := strings.Split(q.Data, ":")

	if len(parts) == 2 {
		b.AnswerCallback(q.ID, "")
		showAdmins(b, chatID, msgID)
		return
	}

	switch parts[2] {
	case "add":
		b.AnswerCallback(q.ID, "")
		b.AskInput(chatID, q.From.ID, "admin_add", "",
			"请输入要添加为次级管理员的 <b>TG user_id</b>（纯数字），"+
				"后面可以跟一个备注，例如：\n<code>123456789 小王</code>")

	case "del":
		if len(parts) < 4 {
			b.AnswerCallback(q.ID, "参数缺失")
			return
		}
		uid, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil {
			b.AnswerCallback(q.ID, "参数无效")
			return
		}
		if err := b.RemoveAdmin(uid); err != nil {
			b.AnswerCallback(q.ID, "移除失败")
			return
		}
		b.AnswerCallback(q.ID, "已移除（他名下的 bot 仍在运行）")
		showAdmins(b, chatID, msgID)

	default:
		b.AnswerCallback(q.ID, "")
	}
}

// ---- 联合封禁面板（仅主管理员）----

const gbanPageSize = 10

func showGban(b *core.Bot, chatID, msgID int64, page int) {
	page = clampPageInt(page)
	on := antiad.GbanEnabled(b.Shared)
	total := len(b.Cache.Snap().Gban)

	var sb strings.Builder
	sb.WriteString("🚫 <b>联合封禁</b>\n\n")
	if on {
		sb.WriteString("状态: ✅ 已启用\n\n")
	} else {
		sb.WriteString("状态: ⛔ 已关闭\n\n")
	}
	sb.WriteString("开启后：任一接入群把某人判为广告（最高档）或被人工标记，" +
		"他会在<b>全平台所有 bot 的所有生效群</b>一起被封；" +
		"之后再进任何一个群都会当场拦下。\n" +
		"标记误判会同时把他从名单里放出来并全平台解封。\n\n")
	fmt.Fprintf(&sb, "<b>名单</b>: %d 人（第 %d 页）\n", total, page)

	loc := b.Cache.Snap().Location()
	list := antiad.GbanList(b.Shared, gbanPageSize, (page-1)*gbanPageSize)
	for _, g := range list {
		when := time.Unix(g.CreatedAt, 0).In(loc).Format("01-02 15:04")
		fmt.Fprintf(&sb, "• <code>%d</code> <i>%s</i>\n  %s\n",
			g.UserID, when, html.EscapeString(antiad.GbanReasonLabel(g.Reason)))
	}
	if total == 0 {
		sb.WriteString("（名单为空）\n")
	}

	toggle := "✅ 启用联合封禁"
	if on {
		toggle = "⛔ 关闭联合封禁"
	}
	rows := [][][2]string{
		{{toggle, "a:gb:on"}},
		{{"➕ 手工加入名单", "a:gb:add"}},
	}
	for _, g := range list {
		rows = append(rows, [][2]string{{
			fmt.Sprintf("🔓 解除 %d", g.UserID),
			fmt.Sprintf("a:gb:del:%d", g.UserID)}})
	}

	var nav [][2]string
	if page > 1 {
		nav = append(nav, [2]string{"◀️ 上一页", fmt.Sprintf("a:gb:p:%d", page-1)})
	}
	if len(list) == gbanPageSize {
		nav = append(nav, [2]string{"下一页 ▶️", fmt.Sprintf("a:gb:p:%d", page+1)})
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, [][2]string{{"◀️ 返回主菜单", "a:main"}})
	b.EditOrSend(chatID, msgID, sb.String(), tg.InlineKB(rows...))
}

func handleGbanCallback(b *core.Bot, q *tg.CallbackQuery) {
	chatID, msgID := q.Message.Chat.ID, q.Message.MessageID
	if !b.IsMain(q.From.ID) {
		b.AnswerCallback(q.ID, "")
		return
	}
	parts := strings.Split(q.Data, ":")

	if len(parts) == 2 {
		b.AnswerCallback(q.ID, "")
		showGban(b, chatID, msgID, 1)
		return
	}

	switch parts[2] {
	case "on":
		next := "1"
		if antiad.GbanEnabled(b.Shared) {
			next = "0"
		}
		if err := setSetting(b.Shared, q.From.ID, 0, "gban_enabled", next); err != nil {
			b.AnswerCallback(q.ID, opText(err, "切换失败"))
			return
		}
		if next == "1" {
			b.AnswerCallback(q.ID, "已启用，此后的高置信命中将全平台封禁")
		} else {
			b.AnswerCallback(q.ID, "已关闭（名单保留，但不再执行）")
		}
		showGban(b, chatID, msgID, 1)

	case "add":
		b.AnswerCallback(q.ID, "")
		b.AskInput(chatID, q.From.ID, "gban_add", "",
			"请输入要加入联合封禁名单的 <b>TG user_id</b>（纯数字），"+
				"后面可以跟理由，例如：\n<code>123456789 跨群刷广告</code>\n\n"+
				"⚠️ 他会立即在<b>所有接入群</b>被封禁。")

	case "del":
		if len(parts) < 4 {
			b.AnswerCallback(q.ID, "参数缺失")
			return
		}
		uid, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil {
			b.AnswerCallback(q.ID, "参数无效")
			return
		}
		b.AnswerCallback(q.ID, "正在全平台解封")
		go antiad.LiftGban(b.Shared, uid)
		showGban(b, chatID, msgID, 1)

	case "p":
		page := 1
		if len(parts) >= 4 {
			if n, err := strconv.Atoi(parts[3]); err == nil {
				page = n
			}
		}
		b.AnswerCallback(q.ID, "")
		showGban(b, chatID, msgID, page)

	default:
		b.AnswerCallback(q.ID, "")
	}
}
