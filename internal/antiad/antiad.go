package antiad

import (
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"menshen/internal/billing"
	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// chatActive 报告反广告在该群对本 bot 是否生效，并带回该群的配置。
//
// 两道门都是纯内存判断，未生效时零 API 调用、零数据库访问——
// bot 可能还在一堆无关群里，一开开关就对所有群生效是不可接受的。
//
//   - antiad_enabled 是**全平台急停**，只有主管理员能动
//   - bot_chats 里那一行的 enabled 是这个 bot 在这个群的开关，归 owner 管
func chatActive(b *core.Bot, chatID int64) (store.BotChat, bool) {
	snap := b.Cache.Snap()
	if snap.SettingInt("antiad_enabled", 0) != 1 {
		return store.BotChat{}, false
	}
	c, ok := snap.ChatConf(b.BotID(), chatID)
	if !ok || !c.Enabled {
		return store.BotChat{}, false
	}
	return c, true
}

// groupMember 是 group_members 的一行。
type groupMember struct {
	ChatID, UserID      int64
	JoinedAt, FirstSeen int64
	MsgCount, LastMsgAt int64
	AdHits              int64
}

func loadMember(s *store.Store, chatID, uid int64) (groupMember, bool) {
	gm := groupMember{ChatID: chatID, UserID: uid}
	err := s.Read.QueryRow(`SELECT joined_at,first_seen,msg_count,last_msg_at,ad_hits
		FROM group_members WHERE chat_id=? AND user_id=?`, chatID, uid).
		Scan(&gm.JoinedAt, &gm.FirstSeen, &gm.MsgCount, &gm.LastMsgAt, &gm.AdHits)
	if err != nil {
		return gm, false
	}
	return gm, true
}

// handleChatMemberUpdate 把「进群」落成 joined_at。
//
// 只认「从非成员状态变为成员状态」这一次跃迁：status 在 member 与
// restricted 之间来回跳（群管给人挂临时限制）不是进群，按进群处理会
// 把一个老成员重新变成「新人」，下一条消息就被按最严档处置。
func HandleChatMemberUpdate(b *core.Bot, cu *tg.ChatMemberUpdated) {
	if cu == nil || cu.Chat == nil || cu.NewChatMember == nil ||
		cu.NewChatMember.User == nil {
		return
	}
	conf, ok := chatActive(b, cu.Chat.ID)
	if !ok {
		return
	}
	old := ""
	if cu.OldChatMember != nil {
		old = cu.OldChatMember.Status
	}
	if !isMemberStatus(cu.NewChatMember.Status) || isMemberStatus(old) {
		return
	}
	if !b.ClaimSender(cu.Chat.ID, cu.NewChatMember.User.ID, time.Now()) {
		return
	}
	at := cu.Date
	if at == 0 {
		at = time.Now().Unix()
	}
	onJoin(b, conf, cu.NewChatMember.User, at)
}

// isMemberStatus 报告该状态是否代表「人在群里」。
// restricted 也算：它是 TG 表达「权限非默认满值」的方式，
// 普通成员在设了默认限制的群里就是这个状态。
func isMemberStatus(s string) bool {
	switch s {
	case "member", "administrator", "creator", "restricted":
		return true
	}
	return false
}

// recordJoin 写入进群时刻。已有记录则只更新 joined_at，
// 不动 msg_count —— 退群重进的人，历史发言量仍是有效画像。
func recordJoin(b *core.Bot, chatID, uid, at int64) {
	if _, err := b.Store.Write.Exec(`INSERT INTO group_members
		(chat_id,user_id,joined_at,first_seen,msg_count,last_msg_at,ad_hits)
		VALUES (?,?,?,?,0,0,0)
		ON CONFLICT(chat_id,user_id) DO UPDATE SET joined_at=excluded.joined_at`,
		chatID, uid, at, at); err != nil {
		slog.Error("反广告：记录进群失败", "chat", chatID, "uid", uid, "err", err)
	}
}

// touchMember 记一条发言并返回更新后的画像。
//
// first_seen 只在插入时写，后续发言不得覆盖：它是 joined_at 缺失时
// 唯一的年龄下界，被每条消息刷新的话所有人都会永远是「刚出现」。
func touchMember(b *core.Bot, chatID, uid, at int64) groupMember {
	if _, err := b.Store.Write.Exec(`INSERT INTO group_members
		(chat_id,user_id,joined_at,first_seen,msg_count,last_msg_at,ad_hits)
		VALUES (?,?,0,?,1,?,0)
		ON CONFLICT(chat_id,user_id) DO UPDATE SET
		  msg_count = msg_count + 1,
		  last_msg_at = excluded.last_msg_at`,
		chatID, uid, at, at); err != nil {
		slog.Error("反广告：记录发言失败", "chat", chatID, "uid", uid, "err", err)
	}
	gm, _ := loadMember(b.Store, chatID, uid)
	return gm
}

// gmsgTextLimit 是留底正文的字符上限。与判定日志同一量级：
// 留底是给人和模型复查用的，不是原样归档聊天记录。
const gmsgTextLimit = 1000

// recordMessage 把一条群消息留底，供 /ad 事后复查与连带删除。编辑过的
// 消息覆盖正文：复查要看的是群里现在显示的样子。album 是相册 ID。
//
// 失败只记日志不中断：留底是复查用的辅助设施，不该让一次写失败
// 把这条消息的判定也一起拖没。
func recordMessage(b *core.Bot, chatID, msgID, uid int64, text string, at int64, album string) {
	if _, err := b.Store.Write.Exec(`INSERT INTO group_messages
		(chat_id,message_id,user_id,text,at,media_group) VALUES (?,?,?,?,?,?)
		ON CONFLICT(chat_id,message_id) DO UPDATE SET text=excluded.text`,
		chatID, msgID, uid, core.TruncateRunes(text, gmsgTextLimit), at, album); err != nil {
		slog.Error("反广告：留底失败", "chat", chatID, "msg", msgID, "err", err)
	}
}

// recordedText 取这条消息的留底正文，没有留底时返回空串。
func recordedText(b *core.Bot, chatID, msgID int64) string {
	var t string
	b.Store.Read.QueryRow(`SELECT text FROM group_messages
		WHERE chat_id=? AND message_id=?`, chatID, msgID).Scan(&t)
	return t
}

// gmsgRow 是一条留底记录。
type gmsgRow struct {
	MessageID int64
	Text      string
	At        int64
}

// loadUserMessages 取某人在某群的留底，按时间正序返回最近 limit 条。
//
// 正序是给模型看的：复查要判断「这个账号一路以来在干什么」，
// 倒序会让它把最新的当成开头。
func loadUserMessages(s *store.Store, chatID, uid int64, limit int) []gmsgRow {
	// 没有文字的（图片、贴纸）只为连带删除留着 ID，复查里是空行。
	rows, err := s.Read.Query(`SELECT message_id,text,at FROM group_messages
		WHERE chat_id=? AND user_id=? AND text != ''
		ORDER BY at DESC, message_id DESC LIMIT ?`,
		chatID, uid, limit)
	if err != nil {
		slog.Error("反广告：读取留底失败", "chat", chatID, "uid", uid, "err", err)
		return nil
	}
	defer rows.Close()

	var out []gmsgRow
	for rows.Next() {
		var r gmsgRow
		if err := rows.Scan(&r.MessageID, &r.Text, &r.At); err != nil {
			slog.Error("反广告：留底行解析失败", "err", err)
			continue
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		slog.Error("反广告：留底游标出错，结果可能不完整", "err", err)
	}
	// 查询按倒序取「最近 N 条」，返回前翻正序。
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// adReviewLimit 是一次复查送检的留底条数上限。
// 全部历史一次性给模型是这条命令的意义所在，但仍要有个天花板：
// 一个刷了几千条的号会把单次请求撑成天价。
const adReviewLimit = 60

const adCmdUsage = "用法：回复某人的消息发 <code>/ad</code>，" +
	"或直接发 <code>/ad &lt;user_id&gt;</code>（频道填 -100 开头的频道 ID）。\n" +
	"会把该用户在本群的全部留底一次性交给两个模型复查。"

// parseAdCommand 识别 /ad、/adb、/adw 并取出命令名与参数。
//
// 群里 TG 客户端会自动补成 /ad@botname，必须一并认。
// 几条命令合并识别，因为 /adb、/adw 以 /ad 为前缀 —— 分开写的话，
// 先匹配 /ad 的那一方会把它们也吃掉。
func parseAdCommand(text string) (cmd, arg string, ok bool) {
	f := strings.Fields(text)
	if len(f) == 0 {
		return "", "", false
	}
	head := f[0]
	if i := strings.Index(head, "@"); i > 0 {
		head = head[:i]
	}
	switch head {
	case "/ad", "/adb", "/adw":
		return head, strings.Join(f[1:], " "), true
	}
	return "", "", false
}

// handleAdCommand 处理群内 /ad 复查。
//
// 命令对所有人开放（它不直接封禁，走的是与自动判定同一套处置矩阵），
// 因此它本身就是一个花钱入口：一次复查跑两个模型、带上目标全部历史。
// 必须按发起人限频，否则任何成员都能靠刷命令烧钱。
func HandleAdCommand(b *core.Bot, conf store.BotChat, m *tg.Message, arg string) {
	snap := b.Cache.Snap()

	var target *tg.TGUser
	var targetMsgID int64
	switch {
	case m.ReplyToMessage != nil && m.ReplyToMessage.From != nil:
		target = senderOf(m.ReplyToMessage)
		targetMsgID = m.ReplyToMessage.MessageID
	case arg != "":
		// 负数是频道 ID（以频道身份发言的记录就记在频道名下）。
		uid, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
		if err != nil || uid == 0 {
			b.Send(m.Chat.ID, adCmdUsage, nil)
			return
		}
		target = &tg.TGUser{ID: uid}
	default:
		b.Send(m.Chat.ID, adCmdUsage, nil)
		return
	}

	if !b.AdLimits.Allow(fmt.Sprintf("ad:cmd:%d", m.From.ID),
		snap.BotSettingInt(b.BotID(), "antiad_cmd_rpm", 3)) {
		b.Send(m.Chat.ID, "复查太频繁，请稍后再试。", nil)
		return
	}

	hist := loadUserMessages(b.Store, m.Chat.ID, target.ID, adReviewLimit)
	if len(hist) == 0 {
		b.Send(m.Chat.ID, "该用户在本群没有留底消息，无法复查。", nil)
		return
	}

	// 构造一条「代表消息」：处置要作用在具体消息上（删除），
	// 而 /ad <user_id> 这一路没有具体消息，此时 MessageID 为 0，
	// 下面会把删除动作摘掉。
	// 代表消息的正文：回复形态下用被回复的那一条，否则用最新一条留底。
	// 两者不一致会让告警展示的原文与「删除」按钮实际作用的消息对不上。
	repText := hist[len(hist)-1].Text
	if m.ReplyToMessage != nil {
		if t := displayText(m.ReplyToMessage); t != "" {
			repText = t
		}
	}
	tgt := &tg.Message{Chat: m.Chat, From: target, MessageID: targetMsgID, Text: repText}
	gm, _ := loadMember(b.Store, m.Chat.ID, target.ID)
	profile := buildProfile(b, tgt, gm, time.Now().Unix())
	state := buildState(b, snap, tgt, profile)
	state.ReviewHistory = make([]core.CtxMsg, 0, len(hist))
	for _, h := range hist {
		state.ReviewHistory = append(state.ReviewHistory,
			core.CtxMsg{Name: senderName(target), Text: h.Text, At: h.At})
	}

	if !b.AdSubmit(func() { reviewAndAct(b, snap, conf, tgt, profile, state) }) {
		b.Send(m.Chat.ID, "判定通道繁忙，请稍后再试。", nil)
	}
}

// canMarkAd 报告此人能否用 /adb 直接标记广告。
//
// 比 /ad 严格得多：/ad 只是花钱跑一次判定，处置仍由矩阵决定；
// /adb 绕过判定直接删人禁言，对所有人开放等于把删消息的权力给了全群。
func canMarkAd(b *core.Bot, chatID, uid int64) bool {
	if b.IsMain(uid) || uid == b.Owner() {
		return true
	}
	return IsChatAdmin(b, chatID, uid)
}

const adbCmdUsage = "用法：<b>回复</b>要标记的那条消息，发送 <code>/adb</code>。\n" +
	"会直接按最高档处置（删除 + 禁言），不经过 AI 判定。"

// handleAdbCommand 人工把一条消息标记为广告并立即处置。
//
// 不发任何 AI 请求：人已经看明白了，再花一次钱去问模型没有意义。
// 代价是这条判定没有置信度可言，所以它只对群管理员及以上开放。
//
// 标记结果会进**正例池**（verdict='ad' 且 action != 'undone'），
// 形态总结下一轮就能学到它 —— 人工标记是质量最高的训练样本。
func HandleAdbCommand(b *core.Bot, conf store.BotChat, m *tg.Message) {
	// 非授权者静默忽略：回一句「你没有权限」等于告诉刷屏的人这条命令
	// 存在、值得去试。
	if !canMarkAd(b, conf.ChatID, m.From.ID) {
		return
	}
	// 命令本身不该留在群里。删不掉也不影响后续，忽略结果。
	b.TG.Call("deleteMessage", map[string]any{
		"chat_id": conf.ChatID, "message_id": m.MessageID,
	})

	if m.ReplyToMessage == nil || m.ReplyToMessage.From == nil {
		scheduleAlertCleanup(b, conf.ChatID,
			b.SendGetID(conf.ChatID, adbCmdUsage, nil), 30*time.Second)
		return
	}
	// 以频道身份或访客 bot 发的，处置要落在频道/召唤者身上。
	target := asSender(m.ReplyToMessage)
	if target.From.ID == b.BotID() {
		return // 别把 bot 自己的告警标成广告
	}

	snap := b.Cache.Snap()
	if !b.AdLimits.Allow(fmt.Sprintf("adb:%d", m.From.ID),
		snap.BotSettingInt(b.BotID(), "antiad_cmd_rpm", 3)) {
		scheduleAlertCleanup(b, conf.ChatID,
			b.SendGetID(conf.ChatID, "操作太频繁，请稍后再试。", nil), 30*time.Second)
		return
	}

	v := adVerdict{
		IsAd: true, Confidence: 1, Kind: "manual",
		Decider: "manual",
		Reason: fmt.Sprintf("由 %s (%d) 人工标记为广告",
			senderName(m.From), m.From.ID),
	}
	// 人工标记直接取最高档：判断已经由人做出，不必再过阈值矩阵。
	// 禁言与否仍服从本群的处罚方式（可改为封禁）。
	act := withPunish(adAction{Delete: true, Mute: true, Alert: true, Name: "deleted_muted"},
		snap.BanMode(conf))

	note := ApplyAction(b, target, act, conf.Dryrun)
	logID := logAd(b, target, v, logAction(act, conf.Dryrun), note)

	if !conf.Dryrun {
		BumpAdHits(b, conf.ChatID, target.From.ID, 1)
		maybeGban(b, conf.ChatID, target.From.ID,
			"人工标记："+core.TruncateRunes(displayText(target), 60))
	}

	text, kb := renderAdAlertBrief(b, target, v, act, note, logID, conf.Dryrun)
	scheduleAlertCleanup(b, conf.ChatID,
		b.SendGetID(conf.ChatID, "🖐 <b>人工标记</b>\n"+text, kb),
		time.Duration(snap.BotSettingInt(b.BotID(), "antiad_alert_ttl", 300))*time.Second)
}

// reviewAndAct 跑复查并按处置矩阵动作，结果贴回群里。
func reviewAndAct(b *core.Bot, snap *store.Snapshot, conf store.BotChat, tgt *tg.Message,
	profile senderProfile, state adState) {

	chatID := conf.ChatID
	enrichSender(b, &state.Sender)

	v, err := judgeBoth(b, snap, state)
	if err != nil {
		slog.Warn("反广告：复查失败", "chat", chatID, "uid", tgt.From.ID, "err", err)
		b.Send(chatID, "复查失败："+html.EscapeString(core.TruncateRunes(err.Error(), 200)), nil)
		return
	}

	dryrun := conf.Dryrun
	act := planAction(b, snap, conf, isNewbie(b, snap, profile), v)
	if tgt.MessageID == 0 {
		// /ad <user_id> 没有指向具体消息，删不了任何东西。
		// 不摘掉的话 deleteMessage 必然失败，告警里还会多一条假的失败说明。
		act.Delete = false
	}
	note := ApplyAction(b, tgt, act, dryrun)
	logID := logAd(b, tgt, v, logAction(act, dryrun), logNote(act, note, dryrun))
	if act.Name != "none" && !dryrun {
		BumpAdHits(b, chatID, tgt.From.ID, 1)
		if act.Mute || act.Ban {
			maybeGban(b, chatID, tgt.From.ID, "复查判定："+core.TruncateRunes(v.Reason, 80))
		}
	}

	// 与自动告警的群内版用同一份渲染：同一个渠道不该有两种长度，
	// 也同样不把广告原文整段贴回群里。
	text, kb := renderAdAlertBrief(b, tgt, v, act, note, logID, dryrun)
	scheduleAlertCleanup(b, chatID, b.SendGetID(chatID, "🔎 <b>复查结果</b>\n"+text, kb),
		time.Duration(snap.BotSettingInt(b.BotID(), "antiad_alert_ttl", 300))*time.Second)
}

// handleGroupMessage 是群消息的总入口，也接编辑过的消息（edited_message）。
func HandleGroupMessage(b *core.Bot, m *tg.Message) {
	if m.Chat == nil || m.From == nil {
		return
	}
	// 以频道身份发言、访客 bot 代发的，换成实际发言者：豁免、画像、留底、
	// 处置都落在他身上，删除仍作用于这条消息本身。「bot 一律豁免」对这两类
	// 不成立，否则随便 @ 几个广告 bot、或换成频道身份就能绕过检测。
	m = asSender(m)
	conf, active := chatActive(b, m.Chat.ID)
	if !active {
		return
	}
	edited := m.EditDate != 0

	at := m.Date
	if at == 0 {
		at = time.Now().Unix()
	}

	// service 消息兜底：chat_member 在 bot 权限变动期间可能漏收，
	// 而「谁刚进群」是整个分档的基础，两条路都要接。
	// 进群按进群的人分给 bot，不按发这条 service 消息的人：
	// 管理员拉人进群时 from 是管理员。
	if len(m.NewChatMembers) > 0 {
		for _, nu := range m.NewChatMembers {
			if nu != nil && b.ClaimSender(m.Chat.ID, nu.ID, time.Now()) {
				onJoin(b, conf, nu, at)
			}
		}
		return // 入群 service 消息本身没有正文，不进判定
	}

	// 同群有几个 bot 时，这个人只归其中一个（见 core/shard.go）。必须排在
	// 画像计数之前：每个 bot 各计一次的话，新人的发言数会翻倍涨成老成员。
	if !b.ClaimSender(m.Chat.ID, m.From.ID, time.Now()) {
		return
	}

	// 资历累计必须先于「无正文就早退」。纯图/贴纸同样是在群里活动的证据，
	// 不计的话这类成员 msg_count 永不增长，在 isNewbie 的发言数轴上
	// 永远算新人；而年龄轴在 joined_at 缺失时本就不参与判定，
	// 两条轴一起失效就等于「老成员低风险」对他们完全不成立。
	// 编辑不是新发言：计进资历的话，反复编辑一条就能把新人刷成老成员。
	var gm groupMember
	if edited {
		gm, _ = loadMember(b.Store, m.Chat.ID, m.From.ID)
	} else {
		gm = touchMember(b, m.Chat.ID, m.From.ID, at)
	}

	// 命令不是群聊内容：不留底（留了会被送进下一次复查），也不进判定。
	// 取本人正文而不是 displayText，否则回复形态下被引用的原文会混进参数。
	// 命令只在发出时执行一次：编辑一条旧命令不该再执行一遍。
	if cmd, arg, ok := parseAdCommand(m.Text); ok {
		if !edited {
			switch cmd {
			case "/ad":
				HandleAdCommand(b, conf, m, arg)
			case "/adb":
				HandleAdbCommand(b, conf, m)
			case "/adw":
				HandleAdwCommand(b, conf, m, arg)
			}
		}
		return
	}

	// 判空看的是「本人正文 + 引用内容」的合集，不能只看本人正文：
	// 规避形态的极限版是一个字都不发（只发贴纸），载荷全在引用块里。
	text := displayText(m)

	// TG 会因为 bot 用不到的字段变化推来「编辑」（官方文档原话）。
	// 正文没变就什么都不做，否则每次这类变动都要再花一次送检的钱。
	if edited && recordedText(b, m.Chat.ID, m.MessageID) == core.TruncateRunes(text, gmsgTextLimit) {
		return
	}

	// 留底先于一切判定分支。豁免者、被护栏拦下的、判定失败的都要留：
	// /ad 复查与 recent_context 需要的恰恰是这些「没被判过」的消息。
	// 没有文字的（图片、贴纸）也留：判成广告号时要连带删掉此人近期的
	// 全部消息，靠的是这里的 ID。
	recordMessage(b, m.Chat.ID, m.MessageID, m.From.ID, text, at, m.MediaGroupID)

	// 相册里判定之后才到的那几张：整组已判成广告，到一张删一张。
	if m.MediaGroupID != "" && albumDoomed(b, m.Chat.ID, m.MediaGroupID) {
		b.CallOK("deleteMessage", map[string]any{"chat_id": m.Chat.ID, "message_id": m.MessageID})
		return
	}

	snap := b.Cache.Snap()
	// 纯图/贴纸配了识图模型才判（识图在判定 worker 里做，见 judgeAndAct）。
	if _, seeable := visualOf(m); text == "" && !(seeable && visionOn(snap)) {
		return
	}

	if adExempt(b, snap, m.Chat.ID, m.From) {
		return
	}

	profile := buildProfile(b, m, gm, at)
	// buildState 必须留在同步段：recent_context 读的是本人此前的留底，
	// 更新按到达顺序串行处理，此刻库里恰好是「这条之前」的全部发言。
	// 挪进判定 worker 的话，此人随后几条也可能已经落库，模型会把
	// 「之后说的话」当成上下文。
	state := buildState(b, snap, m, profile)

	// 同样的内容此前已判为消息级广告：不送检、不占本群送检额度，直接删，
	// 禁言交给复判模型——必须排在护栏之前。
	if text != "" {
		if h, ok := lookupAdHash(b, text); ok {
			if !b.AdSubmit(func() { hashHit(b, snap, conf, m, profile, state, h, text) }) {
				slog.Warn("反广告：判定队列已满，哈希命中未处置", "chat", m.Chat.ID, "uid", m.From.ID)
			}
			return
		}
	}

	if ok, why := adAllow(b, snap, m.Chat.ID, m.MessageID, m.EditDate,
		isNewbie(b, snap, profile)); !ok {
		// 护栏拦下的同样按放行处理，只是不花这次 AI 的钱。
		slog.Info("反广告：护栏拦下，未送检", "chat", m.Chat.ID,
			"uid", m.From.ID, "why", why)
		// 重复投递的是已经处理过的同一条，不用再记；其余的要记：
		// 那是一条真实的消息被放过了，面板上必须看得见。
		if why != adDupMessage {
			logAd(b, m, adVerdict{Decider: adDeciderSkipped, Reason: why}, "none", "未送检")
		}
		return
	}

	// 判定要发 1~2 次 AI 请求（带重试最坏几十秒），而更新处理是串行的。
	// 同步等在这里，上游一慢整个 bot 就停摆——管理员连「关闭反广告」
	// 都点不动，而上游抖动恰恰是最需要关掉它的时刻。
	if !b.AdSubmit(func() { judgeAndAct(b, snap, conf, m, profile, state) }) {
		// 队列已满就放行这一条。与超时放行是同一个失败方向：宁可漏判，
		// 也不能让判定链路反过来拖垮 bot 本身。
		slog.Warn("反广告：判定队列已满，本条放行", "chat", m.Chat.ID, "uid", m.From.ID)
		logAd(b, m, adVerdict{Reason: "判定队列已满"}, "none", "判定队列已满，未送检")
	}
}

// judgeAndAct 是判定与处置，跑在判定 worker 上。
//
// 先删后判：初判（systemone）一出结论就先动手——删消息、要罚的先临时禁言
// （tempMute），再把大模型复判投进**单独的**复判队列，定案后补正式处罚、
// 连带删除与告警。大模型再慢，广告也不会一直挂在群里，发广告的人也发不了
// 下一条。初判低于采信线、或者要罚才复判（needReview）。
//
// 进程退出时不等待在飞的判定：它们最长几十秒，且并发有上限，全部只会
// 写自己的流水。store 关闭后写操作返回错误而非 panic（database/sql
// 的既有行为），最坏结果是日志里多几条写失败——用一整套等待机制换
// 这个，不划算。
func judgeAndAct(b *core.Bot, snap *store.Snapshot, conf store.BotChat, m *tg.Message,
	profile senderProfile, state adState) {

	// 简介与简介里的链接要发 getChat，所以放在异步段取：同步段每多一次 TG
	// 往返，更新处理就多停一次。
	enrichSender(b, &state.Sender)

	// 内容哈希按识图之前的文字记，与 HandleGroupMessage 查的是同一个键。
	// 纯图没有文字：空串当键的话，之后所有纯图都会互相命中。
	text := displayText(m)
	m, state, vis, ok := seeVisual(b, snap, m, state, text)
	if !ok {
		return
	}

	v, err := judgeFirst(b, snap, state)
	if err != nil {
		// 失败一律放行。反向会在上游抖动时清空整个群聊。
		slog.Warn("反广告：判定失败，放行", "chat", m.Chat.ID,
			"uid", m.From.ID, "err", err)
		logAd(b, m, adVerdict{Reason: err.Error(), Usage: vis.Usage, Cost: vis.Cost},
			"none", "判定失败")
		return
	}
	// 识图的钱也是判这条消息花的。
	v.Usage, v.Cost = billing.MergeUsage(v.Usage, vis.Usage), v.Cost+vis.Cost

	finish := func(v adVerdict, pre adAction, preNote string) {
		actOnVerdict(b, snap, conf, m, profile, v, pre, preNote, text)
	}
	act := planAction(b, snap, conf, isNewbie(b, snap, profile), v)
	if !needReview(b, snap, v, act) {
		finish(v, adAction{}, "")
		return
	}
	// 复判前先按初判动手：删消息、临时禁言。连带删除、封禁、告警都等复判
	// 定了再做——封禁踢出群，不适合当先行动作。频道身份不先封：TG 封频道身份
	// 不支持限时，封了就得等人手工解。
	pre := adAction{Delete: act.Delete, Mute: (act.Mute || act.Ban) && m.From.ID > 0, Temp: true}
	preNote := ApplyAction(b, m, pre, conf.Dryrun)
	if !b.AdReview(func() { finish(review(b, snap, state, v, llmSystemPrompt), pre, preNote) }) {
		// 按初判定案，临时禁言照样转正式：让它到期自己解除，等于白白放走。
		slog.Warn("反广告：复判队列已满，按初判定案", "chat", m.Chat.ID, "uid", m.From.ID)
		v.Reason = "（复判队列已满，仅采信 systemone 初判）" + v.Reason
		finish(v, pre, preNote)
	}
}

// adAllow 是送检前的滥用护栏，返回 false 表示这条不送检。
//
// 用户明知成本地选择了全量送检，所以这里不做抽样、不关送检，只堵滥用：
// 任何普通群成员都能靠刷屏把钱烧掉，这与「按设计每条都判」是两回事。
func adAllow(b *core.Bot, snap *store.Snapshot, chatID, msgID, editDate int64, newbie bool) (bool, string) {
	// 去重按消息 ID，只挡同一条消息被处理两次（重启后 TG 重发了更新）。
	// 不按文字去重：那样多号轮番刷同一段模板，只有第一个号被判，其余全被
	// 当成「重复」既不判也不删——线上实测漏过。同文刷屏的成本由内容哈希
	// （hash.go）兜住，它排在这之前。
	// 带上编辑时间：编辑成广告的那一版与原版同一个 ID，常常就在一分钟内。
	if !b.AdLimits.Allow(fmt.Sprintf("ad:d:%d:%d:%d", chatID, msgID, editDate), 1) {
		return false, adDupMessage
	}
	// 每群上限只管老成员，新人既不受限也不占额度：广告几乎都出自新号，而额度
	// 多半是被老成员的日常聊天占满的——先到先得的话，刷屏高峰里最先被放过的
	// 恰恰是广告号。新号刷屏的成本由内容哈希和判成广告后的禁言兜住。
	if !newbie && !b.AdLimits.Allow(fmt.Sprintf("ad:c:%d", chatID),
		snap.BotSettingInt(b.BotID(), "antiad_rpm_chat", 30)) {
		return false, "超出本群送检频率上限"
	}
	return true, ""
}

const (
	adDupMessage = "同一条消息重复投递"
	// adDeciderSkipped 标记「护栏拦下、没有送检」的流水，verdict 记为 skipped。
	adDeciderSkipped = "skipped"
)

const chatAdminTTL = 10 * time.Minute

type chatAdminEntry struct {
	admin  bool
	expire time.Time
}

// adExempt 报告该发送者是否跳过判定。
//
// 顺序按成本排：三个纯内存判断在前，唯一要发 API 的群管理员判断在最后，
// 且带 10 分钟缓存。
func adExempt(b *core.Bot, snap *store.Snapshot, chatID int64, u *tg.TGUser) bool {
	if u == nil {
		return true
	}
	// 关联频道自动转发（777000）：能往关联频道发帖的只有频道方，判它等于
	// 判频道自己的帖子。匿名管理员就是管理员本人，打开「判定成员 bot」后也不判。
	if u.ID == tgServiceUID || u.ID == groupAnonymousBotID {
		return true
	}
	// 成员 bot 默认豁免：群里的工具 bot（RSS、签到）常发链接，判了会误删。
	// 开启 antiad_judge_bots 后只豁免管理员 bot 与白名单——另一个反广告
	// bot 通常是管理员，判它的告警会互相删来删去。
	// 访客 bot 与频道身份不走这里：入口处已把发送者换成了召唤者/频道。
	if u.IsBot && snap.BotSettingInt(b.BotID(), "antiad_judge_bots", 0) != 1 {
		return true
	}
	// 主管理员与本 bot 的归属人豁免：他们要能在群里说话而不被自己部署
	// 的东西拦下。**其他**次级管理员不豁免 —— 他管的是自己的群，
	// 在别人的群里他就是个普通成员。
	if b.IsMain(u.ID) || u.ID == b.Owner() {
		return true
	}
	if slices.Contains(snap.BotSettingInt64List(b.BotID(), "antiad_exempt_users"), u.ID) {
		return true
	}
	// 按群白名单要一次本地读库，仍排在唯一要发 TG API 的群管理员判断之前。
	if isGroupWhitelisted(b, chatID, u.ID) {
		return true
	}
	// 申诉解禁 / /white 产生的白名单：纯内存判断，同样排在群管理员之前。
	if snap.Whitelisted(b.BotID(), chatID, u.ID, time.Now().Unix()) {
		return true
	}
	return IsChatAdmin(b, chatID, u.ID)
}

// isChatAdmin 查此人是否为该群的管理员或群主，结果缓存 10 分钟。
// 查询失败按「不是管理员」处理：API 故障不得放大权限。
func IsChatAdmin(b *core.Bot, chatID, uid int64) bool {
	key := fmt.Sprintf("%d:%d", chatID, uid)
	if v, ok := b.ChatAdminCache.Load(key); ok {
		e := v.(chatAdminEntry)
		if time.Now().Before(e.expire) {
			return e.admin
		}
	}

	admin, ok := queryChatAdmin(b, chatID, uid)
	if !ok {
		return false
	}
	b.ChatAdminCache.Store(key, chatAdminEntry{
		admin: admin, expire: time.Now().Add(chatAdminTTL)})
	return admin
}

// queryChatAdmin 向 TG 查一次，ok 为假表示没查成（已记日志，不缓存）。
func queryChatAdmin(b *core.Bot, chatID, uid int64) (admin, ok bool) {
	if uid < 0 {
		// 频道身份：频道当不了群管理员，与之对等的是本群的关联频道——
		// 讨论群里能以它的身份发言的只有群主一方。
		raw, err := b.TG.Call("getChat", map[string]any{"chat_id": chatID})
		var resp tg.ChatFullResp
		if err != nil || json.Unmarshal(raw, &resp) != nil || !resp.OK {
			slog.Warn("反广告：查询关联频道失败，按普通频道处理",
				"chat", chatID, "sender_chat", uid, "err", err)
			return false, false
		}
		return resp.Result.LinkedChatID == uid, true
	}

	raw, err := b.TG.Call("getChatMember", map[string]any{
		"chat_id": chatID, "user_id": uid,
	})
	if err != nil {
		slog.Warn("反广告：查询群管理员失败，按普通成员处理",
			"chat", chatID, "uid", uid, "err", err)
		return false, false
	}
	var resp tg.ChatMemberResp
	if json.Unmarshal(raw, &resp) != nil || !resp.OK {
		slog.Warn("反广告：查询群管理员返回异常，按普通成员处理",
			"chat", chatID, "uid", uid, "resp", string(raw))
		return false, false
	}
	return resp.Result.Status == "administrator" || resp.Result.Status == "creator", true
}

// bioTTL 是个人简介的缓存时长。简介本身很少变，但广告号会在被处置后
// 改简介换马甲，所以不做成进程级永久缓存。
const bioTTL = time.Hour

type bioEntry struct {
	bio    string
	expire time.Time
}

// userBio 取此人的 Telegram 个人简介，带缓存。
//
// 广告号的强特征常常不在消息里而在账号本身：简介写着联系方式、价目、
// 引流话术。这个字段只有 getChat 给得到，而全量送检下不缓存就等于
// 每条群消息多一次 TG 往返，必然撞上速率限制。
//
// 查询失败返回空串：与群管理员查询同向，TG 故障不得让判定链路停摆。
func userBio(b *core.Bot, uid int64) string {
	if v, ok := b.BioCache.Load(uid); ok {
		e := v.(bioEntry)
		if time.Now().Before(e.expire) {
			return e.bio
		}
	}

	raw, err := b.TG.Call("getChat", map[string]any{"chat_id": uid})
	if err != nil {
		slog.Warn("反广告：查询个人简介失败", "uid", uid, "err", err)
		return ""
	}
	var resp tg.ChatFullResp
	if json.Unmarshal(raw, &resp) != nil || !resp.OK {
		// 对方从未与 bot 私聊过时 TG 也会拒答，这是常态而非故障，
		// 所以按空简介缓存下来，避免每条消息都重试一次。
		b.BioCache.Store(uid, bioEntry{expire: time.Now().Add(bioTTL)})
		return ""
	}
	bio := resp.Result.Bio
	if uid < 0 {
		bio = resp.Result.Description // 频道身份：频道简介
	}
	b.BioCache.Store(uid, bioEntry{bio: bio, expire: time.Now().Add(bioTTL)})
	return bio
}

// gcBioCache 清理过期条目，防止 map 无限增长。
func GCBioCache(sh *core.Shared) {
	now := time.Now()
	sh.BioCache.Range(func(k, v any) bool {
		if now.After(v.(bioEntry).expire) {
			sh.BioCache.Delete(k)
		}
		return true
	})
}

// gcChatAdminCache 清理过期条目，防止 map 无限增长。
func GCChatAdminCache(sh *core.Shared) {
	now := time.Now()
	sh.ChatAdminCache.Range(func(k, v any) bool {
		if now.After(v.(chatAdminEntry).expire) {
			sh.ChatAdminCache.Delete(k)
		}
		return true
	})
}

// handleMyChatMemberUpdate 监控 bot 自身在群里的状态。
//
// 反广告有一个代码解决不了的前提：bot 必须是群管理员，否则 Telegram
// 的 privacy mode 会让它只收得到命令消息，整条链路静默失效。被加进群
// 却没给权限是最常见的部署事故，这里主动告警，不让它无声无息。
func HandleMyChatMemberUpdate(b *core.Bot, cu *tg.ChatMemberUpdated) {
	if cu == nil || cu.Chat == nil || cu.NewChatMember == nil {
		return
	}
	if cu.Chat.Type == "private" {
		return
	}
	status := cu.NewChatMember.Status
	if !isMemberStatus(status) {
		return
	}
	if status == "administrator" || status == "creator" {
		return
	}
	// 只看总开关，不要求该群已在白名单里。部署顺序是「先把 bot 加进群
	// → 再在面板添加 chat_id」，用 antiadActive 把关的话，进群那一刻
	// 白名单里还没有它，这条告警就永远发不出去 —— 只剩「已配置的群里
	// bot 被降级」一种情况会响，而那恰恰不是最常见的事故。
	// 总开关仍要判：关着时 bot 可能还在一堆无关群里，每进一个群都私聊是纯噪音。
	if b.Cache.Snap().SettingInt("antiad_enabled", 0) != 1 {
		return
	}
	title := cu.Chat.Title
	if title == "" {
		title = "（无标题）"
	}
	msg := fmt.Sprintf("⚠️ <b>反广告无法在该群工作</b>\n\n群组: %s (<code>%d</code>)\n"+
		"当前身份: %s\n\n请把 bot 设为群管理员并授予<b>删除消息</b>与<b>封禁用户</b>权限，"+
		"否则它收不到群内普通消息。",
		html.EscapeString(title), cu.Chat.ID, status)
	for _, admin := range b.AlertTargets() {
		b.Send(admin, msg, nil)
	}
}

// msgText 返回参与判定的正文：本人正文（为空时取图片/视频配文），再加上
// 消息附带的非文字载荷，每种一行、带方括号前缀。
//
// 只看 Text 会把「图片 + 配文」这一整类漏光；只看 Text/Caption 又会把
// 联系人卡片、投票、文件名、藏在「点这里」背后的链接、内联按钮漏光——
// 它们在「无正文」守门处被直接放过，而卡片的名字与号码本身就是广告。
func msgText(m *tg.Message) string {
	var parts []string
	if m.Text != "" {
		parts = append(parts, m.Text)
	} else if m.Caption != "" {
		parts = append(parts, m.Caption)
	}
	if m.Vision != "" {
		parts = append(parts, m.Vision)
	}
	// 分两次遍历而不是 append 拼接两个切片：后者可能写进 Entities 的
	// 备用容量，与并发读同一条消息的人互相踩。
	for _, es := range [][]tg.MessageEntity{m.Entities, m.CaptionEntities} {
		for _, e := range es {
			if e.Type == "text_link" && e.URL != "" {
				parts = append(parts, "［隐藏链接］"+e.URL)
			}
		}
	}
	parts = append(parts, payloadLines(&m.MsgPayload)...)
	if m.ReplyMarkup != nil {
		for _, row := range m.ReplyMarkup.InlineKeyboard {
			for _, btn := range row {
				parts = append(parts, joinNonEmpty("［按钮］", btn.Text, btn.URL))
			}
		}
	}
	if m.ViaBot != nil && m.ViaBot.Username != "" {
		parts = append(parts, "［经由］@"+m.ViaBot.Username)
	}
	if m.GuestBot != nil {
		parts = append(parts, "［访客 bot］@"+m.GuestBot.Username)
	}
	if src := forwardSource(m.ForwardOrigin); src != "" {
		parts = append(parts, "［转发自］"+src)
	}
	return strings.Join(parts, "\n")
}

// payloadLines 把非文字载荷渲染成带类型前缀的文字，每种一行。
// 前缀让模型分得清哪段是卡片、哪段是文件名，而不是本人说的话。
func payloadLines(p *tg.MsgPayload) []string {
	var out []string
	add := func(prefix string, fields ...string) {
		if s := joinNonEmpty(prefix, fields...); s != prefix {
			out = append(out, s)
		}
	}
	if c := p.Contact; c != nil {
		add("［联系人卡片］", strings.TrimSpace(c.FirstName+" "+c.LastName), c.PhoneNumber)
	}
	if v := p.Poll; v != nil {
		f := []string{v.Question}
		for _, o := range v.Options {
			f = append(f, o.Text)
		}
		add("［投票］", f...)
	}
	if v := p.Venue; v != nil {
		add("［地点］", v.Title, v.Address)
	}
	if v := p.Game; v != nil {
		add("［游戏］", v.Title, v.Description)
	}
	if v := p.Invoice; v != nil {
		add("［账单］", v.Title, v.Description)
	}
	if v := p.Checklist; v != nil {
		f := []string{v.Title}
		for _, t := range v.Tasks {
			f = append(f, t.Text)
		}
		add("［清单］", f...)
	}
	if v := p.Audio; v != nil {
		add("［音频］", v.Title, v.Performer, v.FileName)
	}
	for _, f := range []*tg.FileNamed{p.Document, p.Video, p.Animation} {
		if f != nil {
			add("［文件］", f.FileName)
		}
	}
	if v := p.Story; v != nil && v.Chat != nil {
		add("［故事］", chatLabel(v.Chat.Title, v.Chat.Username))
	}
	if v := p.LinkPreviewOptions; v != nil {
		add("［预览］", v.URL)
	}
	return out
}

// joinNonEmpty 以空格拼接非空字段并加上前缀；全空时只返回前缀。
func joinNonEmpty(prefix string, fields ...string) string {
	var f []string
	for _, s := range fields {
		if s = strings.TrimSpace(s); s != "" {
			f = append(f, s)
		}
	}
	return prefix + strings.Join(f, " ")
}

func chatLabel(title, username string) string {
	if username != "" {
		return strings.TrimSpace(title + " @" + username)
	}
	return title
}

// forwardSource 取转发来源的名字。从引流频道整条转发进群是常见形态，
// 来源名本身就是信号（「日赚频道」），只标 is_forwarded 模型看不到它。
func forwardSource(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var o struct {
		SenderUser     *tg.TGUser `json:"sender_user"`
		SenderUserName string     `json:"sender_user_name"`
		SenderChat     *tg.Chat   `json:"sender_chat"`
		Chat           *tg.Chat   `json:"chat"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return ""
	}
	switch {
	case o.SenderUser != nil:
		return chatLabel(strings.TrimSpace(o.SenderUser.FirstName+" "+o.SenderUser.LastName),
			o.SenderUser.Username)
	case o.SenderUserName != "":
		return o.SenderUserName
	case o.Chat != nil:
		return chatLabel(o.Chat.Title, o.Chat.Username)
	case o.SenderChat != nil:
		return chatLabel(o.SenderChat.Title, o.SenderChat.Username)
	}
	return ""
}

// displayText 是「这条消息在群里实际可见的全部文字」：本人正文加上
// 被引用的内容。
//
// 判空、去重、上下文、落库、告警五处共用它，因为引用规避会把本人正文
// 压到空或一两个字符：
//   - 只看本人正文判空 → 整条漏判
//   - 只拿本人正文做去重 → 所有空正文消息哈希成同一个 key，
//     第一条之后全被当成重复吞掉
//   - 只拿本人正文落库/告警 → 管理员和形态总结看到的都是空白
//
// 送检载荷不用它：那里 message.text 与 quoted 必须分开，保住归属。
func displayText(m *tg.Message) string {
	t := msgText(m)
	q := quotedInfo(m)
	if q == nil {
		return t
	}
	if t == "" {
		return "［引用］" + q.Text
	}
	return t + "\n［引用］" + q.Text
}

// senderProfile 是喂给 AI 的发送者画像。
// 字段名即 JSON 键名，模型直接读它做判断，改名要同步改提示词。
type senderProfile struct {
	UserID    int64  `json:"user_id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	// Bio 是 TG 个人简介。广告号常把联系方式与价目写在这里，
	// 本条消息看起来再无害也该据此提高可疑度。
	Bio string `json:"bio"`
	// BioLinks 是昵称与简介里挂的公开频道/群组/bot 查出来的样子（见 enrichSender）。
	BioLinks []linkInfo `json:"bio_links,omitempty"`
	// IsChannel 表示以频道身份发言：first_name 是频道名，bio 是频道简介。
	IsChannel   bool  `json:"is_channel,omitempty"`
	IsPremium   bool  `json:"is_premium"`
	AgeHours    int64 `json:"age_hours"`
	AgeKnown    bool  `json:"age_known"`
	MsgsInGroup int64 `json:"msgs_in_group"`
	PriorAdHits int64 `json:"prior_ad_hits"`
}

type adMessageInfo struct {
	Text        string `json:"text"`
	HasLink     bool   `json:"has_link"`
	HasMention  bool   `json:"has_mention"`
	HasMedia    bool   `json:"has_media"`
	IsForwarded bool   `json:"is_forwarded"`
	// IsEdited 表示这是发出后又编辑过的版本。
	IsEdited bool `json:"is_edited,omitempty"`
	Length   int  `json:"length"`
	// MentionedBots 是正文里 @ 到的 bot，见 mentionedBots。
	MentionedBots []string `json:"mentioned_bots,omitempty"`
}

type adChatInfo struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

// adQuotedInfo 是本人引用/回复的那段内容。
//
// 单列而不并进 message.text：归属必须分清。那段话是被引用者写的，
// 混进本人正文会让「转述并批评广告」的人也被判成广告。
type adQuotedInfo struct {
	Text string `json:"text"`
	From string `json:"from"`
	// IsExternal 表示引用的是其它聊天里的消息（频道引用属于这类）。
	// 这本身就是信号：把外部频道的推广内容搬进群，比回复群友更可疑。
	IsExternal bool `json:"is_external"`
	Length     int  `json:"length"`
}

// quotedInfo 取出被引用内容。没有引用则返回 nil ——
// 空的 quoted 块会给模型一个「有引用但内容为空」的假信号。
//
// 取值优先级：手动选中的引文 > 跨聊天引用摘要 > 同群原消息。
// 手动引文优先，是因为它才是群里实际显示、其他成员看得到的那一段。
func quotedInfo(m *tg.Message) *adQuotedInfo {
	q := &adQuotedInfo{}

	if m.ExternalReply != nil {
		q.IsExternal = true
		q.Text = m.ExternalReply.Text
		if q.Text == "" {
			q.Text = m.ExternalReply.Caption
		}
		// 频道引用同样可能是一张联系人卡片或一个投票。
		if extra := payloadLines(&m.ExternalReply.MsgPayload); len(extra) > 0 {
			q.Text = strings.TrimSpace(q.Text + "\n" + strings.Join(extra, "\n"))
		}
		if c := m.ExternalReply.Chat; c != nil {
			q.From = c.Title
		}
	} else if r := m.ReplyToMessage; r != nil {
		q.Text = msgText(r)
		q.From = senderName(r.From)
		if r.From == nil {
			q.From = ""
		}
	}

	if m.Quote != nil && m.Quote.Text != "" {
		q.Text = m.Quote.Text
	}
	if q.Text == "" {
		return nil
	}
	q.Length = len([]rune(q.Text))
	// 与本人正文同一套上限：引用内容同样完全由攻击者控制。
	q.Text = core.TruncateRunes(q.Text, adStateTextLimit)
	return q
}

// adState 是 systemone 的 state，也是大模型复判的输入。
// 两级判定共用同一份，保证它们看到的信息完全一致。
type adState struct {
	Chat          adChatInfo    `json:"chat"`
	Message       adMessageInfo `json:"message"`
	Sender        senderProfile `json:"sender"`
	RecentContext []core.CtxMsg `json:"recent_context"`
	// Quoted 是本人引用/回复的内容，无引用时为 nil。
	Quoted *adQuotedInfo `json:"quoted,omitempty"`
	// ReviewHistory 只在 /ad 复查时非空：该用户在本群的全部留底，
	// 一次性交给模型。非空即代表「这是对账号的整体复查」而非对单条
	// 消息的判定，两级提示词都据此切换判断口径。
	ReviewHistory []core.CtxMsg `json:"review_history,omitempty"`
	// JoinCheck 为真表示这是**进群冷判定**：此人一条消息都还没发过，
	// message 整块是空的，全部证据在 sender 里。提示词据此切换口径，
	// 不加这个标记的话模型会把「正文为空」当成规避形态而误判。
	JoinCheck           bool   `json:"join_check,omitempty"`
	KnownAdPatterns     string `json:"known_ad_patterns"`
	KnownFalsePositives string `json:"known_false_positives"`
}

// buildProfile 组装发送者画像。
func buildProfile(b *core.Bot, m *tg.Message, gm groupMember, now int64) senderProfile {
	p := senderProfile{PriorAdHits: gm.AdHits, MsgsInGroup: gm.MsgCount}
	if m.From != nil {
		p.UserID = m.From.ID
		p.Username = m.From.Username
		p.FirstName = m.From.FirstName
		p.LastName = m.From.LastName
		p.IsPremium = m.From.IsPremium
		p.IsChannel = m.From.ID < 0 // 见 senderOf
	}

	// joined_at 缺失（bot 部署前此人已在群）时退回 first_seen，
	// 并如实告知模型年龄未知——否则它会把一个老群员当成刚进来的。
	base := gm.JoinedAt
	p.AgeKnown = base > 0
	if !p.AgeKnown {
		base = gm.FirstSeen
	}
	if base > 0 && now > base {
		p.AgeHours = (now - base) / 3600
	}
	return p
}

// buildState 组装送检载荷。recent_context 取自本人留底并排除当前这条，
// 所以当前消息先留底再调用它是安全的（HandleGroupMessage 正是如此）。
func buildState(b *core.Bot, snap *store.Snapshot, m *tg.Message, p senderProfile) adState {
	text := msgText(m)
	st := adState{
		Message: adMessageInfo{
			Text:        core.TruncateRunes(text, adStateTextLimit),
			HasMedia:    m.Text == "" && m.Caption != "",
			IsForwarded: len(m.ForwardOrigin) > 0,
			IsEdited:    m.EditDate != 0,
			// 长度报原文的，不报截断后的：长度本身是判定信号
			// （刷屏、超长广告文案），截断载荷不该把它一起抹掉。
			Length:        len([]rune(text)),
			MentionedBots: mentionedBots(text),
		},
		Sender: p,
		Quoted: quotedInfo(m),
	}
	if m.Chat != nil {
		st.Chat = adChatInfo{ID: m.Chat.ID, Title: m.Chat.Title}
		st.RecentContext = recentOwn(b.Store, m,
			int(snap.BotSettingInt(b.BotID(), "antiad_ctx_msgs", 6)))
	}

	// 显性特征由 TG 的 entities 直接给出，结构化标注比让模型
	// 自己从正文里数链接可靠得多。
	ents := m.Entities
	if len(ents) == 0 {
		ents = m.CaptionEntities
	}
	for _, e := range ents {
		switch e.Type {
		case "url", "text_link":
			st.Message.HasLink = true
		case "mention", "text_mention":
			st.Message.HasMention = true
		}
	}

	st.KnownAdPatterns, st.KnownFalsePositives = splitDigest(snap.Setting("antiad_digest"))
	return st
}

// ctxTextLimit 是单条上下文的字符上限。上下文用来让模型看到此人近来
// 说过什么，不需要全文；不截断的话一条长消息就能把每次判定的 input
// token 撑爆。
const ctxTextLimit = 200

// recentOwn 取发送者本人在本群最近 n 条留底（不含当前这条），由旧到新。
//
// 只取本人的：别人的发言（尤其是带「［引用］」载荷的广告）混进来，模型
// 会把它当成本条消息引用的内容——线上真实误判过。从留底取而不是另开
// 一个内存环：环按群共享、容量有限，活跃群里本人的上一条早被挤出去了；
// 留底按人建了索引，重启也不丢。
func recentOwn(s *store.Store, m *tg.Message, n int) []core.CtxMsg {
	if n <= 0 || m.From == nil {
		return nil
	}
	var out []core.CtxMsg
	// 多取一条：当前这条通常已经留底，过滤掉之后仍有 n 条。
	for _, h := range loadUserMessages(s, m.Chat.ID, m.From.ID, n+1) {
		if h.MessageID == m.MessageID {
			continue
		}
		out = append(out, core.CtxMsg{Name: senderName(m.From),
			Text: core.TruncateRunes(h.Text, ctxTextLimit), At: h.At})
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

const (
	DigestAdHeader = "【近期广告形态】"
	DigestFPHeader = "【易误杀的正常形态】"

	// 摘要样本的围栏。样本是攻击者完全控制的群消息原文，必须明确
	// 标成「数据区」，且 system 提示词里要声明围栏内不得当指令执行。
	digestFenceOpen  = "<<<"
	digestFenceClose = ">>>"
)

// adStateTextLimit 是送检正文的字符上限。这是整条链路上唯一由攻击者
// 自由控制长度的字段（上下文/日志/告警/样本/摘要都各有上限），而广告的
// 判定特征从来不在第 2000 个字之后。
const adStateTextLimit = 2000

// splitDigest 把形态摘要切成正例与反例两段。
// 标题缺失（管理员手工改乱了）时整段当作广告形态，
// 不因为格式问题丢掉全部学习成果。
func splitDigest(d string) (patterns, falsePositives string) {
	d = strings.TrimSpace(d)
	if d == "" {
		return "", ""
	}
	body := strings.TrimPrefix(d, DigestAdHeader)
	idx := strings.Index(body, DigestFPHeader)
	if idx < 0 {
		return strings.TrimSpace(body), ""
	}
	return strings.TrimSpace(body[:idx]),
		strings.TrimSpace(body[idx+len(DigestFPHeader):])
}

// isNewbie 判定是否按新人档处置。两个条件任一命中即为新人：
// 刚进群，或进群久了但几乎没发过言（潜伏号）。
//
// 两条界限都可以按 bot 覆盖：技术群和闲聊群对「新人」的合理定义不一样，
// 而这两个群往往归不同的人管。
func isNewbie(b *core.Bot, snap *store.Snapshot, p senderProfile) bool {
	id := b.BotID()
	// 年龄轴只在进群时间确实已知时参与判定。AgeKnown 为假时 AgeHours 退回
	// first_seen，而 first_seen 是「bot 第一次见到这个人发言」——上线首日
	// 全群元老的 first_seen 都约等于此刻，采信它会把待了五年的人一起打成新人，
	// 正好是「老成员低风险」的反面。这也与提示词里
	// 「age_known 为 false 时按普通成员对待，不要因此加重怀疑」保持同向。
	if p.AgeKnown && p.AgeHours < snap.BotSettingInt(id, "antiad_new_hours", 72) {
		return true
	}
	// 发言数轴不受年龄是否已知影响：它本身就是资历的独立证据。
	return p.MsgsInGroup < snap.BotSettingInt(id, "antiad_new_msgs", 10)
}

// senderName 取一个供上下文展示的名字，优先 username。
func senderName(u *tg.TGUser) string {
	if u == nil {
		return "?"
	}
	if u.Username != "" {
		return "@" + u.Username
	}
	if u.FirstName != "" {
		return u.FirstName
	}
	return fmt.Sprint(u.ID)
}

// ---- 处置矩阵 + 演练模式 + 流水落库 ----

// adTextLimit 是 antiad_log 里保存的原文字符上限。
// 一条几千字的广告不该把库撑大，管理员也读不完。
const adTextLimit = 1000

// decideAction 按「用户风险档 × 置信度」决定处置强度。
//
//	置信度 ≥ hard(90%)      新人: 删 + 禁言 + 告警    老人: 删 + 告警
//	soft(75%) ≤ 置信 < hard 新人: 删 + 告警           老人: 仅告警
//	置信度 < soft           都不处置
//
// 老人永远不自动禁言：误伤一个长期成员的社交代价远大于漏一条广告，
// 告警里给管理员一键补刀的按钮就够了。
//
// 两条线都可以按 bot 覆盖，归 owner 调 —— 他最清楚自己的群该多严。
func decideAction(b *core.Bot, snap *store.Snapshot, newbie bool, v adVerdict) adAction {
	if !v.IsAd {
		return adAction{Name: "none"}
	}
	id := b.BotID()
	conf := v.Confidence * 100
	hard := float64(snap.BotSettingInt(id, "antiad_act_hard", 90))
	soft := float64(snap.BotSettingInt(id, "antiad_act_soft", 75))

	switch {
	case conf >= hard && newbie:
		// 账号本身就是广告号时连带删掉此人近期的全部消息。只在这一档：
		// 老成员误判的代价与自动禁言同理，删光了更是没法挽回。
		return adAction{Delete: true, Mute: true, Alert: true,
			Purge: v.Scope == "account", Name: "deleted_muted"}
	case conf >= hard:
		return adAction{Delete: true, Alert: true, Name: "deleted"}
	case conf >= soft && newbie:
		return adAction{Delete: true, Alert: true, Name: "deleted"}
	case conf >= soft:
		return adAction{Alert: true, Name: "alerted"}
	}
	return adAction{Name: "none"}
}

// logAd 落一条判定流水，返回自增 id（失败返回 0）。
// clean 也要记：否则算不出真实开销，形态总结也拿不到样本量基数。
func logAd(b *core.Bot, m *tg.Message, v adVerdict, action, reason string) int64 {
	verdict := "clean"
	if v.IsAd {
		verdict = "ad"
	}
	switch v.Decider {
	case "":
		verdict = "error"
	case adDeciderSkipped:
		verdict = "skipped" // 没送检，不是判定失败
	}
	note := v.Reason
	if reason != "" {
		note = strings.TrimSpace(note + " | " + reason)
	}

	res, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,prompt_tokens,completion_tokens,quota_cost,created_at,bot_id)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.Chat.ID, m.From.ID, m.MessageID,
		core.TruncateRunes(displayText(m), adTextLimit),
		verdict, v.Confidence, v.Decider, v.Kind, action, note,
		v.Usage.PromptTokens, v.Usage.CompletionTokens, v.Cost,
		time.Now().Unix(), b.BotID())
	if err != nil {
		slog.Error("反广告：流水落库失败", "chat", m.Chat.ID, "err", err)
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

// bumpAdHits 调整历史命中数。delta 为负时不低于 0。
func BumpAdHits(b *core.Bot, chatID, uid, delta int64) {
	if _, err := b.Store.Write.Exec(`UPDATE group_members
		SET ad_hits = MAX(0, ad_hits + ?) WHERE chat_id=? AND user_id=?`,
		delta, chatID, uid); err != nil {
		slog.Error("反广告：更新命中数失败", "chat", chatID, "uid", uid, "err", err)
	}
}

// sendAdAlert 把一次命中贴到群里（开了「群内展示」时）。管理员私聊不在这里发：
// 攒成汇总由 FlushAdSummary 定时发，见 summary.go。逐条私聊在刷屏高峰里会
// 刷爆私聊、撞上 TG 发送频率限制，把删除/禁言一起拖慢。
//
// 演练模式下措辞必须让人一眼看出「没有真的动手」，否则群里会以为广告已经被删了。
// note 是 ApplyAction 的失败说明（成功时为空）：告警要按「实际结果」而非
// 「意图」渲染。
func sendAdAlert(b *core.Bot, conf store.BotChat, m *tg.Message, v adVerdict, act adAction,
	note string, logID int64, dryrun bool) {

	// 默认关闭 —— 升级上来的部署不该突然在群里多出消息。
	if !conf.GroupAlert || m.Chat == nil {
		return
	}
	// 按 (群, 人) 节流：一个刷屏的人不能把群刷成告警墙，也不能撞上 TG 对
	// 单群的发送频率限制。压掉的只是这一个人的重复告警。
	// /ad 复查不走这里：它是人主动要的，有自己的命令限频。
	snap := b.Cache.Snap()
	if !b.AdLimits.Allow(fmt.Sprintf("ad:a:%d:%d", m.Chat.ID, m.From.ID),
		snap.BotSettingInt(b.BotID(), "antiad_alert_rpm", 3)) {
		slog.Info("反广告：告警被节流", "chat", m.Chat.ID, "uid", m.From.ID)
		return
	}
	// 贴在群里：处置结果对全群可见，该群的 TG 管理员能直接点按钮判断
	// （见 adDispositionAllowed）。精简版：昵称与原文一个字都不能贴回去。
	brief, bkb := renderAdAlertBrief(b, m, v, act, note, logID, dryrun)
	scheduleAlertCleanup(b, m.Chat.ID, b.SendGetID(m.Chat.ID, brief, bkb),
		time.Duration(snap.BotSettingInt(b.BotID(), "antiad_alert_ttl", 300))*time.Second)
}

// renderAdAlertBrief 渲染群内版告警。
//
// 与私聊版分开：私聊是给管理员事后复盘的，信息越全越好；群里是给在场
// 的人一眼看明白「谁、被怎么处理了」，长文只会刷屏，而且群名与
// chat_id 在群里是纯噪音——大家已经在这个群里了。
//
// 昵称与原文一个字都不贴：广告号的昵称和正文本身就是广告，bot 把它们
// 发回群里等于替它再发一遍（删除白做），还会让 bot 自己被 TG 当成广告号
// 封掉。掐头去尾也不行——首尾同样可能是载荷。
func renderAdAlertBrief(b *core.Bot, m *tg.Message, v adVerdict, act adAction,
	note string, logID int64, dryrun bool) (string, map[string]any) {

	var sb strings.Builder
	if dryrun {
		// 这个标识不能省：没有它，群里会以为广告已经被删了。
		sb.WriteString("🧪 <b>演练（未实际处置）</b>\n")
	}

	kind := adKindLabel(v.Kind)
	fmt.Fprintf(&sb, "🚫 <b>广告</b> · %.0f%% · %s\n",
		v.Confidence*100, html.EscapeString(kind))

	fmt.Fprintf(&sb, "├ 用户  %s\n", userLink(m.From.ID))
	if logID != 0 {
		// 告警撤回或翻不到时，管理员凭这个编号去拦截记录里找。
		fmt.Fprintf(&sb, "├ 记录  <code>#%d</code>\n", logID)
	}
	// 空值来自没跑 AI 的路径（人工标记），印一行空的「判定」只是噪音。
	if v.Model != "" {
		fmt.Fprintf(&sb, "├ 判定  <code>%s</code>\n",
			html.EscapeString(core.TruncateRunes(v.Model, 40)))
	}

	// 处置行是树的末枝，除非还要接一条失败说明。
	tail, label := "└", "处置"
	if dryrun {
		// 演练下写「已执行」等于在群里公开谎报。
		label = "本应"
	}
	if note != "" {
		tail = "├"
	}
	fmt.Fprintf(&sb, "%s %s  %s\n", tail, label, actionDesc(act))
	if note != "" {
		fmt.Fprintf(&sb, "└ ⚠️    %s\n", html.EscapeString(core.TruncateRunes(note, 60)))
	}

	return sb.String(), adAlertKB(act, note, logID, dryrun)
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

// scheduleAlertCleanup 安排到点撤回 bot 自己发的群内告警。
//
// 群里连着几条告警会把正常对话顶走，而告警的信息价值在处置完成后就没了。
// 撤回的只是 bot 自己那条，判定流水与处置结果都留在库里，面板的
// 「📋 拦截记录」随时能查回来。
//
// 代价要讲清楚：告警一撤，上面的处置按钮也跟着没了。所以 ttl 可以配成
// 0 表示永不撤回 —— 有人就是希望按钮一直挂在那儿。
//
// 记进 alert_cleanup，由 SweepAlertCleanup 每分钟撤一批，所以最多晚一分钟。
// 不用内存计时器：进程一重启就丢，还没到点的告警会永久留在群里。
func scheduleAlertCleanup(b *core.Bot, chatID, msgID int64, ttl time.Duration) {
	if ttl <= 0 || msgID == 0 {
		// msgID 为 0 说明没拿到发送结果。传 0 给 TG 会被解读成别的消息，
		// 宁可不撤也不能删错。
		return
	}
	if _, err := b.Store.Write.Exec(`INSERT OR REPLACE INTO alert_cleanup
		(bot_id,chat_id,message_id,due_at) VALUES (?,?,?,?)`,
		b.BotID(), chatID, msgID, time.Now().Add(ttl).Unix()); err != nil {
		slog.Error("反广告：记录待撤回告警失败", "chat", chatID, "msg", msgID, "err", err)
	}
}

// SweepAlertCleanup 撤回这个 bot 到点的群内告警。每条只试一次：撤不掉的
// （已被人删掉、超过 TG 的 48 小时删除期限）重试也没用。
func SweepAlertCleanup(b *core.Bot, now time.Time) {
	rows, err := b.Store.Read.Query(`SELECT chat_id,message_id FROM alert_cleanup
		WHERE bot_id=? AND due_at <= ? LIMIT 200`, b.BotID(), now.Unix())
	if err != nil {
		slog.Error("反广告：读取待撤回告警失败", "err", err)
		return
	}
	var due [][2]int64
	for rows.Next() {
		var c, m int64
		if rows.Scan(&c, &m) == nil {
			due = append(due, [2]int64{c, m})
		}
	}
	rows.Close()
	for _, d := range due {
		b.TG.Call("deleteMessage", map[string]any{"chat_id": d[0], "message_id": d[1]})
		b.Store.Write.Exec(`DELETE FROM alert_cleanup WHERE chat_id=? AND message_id=?`, d[0], d[1])
	}
}

// adAlertKB 构造处置按钮。私聊版与群内版共用 —— 两处各写一份的话，
// 「删失败要把按钮放回来」这类规则迟早只在一边生效。
//
// 按钮按「当前还能做什么」动态生成：已经删过的不再给删除按钮。
// callback_data 形如 a:ad:fp:12345，远在 64 字节以内。
func adAlertKB(act adAction, note string, logID int64, dryrun bool) map[string]any {
	rows := [][][2]string{{
		{"✅ 判定正确", fmt.Sprintf("a:ad:ok:%d", logID)},
		{"↩️ 误判", fmt.Sprintf("a:ad:fp:%d", logID)},
	}}
	// 失败的那一步也要把按钮放回来：act.Delete 为真只说明「打算删」，
	// 删失败时若照样隐藏按钮，唯一需要人工补刀的场景恰好没有入口。
	var manual [][2]string
	if dryrun || !act.Delete || strings.Contains(note, noteDeleteFailed) {
		manual = append(manual, [2]string{"🗑 删除", fmt.Sprintf("a:ad:del:%d", logID)})
	}
	if dryrun || (!act.Mute && !act.Ban) || strings.Contains(note, noteMuteFailed) {
		manual = append(manual, [2]string{"🔇 禁言", fmt.Sprintf("a:ad:mute:%d", logID)})
	}
	if len(manual) > 0 {
		rows = append(rows, manual)
	}
	rows = append(rows, [][2]string{
		{"🚫 封禁出群", fmt.Sprintf("a:ad:ban:%d", logID)},
	})
	return tg.InlineKB(rows...)
}

// actionDesc 把动作组合渲染成人话。
func actionDesc(act adAction) string {
	switch act.Name {
	case "deleted_muted":
		if act.Purge {
			return "删除此人近期全部消息 + 禁言"
		}
		return "删除消息 + 禁言"
	case "deleted_banned":
		if act.Purge {
			return "删除此人近期全部消息 + 封禁出群"
		}
		return "删除消息 + 封禁出群"
	case "muted":
		return "禁言"
	case "deleted":
		return "删除消息"
	case "alerted":
		return "仅告警（未删除）"
	}
	return "无动作"
}

// ---- 判定账本的读写 ----
//
// antiad_log 是本包写的账本（logAd），读回与订正也归本包：面板只是它的
// 消费者之一，把查询放在面板里会让账本的形状由展示需求决定。
// AdLogRow 是 antiad_log 的一行。
type AdLogRow struct {
	ID         int64
	BotID      int64
	ChatID     int64
	UserID     int64
	MessageID  int64
	Text       string
	Verdict    string
	Confidence float64
	Decider    string
	Kind       string
	Action     string
	Reason     string

	PromptTokens     int64
	CompletionTokens int64
	QuotaCost        int64
	CreatedAt        int64
}

func LoadAdLog(s *store.Store, id int64) (AdLogRow, bool) {
	var r AdLogRow
	err := s.Read.QueryRow(`SELECT id,bot_id,chat_id,user_id,message_id,text,verdict,
		confidence,decider,ad_kind,action,reason,prompt_tokens,completion_tokens,
		quota_cost,created_at FROM antiad_log WHERE id=?`, id).
		Scan(&r.ID, &r.BotID, &r.ChatID, &r.UserID, &r.MessageID, &r.Text, &r.Verdict,
			&r.Confidence, &r.Decider, &r.Kind, &r.Action, &r.Reason,
			&r.PromptTokens, &r.CompletionTokens, &r.QuotaCost, &r.CreatedAt)
	if err != nil {
		return r, false
	}
	return r, true
}

func UpdateAdLog(b *core.Bot, id int64, action, reason string) {
	if _, err := b.Store.Write.Exec(
		`UPDATE antiad_log SET action=?, reason=? WHERE id=?`,
		action, reason, id); err != nil {
		slog.Error("反广告：更新流水失败", "id", id, "err", err)
	}
}
