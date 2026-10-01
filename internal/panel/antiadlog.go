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

// chatHealthTTL 是群权限自检结果的缓存时长。
//
// 这一步要发 getChatMember，而它跑在 bot 的串行更新路径上（面板回调与群
// 消息共用同一个 worker）：TG 慢时最坏 40 秒收不到新消息。两分钟对「bot
// 是不是管理员」这种低频变化足够实时——权限刚变完重开一次面板就是最新值。
const chatHealthTTL = 2 * time.Minute

type chatHealthEntry struct {
	text   string
	expire time.Time
}

// adChatHealth 检查 bot 在该群的权限，返回一句人话（结果缓存两分钟）。
func adChatHealth(b *core.Bot, chatID int64) string {
	key := fmt.Sprintf("%d:%d", b.BotID(), chatID)
	if v, ok := b.ChatHealthCache.Load(key); ok {
		if e, ok := v.(chatHealthEntry); ok && time.Now().Before(e.expire) {
			return e.text
		}
	}
	text := queryChatHealth(b, chatID)
	b.ChatHealthCache.Store(key, chatHealthEntry{
		text: text, expire: time.Now().Add(chatHealthTTL)})
	return text
}

// queryChatHealth 向 TG 查一次群权限。
func queryChatHealth(b *core.Bot, chatID int64) string {
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

// GCChatHealthCache 清理过期的自检结果，防止 map 无限增长。
func GCChatHealthCache(sh *core.Shared) {
	now := time.Now()
	sh.ChatHealthCache.Range(func(k, v any) bool {
		if e, ok := v.(chatHealthEntry); ok && now.After(e.expire) {
			sh.ChatHealthCache.Delete(k)
		}
		return true
	})
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
	fix := snap.Setting("antiad_digest_fix")

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
	// 修正文本是写给总结模型的口径说明：摘要每次重新生成都会附上它，
	// 用来纠正「总结总把某类正常消息写成广告」「该抓的形态没抓到」这类
	// 反复出现的问题，不必每轮手工改摘要。
	if fix != "" {
		fmt.Fprintf(&sb, "\n<b>修正文本</b>（每轮总结都附给总结模型）：\n<code>%s</code>\n",
			html.EscapeString(fix))
	} else {
		sb.WriteString("\n<b>修正文本</b>：未设置。\n")
	}

	b.Edit(chatID, msgID, sb.String(), tg.InlineKB(
		[][2]string{{"✏️ 手工编辑摘要", "a:ad:dg:e"}, {"🩹 修正文本", "a:ad:dg:f"}},
		[][2]string{{"🔄 立即重新总结", "a:ad:dg:r"}},
		[][2]string{{"🗑 清空摘要", "a:ad:dg:c"}, {"◀️ 返回", "a:ad"}},
	))
}

const adLogPageSize = 10

// showAntiAdLog 渲染某个 bot 的拦截记录。all=true 时连 clean 一起列 ——
// 演练期管理员真正要看的是「有没有把正常消息判成广告」。
//
// 按 bot_id 过滤是权限边界的一部分：次级管理员只能看自己 bot 判的东西，
// 而流水里有群消息原文。
func showAntiAdLog(b *core.Bot, chatID, msgID, botID int64, page int, all bool) {
	page = clampPageInt(page)
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

	loc := b.Cache.Snap().Location()
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
		// 时刻按 settings.tz_name 呈现：管理员看到的时间必须是本地时间，
		// UTC 会让人对不上号。
		when := time.Unix(at, 0).In(loc).Format("01-02 15:04")
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

// maxPanelPage 是分页上限。页码没有上限时 (page-1)*size 会算出巨大的
// OFFSET（极端值还会溢出成负数），一次查询就把库扫一遍。
const maxPanelPage = 10000

func clampPageInt(p int) int {
	if p < 1 {
		return 1
	}
	if p > maxPanelPage {
		return maxPanelPage
	}
	return p
}

func clampPage(p int64) int64 {
	if p < 1 {
		return 1
	}
	if p > maxPanelPage {
		return maxPanelPage
	}
	return p
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
		case "f":
			b.AnswerCallback(q.ID, "")
			b.AskInput(chatID, q.From.ID, "ad_dg_f", "",
				"请输入<b>修正文本</b>：写给总结模型的口径说明，每轮重新总结都会附上它。"+
					"填 <code>-</code> 可清空。\n\n"+
					"例：\n<code>技术讨论里出现的 GitHub、npm 链接不算广告；"+
					"兼职招募一律按诈骗归类。</code>\n\n"+
					"它不改摘要正文本身；想让新口径立刻生效，"+
					"保存后点「立即重新总结」。")
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

	case "rec": // a:ad:rec:<log_id>[:<uid>:<page>] —— 记录卡片
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
		if uid, page := navFromCallback(q.Data); uid != 0 {
			// 从 /user 列表点进来的：原地重绘并保留返回按钮。
			kb = kbWithNav(kb, fmt.Sprintf(":%d:%d", uid, page))
			b.Edit(chatID, msgID, text,
				tg.KBAppend(kb, [][2]string{{"◀️ 返回列表",
					userLogsCB(uid, page, false)}}))
			return
		}
		b.Send(chatID, text, kb)

	case "ul": // a:ad:ul:<uid>:<page>[:all] —— 某人的资料卡 + 判定记录
		if len(parts) < 5 {
			b.AnswerCallback(q.ID, "参数缺失")
			return
		}
		uid, err1 := strconv.ParseInt(parts[3], 10, 64)
		page, err2 := strconv.ParseInt(parts[4], 10, 64)
		if err1 != nil || err2 != nil {
			b.AnswerCallback(q.ID, "参数无效")
			return
		}
		all := len(parts) > 5 && parts[5] == "all"
		b.AnswerCallback(q.ID, "")
		showUserLogs(b, chatID, msgID, q.From.ID, uid, int(page), all)

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
		// 处置后原地重绘：从 /user 列表进来的卡片要保留「返回列表」。
		redrawAdCard(b, q, id)

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
	if len(parts) < 4 || parts[0] != "a" || parts[1] != "ad" {
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
		case "muted", "deleted_muted", "gban_muted":
			// gban_muted 是联合封禁在发言路径上的禁言（不是名单本身）：
			// 名单另由下面的 AdminLiftGban 撤，但群里的禁言也得解掉。
			unmuted, desc = antiad.Unmute(b, row.ChatID, row.UserID)
		case "banned", "deleted_banned":
			unmuted, desc = antiad.Unban(b, row.ChatID, row.UserID)
		}
		// 同样的内容别再被当成广告直接删（见 antiad 的内容哈希）。
		antiad.ForgetAdHash(b, row.Text)
		antiad.BumpAdHits(b, row.ChatID, row.UserID, -1)
		// 这条判定作废了，由它派生的联合封禁也得一起撤：名单是跨所有接入群
		// 执行的，留着等于让一条被判错的记录继续全平台封人。
		//   - 服务管理员：撤全局组 + 自己的专属组；主管理员额外撤**记录所属
		//     bot 归属人**的专属组（否则误判撤了，人在那个归属人的群里还封着）
		//   - 群管理员（只是 TG 群管，不是服务管理员）：不动名单，只解本群
		var lifted []string
		if b.IsStaff(q.From.ID) {
			if s := antiad.AdminLiftGban(b.Shared, q.From.ID, row.UserID); s != "" {
				lifted = append(lifted, s)
			}
			if b.IsMain(q.From.ID) {
				if rec := b.Cache.Snap().Bots[row.BotID]; rec != nil && rec.OwnerID != q.From.ID {
					if s := antiad.AdminLiftGban(b.Shared, rec.OwnerID, row.UserID); s != "" {
						lifted = append(lifted, s)
					}
				}
			}
		}
		reason := "管理员标记误判"
		if len(lifted) > 0 {
			reason += "，已撤除" + strings.Join(lifted, "、")
		}
		antiad.UpdateAdLog(b, row.ID, "undone", appendReason(row.Reason, reason))
		if !unmuted {
			b.AnswerCallback(q.ID, "已标记误判，但解除限制失败: "+core.TruncateRunes(desc, 40))
			return
		}
		msg := "已标记误判并解除限制"
		if len(lifted) > 0 {
			msg += "；已撤除" + strings.Join(lifted, "、")
		}
		b.AnswerCallback(q.ID, msg)

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
		minutes := b.Cache.Snap().BotSettingInt(row.BotID, "antiad_mute_minutes", 1440)
		if ok, desc := antiad.MuteSender(b, row.ChatID, row.UserID,
			time.Duration(minutes)*time.Minute); !ok {
			b.AnswerCallback(q.ID, "禁言失败: "+core.TruncateRunes(desc, 60))
			return
		}
		reason := appendReason(row.Reason, "管理员手工"+antiad.MuteLabel(minutes))
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

// ---- /log 与 /user：记录卡片与用户记录列表 ----

// ShowLogCard 渲染单条记录卡片并发送（/log <id>）。
func ShowLogCard(b *core.Bot, chatID, uid, id int64) {
	row, ok := antiad.LoadAdLog(b.Store, id)
	if !ok || !b.CanManageBot(uid, row.BotID) {
		// 不区分「不存在」与「无权」：后者等于确认这个编号存在。
		b.Send(chatID, "记录不存在或已过保留期。", nil)
		return
	}
	text, kb := antiad.RenderAdRecord(b, row)
	b.Send(chatID, text, kb)
}

// ShowUserLogs 渲染某人的资料卡与判定记录（/user <uid>）。
func ShowUserLogs(b *core.Bot, chatID, uid, target int64) {
	showUserLogs(b, chatID, 0, uid, target, 1, false)
}

// userLogsCB 拼「某人的资料卡」回调：filter 为真时连未处置的一起列。
func userLogsCB(uid, page int64, all bool) string {
	s := fmt.Sprintf("a:ad:ul:%d:%d", uid, page)
	if all {
		s += ":all"
	}
	return s
}

const userLogsPerPage = 10

// showUserLogs 渲染资料卡与判定记录列表并原地编辑（msgID 为 0 时新发一条）。
//
// 默认只列**被处置过**的记录（见 antiad.processedCond）：管理员查一个人时
// 先要看他被罚过什么，而不是他所有被判过正常的话。点切换可以看全部。
func showUserLogs(b *core.Bot, chatID, msgID, uid, target int64, page int, all bool) {
	page = clampPageInt(page)
	where, args := managedBotsClause(b, uid)
	if where == " AND 0" {
		b.EditOrSend(chatID, msgID, "你名下没有机器人。", nil)
		return
	}
	snap := b.Cache.Snap()

	// 资料卡：昵称/用户名/简介要发一次 getChat（带缓存）。
	name, username, bio := antiad.UserProfile(b, target)
	d := antiad.UserDossier{UID: target, Name: name, Username: username, Bio: bio}
	if rec := snap.Bots[b.BotID()]; rec != nil {
		d = antiad.LoadUserDossier(b.Shared, b.BotID(), target)
		d.Name, d.Username, d.Bio = name, username, bio
	}

	countQ := `SELECT COUNT(*) FROM antiad_log WHERE user_id=?` + where
	if !all {
		countQ += ` AND ` + antiad.ProcessedCond
	}
	var total int64
	b.Store.Read.QueryRow(countQ, append([]any{target}, args...)...).Scan(&total)

	listQ := `SELECT id,chat_id,verdict,confidence,ad_kind,action,created_at
		FROM antiad_log WHERE user_id=?` + where
	if !all {
		listQ += ` AND ` + antiad.ProcessedCond
	}
	listQ += ` ORDER BY id DESC LIMIT ? OFFSET ?`
	rows, err := b.Store.Read.Query(listQ,
		append(append([]any{target}, args...), userLogsPerPage, (page-1)*userLogsPerPage)...)
	if err != nil {
		b.Send(chatID, "查询失败。", nil)
		return
	}
	type item struct {
		id      int64
		chat    int64
		verdict string
		conf    float64
		kind    string
		action  string
		at      int64
	}
	var list []item
	for rows.Next() {
		var it item
		if rows.Scan(&it.id, &it.chat, &it.verdict, &it.conf, &it.kind,
			&it.action, &it.at) == nil {
			list = append(list, it)
		}
	}
	rows.Close()

	loc := snap.Location()
	var sb strings.Builder
	sb.WriteString(antiad.UserDossierText(b, d, loc))
	sb.WriteString("\n")
	if all {
		fmt.Fprintf(&sb, "📋 <b>判定记录（全部 %d 条）</b>\n\n", total)
	} else {
		fmt.Fprintf(&sb, "📋 <b>处置记录（%d 条）</b>\n"+
			"<i>默认只列被处置过的；点下方按钮可看全部 %d 条。</i>\n\n",
			total, d.Total)
	}
	var kb [][][2]string
	for _, it := range list {
		fmt.Fprintf(&sb, "• <code>#%d</code> %s · %s · %s\n",
			it.id, time.Unix(it.at, 0).In(loc).Format("01-02 15:04"),
			html.EscapeString(map[string]string{
				"ad": "广告", "clean": "正常", "error": "失败", "skipped": "未送检"}[it.verdict]),
			html.EscapeString(actionLabel(it.action)))
		kb = append(kb, [][2]string{{fmt.Sprintf("📋 #%d", it.id),
			fmt.Sprintf("a:ad:rec:%d:%d:%d", it.id, target, page)}})
	}
	if len(list) == 0 {
		sb.WriteString("（没有记录）")
	}
	pages := int((total + userLogsPerPage - 1) / userLogsPerPage)
	if pages > 1 {
		nav := [][2]string{}
		if page > 1 {
			nav = append(nav, [2]string{"◀️", userLogsCB(target, int64(page-1), all)})
		}
		nav = append(nav, [2]string{fmt.Sprintf("%d/%d", page, pages), "a:noop"})
		if page < pages {
			nav = append(nav, [2]string{"▶️", userLogsCB(target, int64(page+1), all)})
		}
		kb = append(kb, nav)
	}
	if all {
		kb = append(kb, [][2]string{{"🙈 只看被处置过的",
			userLogsCB(target, 1, false)}})
	} else {
		kb = append(kb, [][2]string{{"👀 显示全部判定记录",
			userLogsCB(target, 1, true)}})
	}
	b.EditOrSend(chatID, msgID, sb.String(), tg.InlineKB(kb...))
}

// managedBotsClause 返回按权限过滤 antiad_log 的 SQL 片段与参数。
// 主管理员看全部；次级管理员只看自己名下 bot 的记录。
// managedBotsClause 返回「只看此人名下的 bot」的 SQL 片段与参数。
func managedBotsClause(b *core.Bot, uid int64) (string, []any) {
	return botsClause(b.Shared, uid, b.IsMain(uid))
}

// botsClause 是上面两个入口的共同实现：主管理员不设限；名下没有 bot 时
// 用 AND 0 让查询必然为空（而不是不过滤、把别人的记录漏出去）。
func botsClause(sh *core.Shared, uid int64, main bool) (string, []any) {
	if main {
		return "", nil
	}
	owned := sh.Cache.Snap().BotsOwnedBy(uid, false)
	if len(owned) == 0 {
		return " AND 0", nil
	}
	holders := make([]string, 0, len(owned))
	args := make([]any, 0, len(owned))
	for _, r := range owned {
		holders = append(holders, "?")
		args = append(args, r.BotID)
	}
	return " AND bot_id IN (" + strings.Join(holders, ",") + ")", args
}

// navFromCallback 从 a:ad:<op>:<id>:<uid>:<page> 里取出列表导航信息。
func navFromCallback(data string) (uid, page int64) {
	parts := strings.Split(data, ":")
	if len(parts) < 6 {
		return 0, 0
	}
	uid, _ = strconv.ParseInt(parts[4], 10, 64)
	page, _ = strconv.ParseInt(parts[5], 10, 64)
	return uid, clampPage(page)
}

// kbWithNav 给卡片上的回调按钮追加 ":<uid>:<page>"，让处置后的重绘
// 仍能拼出「返回列表」。URL 按钮不带 callback_data，原样保留。
func kbWithNav(kb map[string]any, nav string) map[string]any {
	rows, _ := kb["inline_keyboard"].([][]map[string]string)
	for i := range rows {
		for j := range rows[i] {
			if cd := rows[i][j]["callback_data"]; cd != "" {
				rows[i][j]["callback_data"] = cd + nav
			}
		}
	}
	return kb
}

// redrawAdCard 用最新流水重绘记录卡片（处置后调用）。
func redrawAdCard(b *core.Bot, q *tg.CallbackQuery, id int64) {
	row, ok := antiad.LoadAdLog(b.Store, id)
	if !ok {
		return
	}
	text, kb := antiad.RenderAdRecord(b, row)
	if uid, page := navFromCallback(q.Data); uid != 0 {
		kb = kbWithNav(kb, fmt.Sprintf(":%d:%d", uid, page))
		kb = tg.KBAppend(kb, [][2]string{{"◀️ 返回列表",
			fmt.Sprintf("a:ad:ul:%d:%d", uid, page)}})
	}
	b.Edit(q.Message.Chat.ID, q.Message.MessageID, text, kb)
}

// cmdArg 从「/cmd 参数」里取第一个参数，容忍 /cmd@botname 形态。
func cmdArg(text, cmd string) string {
	rest := strings.TrimSpace(strings.TrimPrefix(text, cmd))
	fields := strings.Fields(rest)
	if len(fields) > 0 && strings.HasPrefix(fields[0], "@") {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// HandleLogCommand 处理管理员私聊的 /log <记录号>。
func HandleLogCommand(b *core.Bot, m *tg.Message, text string) {
	id, err := strconv.ParseInt(cmdArg(text, "/log"), 10, 64)
	if err != nil || id <= 0 {
		b.Send(m.Chat.ID, "用法：<code>/log &lt;记录号&gt;</code>\n\n"+
			"记录号见群内告警或私聊汇总里的「#编号」。", nil)
		return
	}
	ShowLogCard(b, m.Chat.ID, m.From.ID, id)
}

// HandleUserCommand 处理管理员私聊的 /user <user_id>。
func HandleUserCommand(b *core.Bot, m *tg.Message, text string) {
	uid, err := strconv.ParseInt(cmdArg(text, "/user"), 10, 64)
	if err != nil || uid == 0 {
		b.Send(m.Chat.ID, "用法：<code>/user &lt;user_id&gt;</code>", nil)
		return
	}
	ShowUserLogs(b, m.Chat.ID, m.From.ID, uid)
}
