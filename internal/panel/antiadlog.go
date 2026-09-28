package panel

import (
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"menshen/internal/antiad"
	"menshen/internal/core"
	"menshen/internal/tg"
)

// appendReason 在原有理由后追加一条人工操作痕迹。
func appendReason(old, add string) string {
	if old == "" {
		return add
	}
	return old + " | " + add
}

// adTodayStats 汇总当日战绩：送检数、命中数、误判数、开销。
// 开销必须可见 —— 全量送检的成本是这个功能最大的风险点。
func adTodayStats(b *core.Bot) (checked, hits, fps, cost int64) {
	since := time.Now().Unix() - 86400
	err := b.Store.Read.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN verdict!='skipped' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN verdict='ad' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN action='undone' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(quota_cost),0)
		FROM antiad_log WHERE created_at >= ?`, since).
		Scan(&checked, &hits, &fps, &cost)
	if err != nil {
		slog.Error("反广告：统计失败", "err", err)
	}
	return
}

// adChatHealth 检查 bot 在该群的权限，返回一句人话。
// 结果不缓存：面板是低频操作，而这里恰恰要的是实时真相。
func adChatHealth(b *core.Bot, chatID int64) string {
	raw, err := b.TG.Call("getChatMember", map[string]any{
		"chat_id": chatID, "user_id": b.BotID(),
	})
	if err != nil {
		return "⚠️ 无法查询"
	}
	var resp tg.ChatMemberResp
	if json.Unmarshal(raw, &resp) != nil || !resp.OK {
		// 生效群列表是全局的，而面板是某一个 bot 的视图：多 bot 接入时
		// 本 bot 本来就可能不在别人管的群里，这不是故障。
		return "— 本 bot 不在该群（或 chat_id 配错）"
	}
	switch resp.Result.Status {
	case "administrator", "creator":
		return "✅ 权限正常"
	}
	return "⚠️ bot 非管理员，该群收不到普通消息"
}

func modelLabel(v string) string {
	if v == "" {
		return "（未配置）"
	}
	return "<code>" + html.EscapeString(v) + "</code>"
}

// modelListLabel 渲染模型列表：按重试顺序用逗号连起来。
func modelListLabel(v []string) string {
	if len(v) == 0 {
		return "（未配置）"
	}
	return "<code>" + html.EscapeString(strings.Join(v, ", ")) + "</code>"
}

// showAntiAdDigest 渲染形态摘要页。
func showAntiAdDigest(b *core.Bot, chatID, msgID int64) {
	snap := b.Cache.Snap()
	digest := snap.Setting("antiad_digest")

	var sb strings.Builder
	sb.WriteString("📝 <b>广告形态摘要</b>\n\n")
	if digest == "" {
		fmt.Fprintf(&sb, "（尚未生成）\n\n积累 %d 条已确认广告后自动总结。\n",
			snap.SettingInt("antiad_digest_min", 5))
	} else {
		fmt.Fprintf(&sb, "<code>%s</code>\n\n共 %d 字，已消费到流水 #%d。\n",
			html.EscapeString(digest), len([]rune(digest)),
			snap.SettingInt("antiad_digest_last_id", 0))
		sb.WriteString("\n这段文字会随每一条群消息发给判定模型，" +
			"越长越准也越贵。管理员可直接编辑修正。\n")
	}

	b.Edit(chatID, msgID, sb.String(), tg.InlineKB(
		[][2]string{{"✏️ 手工编辑", "a:ad:dg:e"}, {"🔄 立即重新总结", "a:ad:dg:r"}},
		[][2]string{{"🗑 清空", "a:ad:dg:c"}},
		[][2]string{{"◀️ 返回", "a:ad"}},
	))
}

const adLogPageSize = 10

// showAntiAdLog 渲染某个 bot 的拦截记录。all=true 时连 clean 一起列 ——
// 演练期管理员真正要看的是「有没有把正常消息判成广告」。
//
// 按 bot_id 过滤是权限边界的一部分：次级管理员只能看自己 bot 判的东西，
// 而流水里有群消息原文。
func showAntiAdLog(b *core.Bot, chatID, msgID, botID int64, page int, all bool) {
	if page < 1 {
		page = 1
	}
	where := `WHERE bot_id=?`
	if !all {
		where += ` AND verdict='ad'`
	}
	back := fmt.Sprintf("a:mb:%d", botID)

	rows, err := b.Store.Read.Query(`SELECT id,chat_id,user_id,verdict,confidence,
		action,ad_kind,text,created_at FROM antiad_log `+where+`
		ORDER BY id DESC LIMIT ? OFFSET ?`,
		botID, adLogPageSize, (page-1)*adLogPageSize)
	if err != nil {
		b.Edit(chatID, msgID, "读取记录失败。", tg.InlineKB(
			[][2]string{{"◀️ 返回", back}}))
		return
	}
	defer rows.Close()

	var sb strings.Builder
	title := "📋 <b>拦截记录</b>"
	if all {
		title = "📋 <b>全部判定记录</b>"
	}
	fmt.Fprintf(&sb, "%s（第 %d 页）\n\n", title, page)

	tz := b.Cache.Snap().SettingInt("tz_offset", 8)
	n := 0
	for rows.Next() {
		var id, cid, uid, at int64
		var verdict, action, kind, text string
		var conf float64
		if err := rows.Scan(&id, &cid, &uid, &verdict, &conf, &action,
			&kind, &text, &at); err != nil {
			slog.Warn("反广告：记录行解析失败，已跳过", "err", err)
			continue
		}
		n++
		mark := "✅"
		if verdict == "ad" {
			mark = "🚫"
		} else if verdict == "error" {
			mark = "⚠️"
		} else if verdict == "skipped" {
			mark = "⏭" // 护栏拦下、没有送检
		}
		// 时刻按 settings.tz_offset 呈现：管理员看到的时间必须是本地时间，
		// UTC 会让人对不上号。
		when := time.Unix(at+tz*3600, 0).UTC().Format("01-02 15:04")
		// chat_id/ad_kind 都要渲染出来——这个页面是跨所有生效群的全局列表，
		// 不带群号就看不出命中来自哪个群；ad_kind 是复盘形态时最直接的分类线索。
		kindSuffix := ""
		if kind != "" {
			kindSuffix = " [" + html.EscapeString(antiad.AdKindLabel(kind)) + "]"
		}
		fmt.Fprintf(&sb, "• %s <code>%s</code> #%d 群<code>%d</code> 用户<code>%d</code> %.0f%% %s%s\n  <i>%s</i>\n",
			mark, when, id, cid, uid, conf*100, html.EscapeString(actionLabel(action)),
			kindSuffix, html.EscapeString(core.TruncateRunes(text, 60)))
	}
	// 不查 rows.Err() 就会把「只扫到一半」悄悄当成「扫完了」——拦截记录是
	// 合规/复盘的审计面，静默截断还会让 n < adLogPageSize 连「下一页」按钮
	// 一起消失，管理员会误判「记录就这些」。
	if err := rows.Err(); err != nil {
		slog.Error("反广告：记录读取中断", "err", err)
		sb.WriteString("\n⚠️ 读取中断，本页可能不完整\n")
	}
	if n == 0 {
		sb.WriteString("（暂无记录）\n")
	}

	// 翻页 callback 形如 a:mb:<botID>:log:<page>:<all>，
	// 与 bot 详情页同属一棵树，权限判断在那里统一做。
	var navRow [][2]string
	if page > 1 {
		navRow = append(navRow, [2]string{"◀️ 上一页",
			fmt.Sprintf("a:mb:%d:log:%d:%v", botID, page-1, all)})
	}
	if n == adLogPageSize {
		navRow = append(navRow, [2]string{"下一页 ▶️",
			fmt.Sprintf("a:mb:%d:log:%d:%v", botID, page+1, all)})
	}

	rowsKB := [][][2]string{}
	if len(navRow) > 0 {
		rowsKB = append(rowsKB, navRow)
	}
	if all {
		rowsKB = append(rowsKB, [][2]string{
			{"🚫 只看命中", fmt.Sprintf("a:mb:%d:log:1:false", botID)}})
	} else {
		rowsKB = append(rowsKB, [][2]string{
			{"🔍 全部判定", fmt.Sprintf("a:mb:%d:log:1:true", botID)}})
	}
	rowsKB = append(rowsKB, [][2]string{{"◀️ 返回", back}})
	b.Edit(chatID, msgID, sb.String(), tg.InlineKB(rowsKB...))
}

// actionLabel 见 antiad.ActionLabel：词汇表归写流水的那一方所有。
func actionLabel(a string) string { return antiad.ActionLabel(a) }

// handleAntiAdCallback 处理 a:ad:* 回调。
//
// 多租户改造后这里只剩两类：形态摘要（全局一份，仅主管理员可动）与
// 告警消息上的人工处置。面板导航、阈值、生效群都迁到了 a:mb 那棵树上，
// 因为它们全都属于某一个 bot。
func handleAntiAdCallback(b *core.Bot, q *tg.CallbackQuery) {
	chatID, msgID := q.Message.Chat.ID, q.Message.MessageID
	parts := strings.Split(q.Data, ":")
	if len(parts) < 3 {
		b.AnswerCallback(q.ID, "")
		return
	}

	switch parts[2] {
	case "dg": // a:ad:dg[:e|:r|:c]
		if !b.IsMain(q.From.ID) {
			// 摘要是全局一份、影响所有接入方的判定，只有主管理员能改。
			b.AnswerCallback(q.ID, "形态摘要由主管理员维护")
			return
		}
		if len(parts) == 3 {
			b.AnswerCallback(q.ID, "")
			showAntiAdDigest(b, chatID, msgID)
			return
		}
		switch parts[3] {
		case "e":
			b.AnswerCallback(q.ID, "")
			b.AskInput(chatID, q.From.ID, "ad_dg_e", "",
				"请输入新的<b>形态摘要</b>正文。填 <code>-</code> 可清空。\n\n"+
					"建议保留两个小标题：\n<code>"+antiad.DigestAdHeader+"</code> 与 <code>"+
					antiad.DigestFPHeader+"</code>，系统按它们切分正例与反例。")
		case "r":
			b.AnswerCallback(q.ID, "正在总结，稍候刷新查看")
			go antiad.RunAdDigest(b.Shared, true) // 忽略样本数阈值，立刻跑一轮
		case "c":
			if err := b.PutSetting("antiad_digest", ""); err != nil {
				b.AnswerCallback(q.ID, "清空失败")
				return
			}
			b.AnswerCallback(q.ID, "已清空")
			showAntiAdDigest(b, chatID, msgID)
		default:
			b.AnswerCallback(q.ID, "")
		}

	case "rec": // a:ad:rec:<log_id> —— 私聊汇总里点进来的记录卡片
		if len(parts) < 4 {
			b.AnswerCallback(q.ID, "参数缺失")
			return
		}
		id, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil {
			b.AnswerCallback(q.ID, "参数无效")
			return
		}
		row, ok := antiad.LoadAdLog(b.Store, id)
		// 卡片带原文，按记录所属的 bot 判权限：callback_data 是客户端发上来的，
		// 次级管理员能把别人 bot 的记录号拼进去。
		if !ok || !b.CanManageBot(q.From.ID, row.BotID) {
			b.AnswerCallback(q.ID, "记录不存在或已过保留期")
			return
		}
		b.AnswerCallback(q.ID, "")
		text, kb := antiad.RenderAdRecord(b, row)
		b.Send(chatID, text, kb)

	case "ok", "fp", "del", "mute", "ban":
		if len(parts) < 4 {
			b.AnswerCallback(q.ID, "参数缺失")
			return
		}
		id, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil {
			b.AnswerCallback(q.ID, "参数无效")
			return
		}
		row, ok := antiad.LoadAdLog(b.Store, id)
		if !ok {
			b.AnswerCallback(q.ID, "记录不存在或已过保留期")
			return
		}
		if !adDispositionAllowed(b, q.From.ID, row) {
			// 静默：与 a: 前缀的既有做法一致，不向无关人员确认按钮存在。
			b.AnswerCallback(q.ID, "")
			return
		}
		applyAdManualAction(b, q, parts[2], row)

	default:
		b.AnswerCallback(q.ID, "")
	}
}

// adDispositionAllowed 报告此人能否对这条判定记录做人工处置。
//
// 处置按钮会贴在群里，所以放宽到该群的 TG 管理员 —— 只让服务管理员
// 点得动的话，「群内展示」就只是让所有人围观，真正该判断的人动不了手。
//
// 两条边界必须守住：
//  1. 只放宽**处置**（确认/误判/删除/禁言/封禁），面板导航与配置仍然
//     只有服务管理员能进，否则任何群管都能改阈值、看全量流水、关功能。
//  2. 判据取流水里的 ChatID，**不是**回调消息所在的聊天。后者由发起人
//     所处的位置决定，拿它当判据等于「在自己能当管理员的群里点一下，
//     就能处置别的群的记录」。
func adDispositionAllowed(b *core.Bot, uid int64, row antiad.AdLogRow) bool {
	if b.Cfg.IsAdmin(uid) {
		return true
	}
	return antiad.IsChatAdmin(b, row.ChatID, uid)
}

// isAdDispositionCallback 报告该回调是否为反广告的人工处置。
// 形如 a:ad:<op>:<log_id>，op 限定在五个处置动作内。
func IsAdDispositionCallback(data string) bool {
	parts := strings.Split(data, ":")
	if len(parts) != 4 || parts[0] != "a" || parts[1] != "ad" {
		return false
	}
	switch parts[2] {
	case "ok", "fp", "del", "mute", "ban":
		return true
	}
	return false
}

// applyAdManualAction 执行管理员的人工处置。
func applyAdManualAction(b *core.Bot, q *tg.CallbackQuery, op string, row antiad.AdLogRow) {
	switch op {
	case "ok":
		// 只留痕，不动群内。样本保持在正例池里供形态总结取用。
		antiad.UpdateAdLog(b, row.ID, row.Action, appendReason(row.Reason, "管理员确认判定正确"))
		b.AnswerCallback(q.ID, "已确认")

	case "fp":
		// 误判是整个闭环最值钱的一环：解禁、回退命中数，
		// 并把样本转入反例池（action=undone 就是反例的判据）。
		//
		// 数据侧的两项修正（命中数回退、action 改判）与解禁 API
		// 调用是否成功无关——即使 TG 一侧解禁失败，样本判据和画像
		// 也不该继续背着这次误判，但必须如实告诉管理员解禁没做成，
		// 不能让他们以为用户已经能正常发言了。
		// 只在 bot 确实禁过言时才解禁。unmute 发的是「十项权限全 true」，
		// TG 语义上不是「撤销我那次禁言」而是「把权限设成全开」：对没被
		// 禁过的人等于提权到群默认之上，还会解掉人类管理员因别的原因
		// 施加的限制。dryrun: 前缀标记的动作从未真实发生，同样不算——
		// 那是演练模式唯一的漏点。
		unmuted, desc := true, ""
		switch row.Action {
		case "muted", "deleted_muted":
			unmuted, desc = antiad.Unmute(b, row.ChatID, row.UserID)
		case "banned", "deleted_banned":
			unmuted, desc = antiad.Unban(b, row.ChatID, row.UserID)
		}
		// 同样的内容别再被当成广告直接删（见 antiad 的内容哈希）。
		antiad.ForgetAdHash(b, row.Text)
		antiad.BumpAdHits(b, row.ChatID, row.UserID, -1)
		antiad.UpdateAdLog(b, row.ID, "undone", appendReason(row.Reason, "管理员标记误判"))
		if !unmuted {
			b.AnswerCallback(q.ID, "已标记误判，但解除限制失败: "+core.TruncateRunes(desc, 40))
			return
		}
		b.AnswerCallback(q.ID, "已标记误判并解除限制")

	case "del":
		if ok, desc := b.CallOK("deleteMessage", map[string]any{
			"chat_id": row.ChatID, "message_id": row.MessageID,
		}); !ok {
			b.AnswerCallback(q.ID, "删除失败: "+core.TruncateRunes(desc, 60))
			return
		}
		reason := appendReason(row.Reason, "管理员手工删除")
		// 告警按钮是静态的，点过 ban 之后同一条消息上的删除按钮仍可点。
		// undone/banned 是比「删除」更强的终态判断，不能被这次删除覆盖：
		// undone 是负采样的判据，banned 已经把人请出群了。
		// TG 侧照常删，只是不改 action 标签，实际动作记进 reason 留痕。
		if row.Action == "undone" || row.Action == "banned" {
			antiad.UpdateAdLog(b, row.ID, row.Action, reason)
			b.AnswerCallback(q.ID, "已删除（该记录保持「"+keepActionLabel(row.Action)+"」状态）")
			return
		}
		// 这条记录如果已经真实禁言过（muted / deleted_muted），删除
		// 不能把禁言信息抹掉——action 只应升级，不应回退成单纯的
		// "deleted"。dryrun: 前缀的历史值不算「已禁言」，演练期
		// 什么都没真的执行。
		action := "deleted"
		if row.Action == "muted" || row.Action == "deleted_muted" {
			action = "deleted_muted"
		}
		antiad.UpdateAdLog(b, row.ID, action, reason)
		b.AnswerCallback(q.ID, "已删除")

	case "mute":
		hours := b.Cache.Snap().BotSettingInt(row.BotID, "antiad_mute_hours", 24)
		if ok, desc := antiad.MuteSender(b, row.ChatID, row.UserID,
			time.Duration(hours)*time.Hour); !ok {
			b.AnswerCallback(q.ID, "禁言失败: "+core.TruncateRunes(desc, 60))
			return
		}
		reason := appendReason(row.Reason, fmt.Sprintf("管理员手工禁言 %d 小时", hours))
		// 同 del 分支：undone/banned 不能被这次禁言覆盖。
		if row.Action == "undone" || row.Action == "banned" {
			antiad.UpdateAdLog(b, row.ID, row.Action, reason)
			b.AnswerCallback(q.ID, "已禁言（该记录保持「"+keepActionLabel(row.Action)+"」状态）")
			return
		}
		// 消息是否已经真实删除过：用 HasPrefix 而不是 Contains——
		// "dryrun:deleted_muted" 表示演练期本应删除但实际没执行，
		// 消息其实还在，HasPrefix 能正确把它排除在外，Contains 会
		// 把它误判成已删。
		action := "muted"
		if strings.HasPrefix(row.Action, "deleted") {
			action = "deleted_muted"
		}
		antiad.UpdateAdLog(b, row.ID, action, reason)
		b.AnswerCallback(q.ID, "已禁言")

	case "ban":
		// banChatMember 是把人请出群，与禁言是两回事。频道身份走 banChatSenderChat。
		if ok, desc := antiad.BanSender(b, row.ChatID, row.UserID); !ok {
			b.AnswerCallback(q.ID, "封禁失败: "+core.TruncateRunes(desc, 60))
			return
		}
		antiad.UpdateAdLog(b, row.ID, "banned", appendReason(row.Reason, "管理员封禁出群"))
		b.AnswerCallback(q.ID, "已封禁出群")
	}
}

// keepActionLabel 把「本次操作保持不变」的终态 action 翻成人话，
// 用于回执提示，让管理员知道 action 标签本身没被这次操作动过。
//
// 调用点只会传 "undone" 或 "banned"（见 applyAdManualAction 里
// del/mute 分支的守卫），但函数本身不应该假设这一点——对非 "banned"
// 的任何输入都静默兜底成「已标记误判」的话，一旦有新的终态值传进来，
// 会被悄悄冒充成误判，无声无息。
// 只保留 "banned" 这一个确有必要的专属措辞（比 actionLabel 的通用
// 「已封禁」更贴合"该记录保持 XX 状态"的回执语境），
// 其余一律委托给 actionLabel 这张唯一的词汇表。
func keepActionLabel(action string) string {
	if action == "banned" {
		return "已封禁出群"
	}
	return actionLabel(action)
}
