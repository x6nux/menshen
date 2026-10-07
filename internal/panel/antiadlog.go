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

// adTodayStats 汇总当日送检数、命中数、误判数与开销。
// 开销必须可见：全量送检的成本是该功能的主要风险。
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
// 消息共用同一个 worker）：TG 慢时最坏 40 秒收不到新消息。两分钟对 bot
// 是否为管理员这类低频变化足够实时——权限刚变完重开一次面板即最新值。
const chatHealthTTL = 2 * time.Minute

// panelCaches 是面板挂在 Shared 上的内存缓存（见 core.Ext）。
type panelCaches struct {
	// chatHealth 缓存群权限自检结果，键 "botID:chatID"。面板每次渲染群详情
	// 都查一次 getChatMember，而它跑在 bot 的串行更新路径上（TG 慢时最坏 40 秒）。
	chatHealth core.TTLMap[string, string]
}

type panelCachesKey struct{}

func panelCachesOf(sh *core.Shared) *panelCaches {
	return core.Ext(sh, panelCachesKey{}, func() *panelCaches { return &panelCaches{} })
}

// adChatHealth 检查 bot 在该群的权限，返回可读结论（结果缓存两分钟）。
func adChatHealth(b *core.Bot, chatID int64) string {
	key := fmt.Sprintf("%d:%d", b.BotID(), chatID)
	cache := &panelCachesOf(b.Shared).chatHealth
	if text, ok := cache.Get(key); ok {
		return text
	}
	text := queryChatHealth(b, chatID)
	cache.Set(key, text, chatHealthTTL)
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
func GCChatHealthCache(sh *core.Shared) { panelCachesOf(sh).chatHealth.GC(time.Now()) }

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
	// 修正文本是写给总结模型的口径说明：每次重新生成摘要都会附上它，
	// 用来纠正总结把某类正常消息写成广告、该抓的形态没抓到等问题，
	// 不必每轮手工改摘要。
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

// showAntiAdLog 渲染某个 bot 的拦截记录。all=true 时连 clean 一起列——
// 演练期管理员真正要看的是有没有把正常消息误判成广告。
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
		// 时刻按 settings.tz_name 呈现：管理员需看到本地时间，UTC 不便于对照。
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
	// 不查 rows.Err() 会把只扫到一半当成扫完了：拦截记录是合规/复盘的审计面，
	// 静默截断会让 n < adLogPageSize，连下一页按钮也一起消失，
	// 管理员会误判记录已全部列出。
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
// 这里只有两类：形态摘要（全局一份，仅主管理员可动）与告警消息上的人工
// 处置。面板导航、阈值、生效群属于某一个 bot，归 a:mb 那棵树处理。
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
			if err := setDigest(b.Shared, q.From.ID, false, ""); err != nil {
				b.AnswerCallback(q.ID, opText(err, "清空失败"))
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

	case "gbl": // a:ad:gbl:<target>:<page>[:all] —— 解除联合封禁
		if len(parts) < 5 {
			b.AnswerCallback(q.ID, "参数缺失")
			return
		}
		target, err := strconv.ParseInt(parts[3], 10, 64)
		page, _ := strconv.ParseInt(parts[4], 10, 64)
		if err != nil || target == 0 {
			b.AnswerCallback(q.ID, "参数无效")
			return
		}
		if !b.CanManageBot(q.From.ID, b.BotID()) {
			b.AnswerCallback(q.ID, "无权操作")
			return
		}
		all := len(parts) > 5 && parts[5] == "all"
		note := "该用户不在你能解除的名单里"
		if scope := antiad.AdminLiftGban(b.Shared, q.From.ID, target); scope != "" {
			note = "已解除：" + scope
		}
		b.AnswerCallback(q.ID, note)
		showUserLogs(b, chatID, msgID, q.From.ID, target, int(page), all)

	case "lift": // a:ad:lift:<chat>:<target>:<page>[:all] —— 解除某群的限制
		if len(parts) < 6 {
			b.AnswerCallback(q.ID, "参数缺失")
			return
		}
		chat, err1 := strconv.ParseInt(parts[3], 10, 64)
		target, err2 := strconv.ParseInt(parts[4], 10, 64)
		page, _ := strconv.ParseInt(parts[5], 10, 64)
		if err1 != nil || err2 != nil || target == 0 {
			b.AnswerCallback(q.ID, "参数无效")
			return
		}
		// 优先用在该群里工作的 bot；群已不属任何 bot 时（仍有生效的限制记录）
		// 退回面板所在的 bot——TG 侧多半解不了，但记录仍要清掉。
		inst, managed := chatBot(b, q.From.ID, chat)
		if !managed {
			if !b.CanManageBot(q.From.ID, b.BotID()) {
				b.AnswerCallback(q.ID, "无权操作")
				return
			}
			inst = b
		}
		all := len(parts) > 6 && parts[6] == "all"
		note := "已解除"
		if ok, desc := antiad.ReleaseUserInChat(inst, chat, target, q.From.ID); !ok {
			note = desc
		}
		b.AnswerCallback(q.ID, note)
		showUserLogs(b, chatID, msgID, q.From.ID, target, int(page), all)

	case "ok", "fp", "del", "mute", "ban", "rel":
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
		inst := recordBot(b, row.BotID)
		if !adDispositionAllowed(inst, q.From.ID, row) {
			// 静默：与 a: 前缀的既有做法一致，不向无关人员确认按钮存在。
			b.AnswerCallback(q.ID, "")
			return
		}
		applyAdManualAction(b, inst, q, parts[2], row)
		// 处置后原地重绘：从 /user 列表进来的卡片要保留返回列表按钮。
		redrawAdCard(b, q, id)

	default:
		b.AnswerCallback(q.ID, "")
	}
}

