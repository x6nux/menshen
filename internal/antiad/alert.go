package antiad

import (
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// groupSilent 报告该 bot 是否开启了群内静默。
//
// 判定、删除、禁言照常执行，只是不在群里留任何 bot 消息，
// 处置结果走管理员私聊与记录查询。
func groupSilent(b *core.Bot) bool {
	return b.Cache.Snap().BotSettingInt(b.BotID(), "antiad_group_silent", 0) == 1
}

// sendGroup 在群里发一条消息；静默开关打开时什么也不发，返回 0。
// 调用方拿到 0 就不要安排撤回（撤回一个不存在的消息号会删错消息）。
// 关闭链接预览：提示里的 deep link 会挂出预览卡片。
func sendGroup(b *core.Bot, chatID int64, text string, kb map[string]any) int64 {
	if groupSilent(b) {
		return 0
	}
	return b.SendGetIDNoPreview(chatID, text, kb)
}

// groupNotice 在群里发一条自动撤回的提示；静默时不发也不安排撤回。
func groupNotice(b *core.Bot, chatID int64, text string, kb map[string]any, ttl time.Duration) {
	if id := sendGroup(b, chatID, text, kb); id != 0 {
		scheduleAlertCleanup(b, chatID, id, ttl)
	}
}

// sendAdAlert 把一次命中贴到群里（开启群内展示时）。管理员私聊不在这里发：
// 攒成汇总由 FlushAdSummary 定时发，见 summary.go。逐条私聊在刷屏高峰会
// 触发 TG 发送频率限制，并拖慢删除与禁言。
//
// 演练模式下措辞必须表明处置未真正执行，否则群里会误以为广告已被删除。
// note 是 ApplyAction 的失败说明（成功时为空）：告警按实际结果而非
// 意图渲染。
func sendAdAlert(b *core.Bot, conf store.BotChat, m *tg.Message, v adVerdict, act adAction,
	note string, logID int64, dryrun bool) {

	// 默认关闭。
	// 群内静默优先级更高：打开后该 bot 在群里不发任何消息。
	if !conf.GroupAlert || m.Chat == nil || groupSilent(b) {
		return
	}
	// 按 (群, 人) 节流：避免单个刷屏者占满群内告警，也避免触发 TG 对
	// 单群的发送频率限制。压掉的只是这一个用户的重复告警。
	// /check 复查不走这里：由用户主动触发，有自己的命令限频。
	snap := b.Cache.Snap()
	if !b.AdLimits.Allow(fmt.Sprintf("ad:a:%d:%d", m.Chat.ID, m.From.ID),
		snap.BotSettingInt(b.BotID(), "antiad_alert_rpm", 3)) {
		slog.Info("反广告：告警被节流", "chat", m.Chat.ID, "uid", m.From.ID)
		return
	}
	// 贴在群里：处置结果对全群可见，该群的 TG 管理员可直接点按钮判断
	// （见 adDispositionAllowed）。精简版：昵称与原文都不回贴。
	brief, bkb := renderAdAlertBrief(b, m, v, act, note, logID, dryrun)
	groupNotice(b, m.Chat.ID, brief, bkb, alertTTL(b, snap, v))
}

// alertTTL 返回一条群内提醒的存活时间。
//
// 置信度已达删除与禁言线、危害度也高的记录，存活时间取短值：处置已落地，
// 通知仅用于告知；置信度不足的记录取常规值，便于管理员查看。
// 短撤回秒数为 0 时关闭这一逻辑。
func alertTTL(b *core.Bot, snap *store.Snapshot, v adVerdict) time.Duration {
	normal := time.Duration(snap.BotSettingInt(b.BotID(), "antiad_alert_ttl", 300)) * time.Second
	short := snap.BotSettingInt(b.BotID(), "antiad_alert_ttl_hard", 30)
	if short <= 0 || normal == 0 || time.Duration(short)*time.Second >= normal {
		return normal
	}
	hard := int64(snap.BotSettingInt(b.BotID(), "antiad_act_hard", 90))
	severe := snap.BotSettingInt(b.BotID(), "antiad_alert_severe", 2)
	if int64(v.Confidence*100) >= hard && v.Severity >= float64(severe) {
		return time.Duration(short) * time.Second
	}
	return normal
}

// verdictBrief 是群内提醒的结论部分：只说明结论与置信度。
//
// 复判模型的长篇理由留给记录卡片、查看页与私聊汇总等查看详情的位置 ——
// 理由常引述广告原文，贴回群里等于替其再次发送。
func verdictBrief(v adVerdict) string {
	if v.Decider == "manual" {
		return "" // 人工标记的头部已经写明，不必重复
	}
	if v.Confidence <= 0 && v.Severity <= 0 {
		return "" // 判定失败这类没有结论可给时不硬编
	}
	choice := "clean"
	if v.IsAd {
		choice = "ad"
	}
	s := fmt.Sprintf("%s 置信度:%.0f%%", soChoiceLabel(choice), v.Confidence*100)
	if v.Severity > 0 {
		s += fmt.Sprintf("，危害度:%.1f", v.Severity)
	}
	return s
}

// renderAdAlertBrief 渲染群内版告警：一行 uid 与结论，尾部跟文本链接
// （① 申诉入口，② 管理员在全局设置里挂的附加链接）。
//
// 置信度、处置动作、记录编号等细节都收进流水与私聊汇总。昵称与原文都不回贴：
// 广告号的昵称和正文本身就是广告，bot 发回群里等于替其再次发送，还会让
// bot 被 TG 判定为广告号封禁。
//
// 演练标识不能省略：缺少它时群里会误以为广告已被删除。
func renderAdAlertBrief(b *core.Bot, m *tg.Message, v adVerdict, act adAction,
	note string, logID int64, dryrun bool) (string, map[string]any) {

	var sb strings.Builder
	if dryrun {
		sb.WriteString("🧪（演练）")
	}
	sb.WriteString("🚫 ")
	sb.WriteString(userLink(m.From.ID))
	if s := verdictBrief(v); s != "" {
		sb.WriteString(" · " + html.EscapeString(s))
	}
	if links := groupLinks(b, logID); links != "" {
		sb.WriteString("\n" + links)
	}
	return sb.String(), nil
}

// renderReviewClean 渲染复查结论为正常时的群内提示。
//
// 与广告告警分开：正常结论不带 🚫，也不附申诉入口 —— 该用户未被处置，
// 申诉入口对其无意义，出现在群里反而像处罚通知。管理员在全局设置里挂的
// 附加链接仍然附上（属于使用说明一类内容）。
// lifted 表示本次复查一并解除了禁言：复判期留下的临时禁言，或原判的
// 正式禁言（/check 判正常时会一并解除，见 reviewAndAct）。
func renderReviewClean(b *core.Bot, m *tg.Message, v adVerdict, lifted bool) string {
	var sb strings.Builder
	sb.WriteString("✅ ")
	sb.WriteString(userLink(m.From.ID))
	if s := verdictBrief(v); s != "" {
		sb.WriteString(" · " + html.EscapeString(s))
	}
	if lifted {
		sb.WriteString("，已解除禁言。")
	} else {
		sb.WriteString("，未处置。")
	}
	if extra := groupFooter(b); extra != "" {
		sb.WriteString("\n" + extra)
	}
	return sb.String()
}

// groupLinks 渲染群内提示尾部的文本链接块：
//
//	① <a href="https://t.me/<bot>?start=<记录号>">点我申诉</a>
//	② <全局设置里的附加链接原文>
//
// 链接以 HTML anchor 嵌在文字上，不把网址原样附在后面：网址暴露在群里
// 占用空间且容易被误当成正文。
// 带记录号的 deep link 按身份分流——管理员点进记录卡片，被限制的用户进
// 申诉入口。
func groupLinks(b *core.Bot, logID int64) string {
	var lines []string
	if b.Username != "" {
		lines = append(lines, `① <a href="`+html.EscapeString(botDeepLink(b, logID))+
			`">点我申诉</a>`)
	}
	if extra := groupFooter(b); extra != "" {
		lines = append(lines, extra)
	}
	return strings.Join(lines, "\n")
}

// groupFooter 是全局设置里的附加链接（使用说明、规则地址等），群内提示
// 尾部原样附上。按纯文本转义：里面出现 HTML 时不该改排版；其中写明的
// 网址由 Telegram 客户端自动识别成链接。
func groupFooter(b *core.Bot) string {
	return html.EscapeString(strings.TrimSpace(
		b.Cache.Snap().Setting("antiad_group_footer")))
}

// botDeepLink 拼打开 bot 的深链。带上记录号：管理员点进去直接落到
// 那条记录卡片（panel.ShowLogCard 会再查一次权限），普通用户进申诉入口。
// 没有记录号（理论上不该发生）时退回 appeal 载荷。
func botDeepLink(b *core.Bot, logID int64) string {
	payload := "appeal"
	if logID > 0 {
		payload = logPayload(logID)
	}
	return "https://t.me/" + b.Username + "?start=" + payload
}

// userLink 渲染可点开的用户 ID。不写昵称：广告号的昵称本身就是广告，
// bot 把它发出去与发原文无异；点开链接由 TG 客户端自己显示资料。
// 频道（负 ID）没有 tg://user 链接可用，只给 ID，频道名同样不写。
func userLink(uid int64) string {
	if uid < 0 {
		return fmt.Sprintf(`<code>%d</code>（频道）`, uid)
	}
	return fmt.Sprintf(`<a href="tg://user?id=%d">%d</a>`, uid, uid)
}

// scheduleAlertCleanup 安排到点撤回 bot 自己发的群内消息（告警卡片、
// 命令结果这类通知）。
//
// 群内连续告警会挤走正常对话，而告警的信息价值在处置完成后即消失。
// 撤回的只是 bot 自己那条，判定流水与处置结果都留在库里，可由面板的
// 拦截记录查回。
//
// 告警撤回后，其上的处置按钮也随之消失，因此 ttl 可配为 0 表示永不撤回。
//
// 记进 alert_cleanup，由 SweepAlertCleanup 每分钟撤一批，最多延迟一分钟。
// 不使用内存计时器：进程重启后计时器丢失，尚未到点的告警会永久留在群里。
func scheduleAlertCleanup(b *core.Bot, chatID, msgID int64, ttl time.Duration) {
	if ttl <= 0 || msgID == 0 {
		// msgID 为 0 说明没拿到发送结果。传 0 给 TG 会被解读成别的消息，
		// 因此不能撤。
		return
	}
	if _, err := b.Store.Write.Exec(`INSERT OR REPLACE INTO alert_cleanup
		(bot_id,chat_id,message_id,due_at) VALUES (?,?,?,?)`,
		b.BotID(), chatID, msgID, time.Now().Add(ttl).Unix()); err != nil {
		slog.Error("反广告：记录待撤回告警失败", "chat", chatID, "msg", msgID, "err", err)
	}
}

// SweepAlertCleanup 撤回该 bot 到点的群内告警。每条只尝试一次：撤不掉的
// （已被删除、超过 TG 的 48 小时删除期限）重试无效。
//
// 按群聚合后用 deleteMessages 批量删除（TG 单次最多 100 条）：告警较多时
// 逐条 deleteMessage 会产生大量往返并阻塞分钟 tick。DB 行在整轮结束后
// 用一条 DELETE 收尾。
func SweepAlertCleanup(b *core.Bot, now time.Time) {
	rows, err := b.Store.Read.Query(`SELECT chat_id,message_id FROM alert_cleanup
		WHERE bot_id=? AND due_at <= ? LIMIT 200`, b.BotID(), now.Unix())
	if err != nil {
		slog.Error("反广告：读取待撤回告警失败", "err", err)
		return
	}
	byChat := map[int64][]int64{}
	var chatOrder []int64
	for rows.Next() {
		var c, m int64
		if rows.Scan(&c, &m) != nil {
			continue
		}
		if _, ok := byChat[c]; !ok {
			chatOrder = append(chatOrder, c)
		}
		byChat[c] = append(byChat[c], m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Error("反广告：待撤回告警读取中断，本轮结果可能不完整", "err", err)
	}
	if len(chatOrder) == 0 {
		return
	}

	for _, chatID := range chatOrder {
		ids := byChat[chatID]
		if ok, desc := deleteMessages(b, chatID, ids); !ok {
			slog.Warn("反广告：撤回群内告警失败", "chat", chatID,
				"count", len(ids), "tg_error", desc)
		}
	}

	// 一次清掉本轮读到的行：主键精确匹配，不用 bot_id+due_at 的粗粒度
	// 条件——那会连带删掉 SELECT 之后才到点、本轮没处理的行。
	ph := make([]string, 0, len(byChat))
	args := make([]any, 0, len(byChat)*2)
	for _, chatID := range chatOrder {
		for _, msgID := range byChat[chatID] {
			ph = append(ph, "(?,?)")
			args = append(args, chatID, msgID)
		}
	}
	if _, err := b.Store.Write.Exec(`DELETE FROM alert_cleanup
		WHERE (chat_id,message_id) IN (`+strings.Join(ph, ",")+`)`, args...); err != nil {
		slog.Error("反广告：清理待撤回告警记录失败", "err", err)
	}
}

// adAlertRows 构造处置按钮行。私聊版与群内版共用，避免删除失败后需重新
// 放回按钮一类规则在两侧不一致。
//
// 按钮按当前还能执行的操作动态生成：已删除的记录不再提供删除按钮。
// callback_data 形如 a:ad:fp:12345，远小于 64 字节上限。
// muteLabel 由调用方按该 bot 的禁言时长渲染（永久禁言需可辨识）。
func adAlertRows(act adAction, action string, note string, logID int64, dryrun bool,
	muteLabel string) [][][2]string {

	rows := [][][2]string{{
		{"✅ 判定正确", fmt.Sprintf("a:ad:ok:%d", logID)},
		{"↩️ 误判", fmt.Sprintf("a:ad:fp:%d", logID)},
	}}
	// 失败的那一步也要放回按钮：act.Delete 为真只说明打算删除，
	// 删失败时若隐藏按钮，需要人工处置时便没有入口。
	var manual [][2]string
	if dryrun || !act.Delete || strings.Contains(note, noteDeleteFailed) {
		manual = append(manual, [2]string{"🗑 删除", fmt.Sprintf("a:ad:del:%d", logID)})
	}
	if dryrun || (!act.Mute && !act.Ban) || strings.Contains(note, noteMuteFailed) {
		manual = append(manual, [2]string{muteLabel, fmt.Sprintf("a:ad:mute:%d", logID)})
	}
	if len(manual) > 0 {
		rows = append(rows, manual)
	}
	bottom := [][2]string{{"🚫 封禁出群", fmt.Sprintf("a:ad:ban:%d", logID)}}
	// 解封：撤销仍在生效的限制，但判定维持正确 —— 与 `↩️ 误判` 不同，
	// 不修改样本池、命中数与内容哈希。仅当记录中确实带有约束（禁言/封禁/
	// 进群限制/联封）时才提供此按钮。
	name := strings.TrimPrefix(action, "dryrun:")
	if act.Mute || act.Ban || name == "join_muted" || name == "gban_muted" ||
		name == "gban_banned" {
		bottom = append(bottom, [2]string{"🔓 解封（判定维持）",
			fmt.Sprintf("a:ad:rel:%d", logID)})
	}
	rows = append(rows, bottom)
	return rows
}

// adAlertLinks 是记录卡片底部的链接按钮行：只留 `📝 申诉` deep link。
//
// 原文与理由已直接印在卡片上（见 RenderAdRecord），不再提供跳转网页的
// 链接，以免管理员的判断多一步跳转。bot 有用户名时才能拼出 deep link。
func adAlertLinks(b *core.Bot, logID int64) [][2]string {
	if b.Username == "" {
		return nil
	}
	return [][2]string{{"📝 申诉", tg.URLBtn(botDeepLink(b, logID))}}
}