// recordBot 返回处置一条记录该用的 bot：记录所属的工作 bot。
//
// 面板所在的 bot 常是不入群的主 bot（/log、/user、深链都可能落在它那里），
// 用它发 restrictChatMember 必然失败，按它的 bot_id 标记流水、撤内容哈希
// 也都对不上。回调应答仍由收到回调的 b 发；实例不在运行时退回 b。
func recordBot(b *core.Bot, botID int64) *core.Bot {
	if b.Reg != nil {
		if inst, ok := b.Reg.LookupID(botID); ok {
			return inst
		}
	}
	return b
}

// chatBot 找在 chatID 里工作、且 uid 有权管理的那个 bot 实例（用户记录页的
// 解除操作只带群号，没有记录可依托）。找不到返回 false。
func chatBot(b *core.Bot, uid, chatID int64) (*core.Bot, bool) {
	snap := b.Cache.Snap()
	for botID := range snap.Bots {
		if _, ok := snap.ChatConf(botID, chatID); !ok || !b.CanManageBot(uid, botID) {
			continue
		}
		if inst := recordBot(b, botID); inst.BotID() == botID {
			return inst, true
		}
	}
	return nil, false
}

// adDispositionAllowed 报告此人能否对这条判定记录做人工处置。
//
// 处置按钮会贴在群里，故放宽到该群的 TG 管理员：若只让服务管理员可点，
// 群内展示就只是让所有人围观，真正该判断的人动不了手。
//
// 两条边界：
//  1. 只放宽处置动作（确认/误判/删除/禁言/封禁），面板导航与配置仍只
//     服务管理员可进，否则任何群管都能改阈值、看全量流水、关功能。
//  2. 判据取流水里的 ChatID，而非回调消息所在的聊天。后者由发起人所在
//     位置决定，拿它当判据等于在自己能管理的群里点一下就能处置别群的记录。
func adDispositionAllowed(b *core.Bot, uid int64, row antiad.AdLogRow) bool {
	// 服务管理员：主管理员全通，次管限自己名下的 bot。记录卡片本来就是
	// 私聊发给 bot 归属人的，只有主管理员点得动的话，次管会看到卡片上
	// 每个按钮都被静默拒绝。
	if b.IsStaff(uid) && b.CanManageBot(uid, row.BotID) {
		return true
	}
	return antiad.IsChatAdmin(b, row.ChatID, uid)
}

// IsAdDispositionCallback 报告该回调是否为反广告的人工处置。
// 形如 a:ad:<op>:<log_id>，op 限定在处置动作集合内。
func IsAdDispositionCallback(data string) bool {
	parts := strings.Split(data, ":")
	if len(parts) < 4 || parts[0] != "a" || parts[1] != "ad" {
		return false
	}
	switch parts[2] {
	case "ok", "fp", "del", "mute", "ban", "rel":
		return true
	}
	return false
}

// applyAdManualAction 执行管理员的人工处置。
func applyAdManualAction(b, inst *core.Bot, q *tg.CallbackQuery, op string, row antiad.AdLogRow) {
	// 已判误判的记录不能再被确认正确/禁言/封禁重新处置：这几条会再次处罚，
	// 而误判是负样本、是最有价值的训练信号，残留按钮的一次误点会把放行的
	// 人重新罚一遍。del/rel 只删消息或解除限制，不在此列。
	if row.Action == "undone" {
		switch op {
		case "ok", "mute", "ban":
			b.AnswerCallback(q.ID, "该记录已标记误判，不再处置")
			return
		}
	}
	switch op {
	case "ok":
		// 管理员确认判定正确：绕过老成员免禁言——人已看过原文并背书，不应
		// 再按资历只删不罚。按本群处罚方式补一次正式处置（记录里没删过的
		// 消息连带补删），动作标签与理由同步更新；样本留在正例池供形态总结
		// 取用，之后按误判仍可走既有的解禁路径。
		snap := inst.Cache.Snap()
		conf, _ := snap.ChatConf(row.BotID, row.ChatID)
		ban := snap.BanMode(conf)
		mins := snap.BotSettingInt(row.BotID, "antiad_mute_minutes", 1440)

		// 仅告警档的消息还在群里：一起补删，处置要与判定一致。
		deleted := strings.HasPrefix(row.Action, "deleted") ||
			strings.HasPrefix(row.Action, "dryrun:deleted")
		if !deleted && row.MessageID != 0 {
			if ok2, _ := antiad.DeleteMessage(inst, row.ChatID, row.MessageID); ok2 {
				deleted = true
			}
		}

		done, fail, skip := "", "", ""
		if ban {
			if ok2, desc := antiad.BanSender(inst, row.ChatID, row.UserID); !ok2 {
				fail = "封禁失败: " + core.TruncateRunes(desc, 60)
			} else {
				done = "封禁出群"
			}
		} else if ok2, desc := antiad.MuteSender(inst, row.ChatID, row.UserID,
			time.Duration(mins)*time.Minute); !ok2 {
			// 人已经退群/被踢/已被禁言时禁言必然失败，这不算故障。
			if why := antiad.MuteMoot(inst, row.ChatID, row.UserID, desc); why != "" {
				skip = why
			} else {
				fail = "禁言失败: " + core.TruncateRunes(desc, 60)
			}
		} else {
			done = antiad.MuteLabel(mins)
		}

		// 动作标签按实际结果写：误判/解封路径靠它决定怎么解。
		action := row.Action
		switch {
		case fail == "" && skip == "" && ban && deleted:
			action = "deleted_banned"
		case fail == "" && skip == "" && ban:
			action = "banned"
		case fail == "" && skip == "" && deleted:
			action = "deleted_muted"
		case fail == "" && skip == "":
			action = "muted"
		case deleted:
			action = "deleted"
		}
		note := "管理员确认判定正确（不看资历）"
		if done != "" {
			note += "：" + done
		}
		if skip != "" {
			note += "；" + skip + "，未追加禁言"
		}
		if fail != "" {
			note += "；" + fail
		}
		antiad.UpdateAdLog(inst, row.ID, action, appendReason(row.Reason, note))
		if fail != "" {
			b.AnswerCallback(q.ID, fail)
			return
		}
		if skip != "" {
			b.AnswerCallback(q.ID, "已确认判定；"+skip+"，无需禁言")
			return
		}
		b.AnswerCallback(q.ID, "已确认并"+done)

	case "fp":
		// 误判是整个闭环最关键的环节：改判、解禁、撤派生的联合封禁（见
		// antiad.UndoVerdict）。数据侧改判与 TG 侧解禁是否成功无关，但解禁
		// 失败必须如实告知管理员，不能让他们以为用户已能正常发言。
		lifted, ok, desc := antiad.UndoVerdict(inst, row, q.From.ID)
		if !ok {
			b.AnswerCallback(q.ID, "已标记误判，但解除限制失败: "+core.TruncateRunes(desc, 40))
			return
		}
		msg := "已标记误判并解除限制"
		if lifted != "" {
			msg += "；已撤除" + lifted
		}
		b.AnswerCallback(q.ID, msg)

	case "del":
		if ok, desc := antiad.DeleteMessage(inst, row.ChatID, row.MessageID); !ok {
			b.AnswerCallback(q.ID, "删除失败: "+core.TruncateRunes(desc, 60))
			return
		}
		reason := appendReason(row.Reason, "管理员手工删除")
		// 告警按钮是静态的，点过 ban 之后同一条消息上的删除按钮仍可点。
		// undone/banned 是比删除更强的终态判定，不能被这次删除覆盖：
		// undone 是负采样的判据，banned 已经把人请出群了。
		// TG 侧照常删，只是不改 action 标签，实际动作记进 reason 留痕。
		if row.Action == "undone" || row.Action == "banned" {
			antiad.UpdateAdLog(inst, row.ID, row.Action, reason)
			b.AnswerCallback(q.ID, "已删除（该记录保持「"+keepActionLabel(row.Action)+"」状态）")
			return
		}
		// 这条记录若已真实禁言过（muted / deleted_muted），删除不能抹掉
		// 禁言信息：action 只应升级，不应回退成单纯的 "deleted"。
		// dryrun: 前缀的值不算已禁言，演练期没有真正执行。
		action := "deleted"
		if row.Action == "muted" || row.Action == "deleted_muted" {
			action = "deleted_muted"
		}
		antiad.UpdateAdLog(inst, row.ID, action, reason)
		b.AnswerCallback(q.ID, "已删除")

	case "mute":
		minutes := inst.Cache.Snap().BotSettingInt(row.BotID, "antiad_mute_minutes", 1440)
		if ok, desc := antiad.MuteSender(inst, row.ChatID, row.UserID,
			time.Duration(minutes)*time.Minute); !ok {
			// 同确认分支：人已出群或已被禁言时不再报假失败。
			if why := antiad.MuteMoot(inst, row.ChatID, row.UserID, desc); why != "" {
				b.AnswerCallback(q.ID, why+"，无需禁言")
				return
			}
			b.AnswerCallback(q.ID, "禁言失败: "+core.TruncateRunes(desc, 60))
			return
		}
		reason := appendReason(row.Reason, "管理员手工"+antiad.MuteLabel(minutes))
		// 同 del 分支：undone/banned 不能被这次禁言覆盖。
		if row.Action == "undone" || row.Action == "banned" {
			antiad.UpdateAdLog(inst, row.ID, row.Action, reason)
			b.AnswerCallback(q.ID, "已禁言（该记录保持「"+keepActionLabel(row.Action)+"」状态）")
			return
		}
		// 消息是否已真实删除过：用 HasPrefix 而不是 Contains——
		// "dryrun:deleted_muted" 表示演练期本应删除但实际未执行，
		// 消息实际仍在，HasPrefix 能正确把它排除在外，Contains 会
		// 把它误判成已删。
		action := "muted"
		if strings.HasPrefix(row.Action, "deleted") {
			action = "deleted_muted"
		}
		antiad.UpdateAdLog(inst, row.ID, action, reason)
		b.AnswerCallback(q.ID, "已禁言")

	case "rel":
		// 解封（判定维持）：撤掉仍生效的限制、清掉记录，但不动判定本身——
		// 与误判操作的区别就在这里（样本池、命中数、内容哈希都不动）。
		did := antiad.ReleaseUser(inst, row, q.From.ID)
		b.AnswerCallback(q.ID, "已"+did+"（判定维持不变）")

	case "ban":
		// banChatMember 是把人请出群，与禁言是两回事。频道身份走 banChatSenderChat。
		if ok, desc := antiad.BanSender(inst, row.ChatID, row.UserID); !ok {
			b.AnswerCallback(q.ID, "封禁失败: "+core.TruncateRunes(desc, 60))
			return
		}
		antiad.UpdateAdLog(inst, row.ID, "banned", appendReason(row.Reason, "管理员封禁出群"))
		b.AnswerCallback(q.ID, "已封禁出群")
	}
}

// keepActionLabel 把本次操作保持不变的终态 action 翻译成回执文案，
// 让管理员知道 action 标签本身未被这次操作改动。
//
// 调用点只会传 "undone" 或 "banned"（见 applyAdManualAction 里 del/mute
// 分支的守卫），但函数本身不应假设这一点：对非 "banned" 的输入都兜底成
// 误判文案的话，一旦有新的终态值传入会被冒充成误判。
// 只保留 "banned" 这一个专属措辞（比 actionLabel 的通用文案更贴合
// "该记录保持 XX 状态" 的回执语境），其余委托给 actionLabel。
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
		// 不区分不存在与无权：后者等于确认这个编号存在。
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

// userLogsCB 拼某人的资料卡回调：filter 为真时连未处置的一起列。
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
// 默认只列被处置过的记录（见 antiad.ProcessedCond）：管理员查一个人时先看
// 他被罚过什么，而不是所有被判正常的话。点切换可看全部。
func showUserLogs(b *core.Bot, chatID, msgID, uid, target int64, page int, all bool) {
	page = clampPageInt(page)
	where, args := managedBotsClause(b, uid)
	if where == " AND 0" {
		b.EditOrSend(chatID, msgID, "你名下没有机器人。", nil)
		return
	}
	snap := b.Cache.Snap()

	// 资料卡：昵称/用户名/简介现查一次 getChat（带缓存），查不到回落到流水里
	// 留存的 user_name。面板常开在主 bot 上，而主 bot 不入群、查不到任何人，
	// 所以交给 ResolveUserProfile 去挑一个跟这人有共同会话的 bot。
	d := antiad.LoadUserDossier(b.Shared, b.BotID(), target)
	antiad.ResolveUserProfile(b.Shared, &d, b, b.BotID(), target)

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

	// 生效中的限制与联合封禁：管理员查人时最需要一眼看到此人还被什么拦着，
	// 并且要能当场解除——只展示不给按钮，还得去别处找入口。
	var actionRows [][][2]string
	if pens := antiad.ActivePenalties(b.Shared, b.BotID(), target); len(pens) > 0 {
		suffix := ""
		if all {
			suffix = ":all"
		}
		sb.WriteString("⚠️ <b>生效中的限制</b>\n")
		sb.WriteString("<i>「进群限制」包括按个人简介判出的资料限制与前置号" +
			"识别限制；解封时把限制记录一并清掉，否则复查任务会按记录再禁回去。</i>\n")
		var chatOrder []int64
		seen := map[int64]bool{}
		for _, p := range pens {
			if p.Type != "gban" && p.Type != "gban_own" && !seen[p.ChatID] {
				seen[p.ChatID] = true
				chatOrder = append(chatOrder, p.ChatID)
			}
		}
		for _, p := range pens {
			when := time.Unix(p.At, 0).In(loc).Format("01-02 15:04")
			switch p.Type {
			case "join_profile":
				fmt.Fprintf(&sb, "• 群 %s ｜ 进群限制（个人简介）｜ %s\n<i>%s</i>\n",
					chatTag(b, p.ChatID), when,
					html.EscapeString(core.TruncateRunes(p.Reason, 120)))
			case "prewarm":
				fmt.Fprintf(&sb, "• 群 %s ｜ 前置号识别限制 ｜ %s\n<i>%s</i>\n",
					chatTag(b, p.ChatID), when,
					html.EscapeString(core.TruncateRunes(p.Reason, 120)))
			case "message":
				fmt.Fprintf(&sb, "• 群 %s ｜ %s ｜ %s\n<i>%s</i>\n",
					chatTag(b, p.ChatID), html.EscapeString(actionLabel(p.Action)), when,
					html.EscapeString(core.TruncateRunes(p.Reason, 120)))
			}
		}
		var gbans []antiad.PenaltyInfo
		for _, p := range pens {
			if p.Type == "gban" || p.Type == "gban_own" {
				gbans = append(gbans, p)
			}
		}
		if len(gbans) > 0 {
			sb.WriteString("🚫 <b>联合封禁</b>\n")
			for _, p := range gbans {
				scope := "全局组"
				if p.Type == "gban_own" {
					scope = "专属组（管理员 " + strconv.FormatInt(p.Owner, 10) + " 的账本）"
				}
				fmt.Fprintf(&sb, "• %s ｜ %s\n<i>%s</i>\n", scope,
					time.Unix(p.At, 0).In(loc).Format("01-02 15:04"),
					html.EscapeString(core.TruncateRunes(p.Reason, 120)))
			}
			actionRows = append(actionRows, [][2]string{{"🚫 解除联合封禁",
				fmt.Sprintf("a:ad:gbl:%d:%d%s", target, page, suffix)}})
		}
		for _, chatID := range chatOrder {
			actionRows = append(actionRows, [][2]string{{"🔓 解除群 " +
				strconv.FormatInt(chatID, 10) + " 的限制",
				fmt.Sprintf("a:ad:lift:%d:%d:%d%s", chatID, target, page, suffix)}})
		}
		sb.WriteString("\n")
	}

	if all {
		fmt.Fprintf(&sb, "📋 <b>判定记录（全部 %d 条）</b>\n\n", total)
	} else {
		fmt.Fprintf(&sb, "📋 <b>处置记录（%d 条）</b>\n"+
			"<i>默认只列被处置过的；点下方按钮可看全部 %d 条。</i>\n\n",
			total, d.Total)
	}
	kb := actionRows
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

// chatTag 渲染群标签：本 bot 有群配置就带上标题，否则只有 id。
// 主管理员看的是全平台记录，群里可能属于别的 bot，所以查不到时给 id 就够。
func chatTag(b *core.Bot, chatID int64) string {
	if c, ok := b.Cache.Snap().ChatConf(b.BotID(), chatID); ok && c.Title != "" {
		return fmt.Sprintf("<code>%d</code>（%s）", chatID, html.EscapeString(c.Title))
	}
	return fmt.Sprintf("<code>%d</code>", chatID)
}

// managedBotsClause 返回按权限过滤 antiad_log 的 SQL 片段与参数：
// 主管理员不设限，次级管理员只看自己名下 bot 的记录。
func managedBotsClause(b *core.Bot, uid int64) (string, []any) {
	return botsClause(b.Shared, uid, b.IsMain(uid))
}

// botsClause 是权限过滤的共同实现：主管理员不设限；名下没有 bot 时
// 用 AND 0 让查询必然为空（而非不过滤、把别人的记录漏出去）。
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
// 仍能拼出返回列表。URL 按钮不带 callback_data，原样保留。
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

// cmdArg 从 /cmd 参数里取第一个参数，容忍 /cmd@botname 形态。
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
