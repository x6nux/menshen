package antiad

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"html"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

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

// recordMessage 把一条群消息留底，供 /ad 事后复查。
//
// 失败只记日志不中断：留底是复查用的辅助设施，不该让一次写失败
// 把这条消息的判定也一起拖没。
func recordMessage(b *core.Bot, chatID, msgID, uid int64, text string, at int64) {
	if _, err := b.Store.Write.Exec(`INSERT INTO group_messages
		(chat_id,message_id,user_id,text,at) VALUES (?,?,?,?,?)
		ON CONFLICT(chat_id,message_id) DO NOTHING`,
		chatID, msgID, uid, core.TruncateRunes(text, gmsgTextLimit), at); err != nil {
		slog.Error("反广告：留底失败", "chat", chatID, "msg", msgID, "err", err)
	}
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
	rows, err := s.Read.Query(`SELECT message_id,text,at FROM group_messages
		WHERE chat_id=? AND user_id=? ORDER BY at DESC, message_id DESC LIMIT ?`,
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
	"或直接发 <code>/ad &lt;user_id&gt;</code>。\n" +
	"会把该用户在本群的全部留底一次性交给两个模型复查。"

// parseAdCommand 识别 /ad 与 /adb 并取出命令名与参数。
//
// 群里 TG 客户端会自动补成 /ad@botname，必须一并认。
// 两条命令合并识别，因为 /adb 以 /ad 为前缀 —— 分开写的话，
// 先匹配 /ad 的那一方会把 /adb 也吃掉。
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
	case "/ad", "/adb":
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
		target = m.ReplyToMessage.From
		targetMsgID = m.ReplyToMessage.MessageID
	case arg != "":
		uid, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
		if err != nil || uid <= 0 {
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

	select {
	case b.AdSem <- struct{}{}:
	default:
		b.Send(m.Chat.ID, "判定通道繁忙，请稍后再试。", nil)
		return
	}
	go func() {
		defer func() { <-b.AdSem }()
		reviewAndAct(b, snap, conf, tgt, profile, state)
	}()
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
	target := m.ReplyToMessage
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
	act := adAction{Delete: true, Mute: true, Alert: true, Name: "deleted_muted"}

	action, note := ApplyAction(b, target, act, conf.Dryrun)
	logID := logAd(b, target, v, action, note)

	if !conf.Dryrun {
		BumpAdHits(b, conf.ChatID, target.From.ID, 1)
		maybeGban(b, conf.ChatID, target.From.ID,
			"人工标记："+core.TruncateRunes(displayText(target), 60))
	}

	gm, _ := loadMember(b.Store, conf.ChatID, target.From.ID)
	p := buildProfile(b, target, gm, time.Now().Unix())
	text, kb := renderAdAlertBrief(b, target, v, act, note, p, logID, conf.Dryrun)
	scheduleAlertCleanup(b, conf.ChatID,
		b.SendGetID(conf.ChatID, "🖐 <b>人工标记</b>\n"+text, kb),
		time.Duration(snap.BotSettingInt(b.BotID(), "antiad_alert_ttl", 300))*time.Second)
}

// reviewAndAct 跑复查并按处置矩阵动作，结果贴回群里。
func reviewAndAct(b *core.Bot, snap *store.Snapshot, conf store.BotChat, tgt *tg.Message,
	profile senderProfile, state adState) {

	chatID := conf.ChatID
	state.Sender.Bio = userBio(b, state.Sender.UserID)

	v, err := judgeBoth(b, snap, state)
	if err != nil {
		slog.Warn("反广告：复查失败", "chat", chatID, "uid", tgt.From.ID, "err", err)
		b.Send(chatID, "复查失败："+html.EscapeString(core.TruncateRunes(err.Error(), 200)), nil)
		return
	}

	dryrun := conf.Dryrun
	act := decideAction(b, snap, isNewbie(b, snap, profile), v)
	if tgt.MessageID == 0 {
		// /ad <user_id> 没有指向具体消息，删不了任何东西。
		// 不摘掉的话 deleteMessage 必然失败，告警里还会多一条假的失败说明。
		act.Delete = false
	}
	action, note := ApplyAction(b, tgt, act, dryrun)
	logID := logAd(b, tgt, v, action, note)
	if act.Name != "none" && !dryrun {
		BumpAdHits(b, chatID, tgt.From.ID, 1)
		if act.Mute {
			maybeGban(b, chatID, tgt.From.ID, "复查判定："+core.TruncateRunes(v.Reason, 80))
		}
	}

	// 与自动告警的群内版用同一份渲染：同一个渠道不该有两种长度，
	// 也同样不把广告原文整段贴回群里。
	text, kb := renderAdAlertBrief(b, tgt, v, act, note, profile, logID, dryrun)
	scheduleAlertCleanup(b, chatID, b.SendGetID(chatID, "🔎 <b>复查结果</b>\n"+text, kb),
		time.Duration(snap.BotSettingInt(b.BotID(), "antiad_alert_ttl", 300))*time.Second)
}

// handleGroupMessage 是群消息的总入口。
func HandleGroupMessage(b *core.Bot, m *tg.Message) {
	if m.Chat == nil || m.From == nil {
		return
	}
	conf, active := chatActive(b, m.Chat.ID)
	if !active {
		return
	}

	at := m.Date
	if at == 0 {
		at = time.Now().Unix()
	}

	// service 消息兜底：chat_member 在 bot 权限变动期间可能漏收，
	// 而「谁刚进群」是整个分档的基础，两条路都要接。
	if len(m.NewChatMembers) > 0 {
		for _, nu := range m.NewChatMembers {
			if nu != nil {
				onJoin(b, conf, nu, at)
			}
		}
		return // 入群 service 消息本身没有正文，不进判定
	}

	// 资历累计必须先于「无正文就早退」。纯图/贴纸同样是在群里活动的证据，
	// 不计的话这类成员 msg_count 永不增长，在 isNewbie 的发言数轴上
	// 永远算新人；而年龄轴在 joined_at 缺失时本就不参与判定，
	// 两条轴一起失效就等于「老成员低风险」对他们完全不成立。
	// 代价是每条贴纸多一次 UPSERT，与一次 AI 调用相比可以忽略。
	gm := touchMember(b, m.Chat.ID, m.From.ID, at)

	// 判空看的是「本人正文 + 引用内容」的合集，不能只看本人正文：
	// 规避形态的极限版是一个字都不发（只发贴纸），载荷全在引用块里，
	// 只看正文的话这条守门恰好把它放过去。
	text := displayText(m)
	if text == "" {
		return // 纯图/贴纸且无引用：本期不判定
	}

	// 两条命令都不是群聊内容：不留底（留了会被送进下一次复查），
	// 也不进判定。放在留底之前正是为此。
	if cmd, arg, ok := parseAdCommand(text); ok {
		switch cmd {
		case "/ad":
			HandleAdCommand(b, conf, m, arg)
		case "/adb":
			HandleAdbCommand(b, conf, m)
		}
		return
	}

	// 留底先于一切判定分支。豁免者、被护栏拦下的、判定失败的都要留：
	// /ad 复查时需要的恰恰是这些「没被判过」的消息。
	recordMessage(b, m.Chat.ID, m.MessageID, m.From.ID, text, at)

	snap := b.Cache.Snap()

	if adExempt(b, snap, m.Chat.ID, m.From) {
		// 豁免者的发言仍要进上下文：模型需要看到完整的群内对话，
		// 否则「管理员刚说了别发广告」这种关键语境会丢失。
		b.AdCtx.Push(m.Chat.ID, senderName(m.From), text, at)
		return
	}

	profile := buildProfile(b, m, gm, at)
	state := buildState(b, snap, m, profile)
	// 入环必须留在同步段，且必须在 buildState 之后：更新是按到达顺序
	// 串行处理的（轮询单 goroutine / webhook 单 worker），挪进 goroutine
	// 后入环顺序会随调度乱掉，模型就会在 recent_context 里看到乱序的
	// 对话，甚至看到自己正要判的那一条。
	b.AdCtx.Push(m.Chat.ID, senderName(m.From), text, at)

	if ok, why := adAllow(b, snap, m.Chat.ID, text); !ok {
		// 护栏拦下的同样按放行处理，只是不花这次 AI 的钱。
		slog.Info("反广告：护栏拦下，未送检", "chat", m.Chat.ID,
			"uid", m.From.ID, "why", why)
		return
	}

	// 判定要发 1~2 次 AI 请求（每次 20 秒超时，最坏 80 秒），而更新处理
	// 是串行的。同步等在这里，上游一慢整个 bot 就停摆——管理员连
	// 「关闭反广告」都点不动，而上游抖动恰恰是最需要关掉它的时刻。
	select {
	case b.AdSem <- struct{}{}:
	default:
		// 并发已满就丢弃这一条。与超时放行是同一个失败方向：宁可漏判，
		// 也不能让判定链路反过来拖垮 bot 本身。
		slog.Warn("反广告：并发已满，本条放行", "chat", m.Chat.ID, "uid", m.From.ID)
		logAd(b, m, adVerdict{Reason: "并发已满"}, "none", "并发已满，未送检")
		return
	}
	go func() {
		defer func() { <-b.AdSem }()
		judgeAndAct(b, snap, conf, m, profile, state)
	}()
}

// judgeAndAct 是判定与处置，跑在独立 goroutine 上。
//
// 进程退出时不等待在飞的判定：它们最长 80 秒，且并发有上限，全部只会
// 写自己的流水。store 关闭后写操作返回错误而非 panic（database/sql
// 的既有行为），最坏结果是日志里多几条写失败——用一整套等待机制换
// 这个，不划算。
func judgeAndAct(b *core.Bot, snap *store.Snapshot, conf store.BotChat, m *tg.Message,
	profile senderProfile, state adState) {

	// 个人简介要发一次 getChat，所以放在异步段取：同步段每多一次 TG
	// 往返，更新处理就多停一次。
	state.Sender.Bio = userBio(b, state.Sender.UserID)

	v, err := judge(b, snap, state)
	if err != nil {
		// 失败一律放行。反向会在上游抖动时清空整个群聊。
		slog.Warn("反广告：判定失败，放行", "chat", m.Chat.ID,
			"uid", m.From.ID, "err", err)
		logAd(b, m, adVerdict{Reason: err.Error()}, "none", "判定失败")
		return
	}

	// 演练是**每个群**各自的状态：新加的群先观察、老群已转正式，
	// 是最常见的形态。
	dryrun := conf.Dryrun
	act := decideAction(b, snap, isNewbie(b, snap, profile), v)
	action, note := ApplyAction(b, m, act, dryrun)
	logID := logAd(b, m, v, action, note)

	// 演练期的判定不该污染真实画像：切回正式模式后，
	// 这些人的 prior_ad_hits 应该还是干净的。
	if act.Name != "none" && !dryrun {
		BumpAdHits(b, m.Chat.ID, m.From.ID, 1)
		// 联合封禁只认最高档：删+禁言那一档意味着置信度过了 hard 线，
		// 而联合封禁会把人从所有接入群一起请出去，证据不足不能动。
		if act.Mute {
			maybeGban(b, m.Chat.ID, m.From.ID, "自动判定："+core.TruncateRunes(v.Reason, 80))
		}
	}
	if act.Alert {
		sendAdAlert(b, conf, m, v, act, note, profile, logID, dryrun)
	}
}

// adAllow 是送检前的滥用护栏，返回 false 表示这条不送检。
//
// 用户明知成本地选择了全量送检，所以这里不做抽样、不关送检，只堵滥用：
// 任何普通群成员都能靠刷屏把钱烧掉，这与「按设计每条都判」是两回事。
func adAllow(b *core.Bot, snap *store.Snapshot, chatID int64, text string) (bool, string) {
	// 去重必须排在上限之前：重复刷屏不该白白吃掉本群的送检额度，
	// 否则攻击者用一条文案就能把正常消息挤出判定。
	h := fnv.New64a()
	h.Write([]byte(text))
	if !b.AdLimits.Allow(fmt.Sprintf("ad:d:%d:%x", chatID, h.Sum64()), 1) {
		return false, "窗口内重复文本"
	}
	if !b.AdLimits.Allow(fmt.Sprintf("ad:c:%d", chatID),
		snap.BotSettingInt(b.BotID(), "antiad_rpm_chat", 30)) {
		return false, "超出本群送检频率上限"
	}
	return true, ""
}

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
	if u.IsBot {
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

	raw, err := b.TG.Call("getChatMember", map[string]any{
		"chat_id": chatID, "user_id": uid,
	})
	if err != nil {
		slog.Warn("反广告：查询群管理员失败，按普通成员处理",
			"chat", chatID, "uid", uid, "err", err)
		return false
	}
	var resp tg.ChatMemberResp
	if json.Unmarshal(raw, &resp) != nil || !resp.OK {
		slog.Warn("反广告：查询群管理员返回异常，按普通成员处理",
			"chat", chatID, "uid", uid, "resp", string(raw))
		return false
	}

	admin := resp.Result.Status == "administrator" || resp.Result.Status == "creator"
	b.ChatAdminCache.Store(key, chatAdminEntry{
		admin: admin, expire: time.Now().Add(chatAdminTTL)})
	return admin
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
	b.BioCache.Store(uid, bioEntry{bio: resp.Result.Bio,
		expire: time.Now().Add(bioTTL)})
	return resp.Result.Bio
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

// msgText 返回参与判定的正文：正文为空时取图片/视频配文。
// 只看 Text 会把「图片 + 配文」这一整类广告漏光。
func msgText(m *tg.Message) string {
	if m.Text != "" {
		return m.Text
	}
	return m.Caption
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
	Bio         string `json:"bio"`
	IsPremium   bool   `json:"is_premium"`
	AgeHours    int64  `json:"age_hours"`
	AgeKnown    bool   `json:"age_known"`
	MsgsInGroup int64  `json:"msgs_in_group"`
	PriorAdHits int64  `json:"prior_ad_hits"`
}

type adMessageInfo struct {
	Text        string `json:"text"`
	HasLink     bool   `json:"has_link"`
	HasMention  bool   `json:"has_mention"`
	HasMedia    bool   `json:"has_media"`
	IsForwarded bool   `json:"is_forwarded"`
	Length      int    `json:"length"`
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

// buildState 组装送检载荷。调用方必须在它返回之后才把当前消息推进
// 上下文环，否则模型会在 recent_context 里看到自己要判的那一条。
func buildState(b *core.Bot, snap *store.Snapshot, m *tg.Message, p senderProfile) adState {
	text := msgText(m)
	st := adState{
		Message: adMessageInfo{
			Text:        core.TruncateRunes(text, adStateTextLimit),
			HasMedia:    m.Text == "" && m.Caption != "",
			IsForwarded: len(m.ForwardOrigin) > 0,
			// 长度报原文的，不报截断后的：长度本身是判定信号
			// （刷屏、超长广告文案），截断载荷不该把它一起抹掉。
			Length: len([]rune(text)),
		},
		Sender: p,
		Quoted: quotedInfo(m),
	}
	if m.Chat != nil {
		st.Chat = adChatInfo{ID: m.Chat.ID, Title: m.Chat.Title}
		st.RecentContext = b.AdCtx.Recent(m.Chat.ID,
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

// adAction 是一次处置的动作组合。
type adAction struct {
	Delete bool
	Mute   bool
	Alert  bool
	Name   string // none / alerted / deleted / deleted_muted
}

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
		return adAction{Delete: true, Mute: true, Alert: true, Name: "deleted_muted"}
	case conf >= hard:
		return adAction{Delete: true, Alert: true, Name: "deleted"}
	case conf >= soft && newbie:
		return adAction{Delete: true, Alert: true, Name: "deleted"}
	case conf >= soft:
		return adAction{Alert: true, Name: "alerted"}
	}
	return adAction{Name: "none"}
}

// applyAction 执行处置，返回实际动作名与失败说明。
//
// dryrun 为真时完整跳过所有群内写操作，只把「本应执行什么」记下来。
// 试运行期必须能看清 AI 会怎么判，而又不真的动群里的人。
//
// 删除与禁言互相独立：一个失败不得连带取消另一个，否则广告号会
// 既留着消息又不受任何限制。
func ApplyAction(b *core.Bot, m *tg.Message, act adAction, dryrun bool) (string, string) {
	if act.Name == "none" {
		return "none", ""
	}
	if dryrun {
		return "dryrun:" + act.Name, ""
	}

	var notes []string
	if act.Delete {
		if ok, desc := b.CallOK("deleteMessage", map[string]any{
			"chat_id": m.Chat.ID, "message_id": m.MessageID,
		}); !ok {
			notes = append(notes, noteDeleteFailed+": "+desc)
		}
	}
	if act.Mute {
		hours := b.Cache.Snap().BotSettingInt(b.BotID(), "antiad_mute_hours", 24)
		if ok, desc := b.CallOK("restrictChatMember", map[string]any{
			"chat_id": m.Chat.ID, "user_id": m.From.ID,
			"until_date":  time.Now().Unix() + hours*3600,
			"permissions": MutedPermissions(),
		}); !ok {
			notes = append(notes, noteMuteFailed+": "+desc)
		}
	}
	return act.Name, strings.Join(notes, "; ")
}

// 失败说明的两个前缀。写入方（applyAction）与判读方（adAlertKB）
// 共用同一份常量：告警要据此决定补刀按钮给不给，两处各写一份字面量
// 的话，改了文案就会静默丢掉按钮。
const (
	noteDeleteFailed = "删除失败"
	noteMuteFailed   = "禁言失败"
)

// logAd 落一条判定流水，返回自增 id（失败返回 0）。
// clean 也要记：否则算不出真实开销，形态总结也拿不到样本量基数。
func logAd(b *core.Bot, m *tg.Message, v adVerdict, action, reason string) int64 {
	verdict := "clean"
	if v.IsAd {
		verdict = "ad"
	}
	if v.Decider == "" {
		verdict = "error"
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

// alertTextLimit 是告警里展示的原文长度。TG 单条消息 4096 字符上限，
// 留足空间给画像、判定与按钮。
const alertTextLimit = 500

// sendAdAlert 把一次命中推给全部管理员。
//
// 演练模式下这是唯一的输出渠道，措辞必须让人一眼看出「没有真的动手」，
// 否则管理员会以为广告已经被删了。
// note 是 applyAction 的失败说明（成功时为空）：告警要按「实际结果」
// 而非「意图」渲染。
func sendAdAlert(b *core.Bot, conf store.BotChat, m *tg.Message, v adVerdict, act adAction,
	note string, p senderProfile, logID int64, dryrun bool) {

	// 每次命中都给「全部管理员」各发一条私聊。不节流的话，一个刷屏的人
	// 就能把每个管理员的私聊刷爆，还会撞上 Telegram 对 bot 的发送速率
	// 限制，连正常的业务消息一起发不出去。按 (群, 人) 隔离：压掉的应该
	// 是这一个人的重复告警，不是整个群的。
	//
	// 节流放在投递侧而非渲染侧：/ad 复查有自己的命令限频，
	// 它的结果是人主动要的，不该被这条自动告警的节流压掉。
	snap := b.Cache.Snap()
	if !b.AdLimits.Allow(fmt.Sprintf("ad:a:%d:%d", m.Chat.ID, m.From.ID),
		snap.BotSettingInt(b.BotID(), "antiad_alert_rpm", 3)) {
		slog.Info("反广告：告警被节流", "chat", m.Chat.ID, "uid", m.From.ID)
		return
	}

	text, kb := renderAdAlert(b, m, v, act, note, p, logID, dryrun)

	// 两个去向互相独立，都可以单独关掉。两个都关是合法配置：
	// 只留流水、不打扰任何人，此时一条消息都不该发出去。
	if snap.BotSettingInt(b.BotID(), "antiad_dm_admins", 1) == 1 {
		for _, admin := range b.AlertTargets() {
			b.Send(admin, text, kb)
		}
	}
	if conf.GroupAlert && m.Chat != nil {
		// 贴在群里：处置结果对全群可见，该群的 TG 管理员能直接点按钮
		// 判断（见 adDispositionAllowed）。默认关闭 —— 升级上来的部署
		// 不该突然在群里多出消息。
		//
		// 用精简版：群里要的是「谁、为什么、被怎么处理了」，
		// 私聊那份完整画像在群里只会刷屏，原文更是不能整段贴回去。
		brief, bkb := renderAdAlertBrief(b, m, v, act, note, p, logID, dryrun)
		// 只撤回群里这条：私聊是管理员自己的收件箱，bot 没有理由去清理，
		// 那里也不会刷屏。
		scheduleAlertCleanup(b, m.Chat.ID, b.SendGetID(m.Chat.ID, brief, bkb),
			time.Duration(snap.BotSettingInt(b.BotID(), "antiad_alert_ttl", 300))*time.Second)
	}
}

// groupAlertTextLimit 是群内告警展示的原文长度。
//
// 比私聊版（500）短得多，而且是故意的：bot 刚把广告删掉，转头自己把
// 原文完整贴回群里的话，删除等于白做，引流信息照样可见。这里只留够
// 让人辨认「是哪条」的开头。
const groupAlertTextLimit = 40

// renderAdAlertBrief 渲染群内版告警。
//
// 与私聊版分开：私聊是给管理员事后复盘的，信息越全越好；群里是给在场
// 的人一眼看明白「谁、为什么、被怎么处理了」，长文只会刷屏，而且群名与
// chat_id 在群里是纯噪音——大家已经在这个群里了。
func renderAdAlertBrief(b *core.Bot, m *tg.Message, v adVerdict, act adAction,
	note string, p senderProfile, logID int64, dryrun bool) (string, map[string]any) {

	var sb strings.Builder
	if dryrun {
		// 这个标识不能省：没有它，群里会以为广告已经被删了。
		sb.WriteString("🧪 <b>演练（未实际处置）</b>\n")
	}

	kind := v.Kind
	if kind == "" {
		kind = "未分类"
	}
	fmt.Fprintf(&sb, "🚫 <b>广告</b> · %.0f%% · %s\n",
		v.Confidence*100, html.EscapeString(kind))

	fmt.Fprintf(&sb, "├ 用户  %s (<code>%d</code>)\n",
		html.EscapeString(senderName(m.From)), m.From.ID)
	// 资历是误判与否最要紧的一条线索，用一个词带过。
	fmt.Fprintf(&sb, "├ 资历  %s\n", seniorityWord(p))

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

	if txt := core.TruncateRunes(displayText(m), groupAlertTextLimit); txt != "" {
		fmt.Fprintf(&sb, "「<code>%s</code>」", html.EscapeString(txt))
	}

	return sb.String(), adAlertKB(act, note, logID, dryrun)
}

// scheduleAlertCleanup 安排到点撤回 bot 自己发的群内告警。
//
// 群里连着几条告警会把正常对话顶走，而告警的信息价值在处置完成后就没了。
// 撤回的只是 bot 自己那条，判定流水与处置结果都留在库里，面板的
// 「📋 拦截记录」随时能查回来。
//
// 代价要讲清楚：告警一撤，上面的处置按钮也跟着没了。所以 ttl 可以配成
// 0 表示永不撤回 —— 有人就是希望按钮一直挂在那儿。
func scheduleAlertCleanup(b *core.Bot, chatID, msgID int64, ttl time.Duration) {
	if ttl <= 0 || msgID == 0 {
		// msgID 为 0 说明没拿到发送结果。传 0 给 TG 会被解读成别的消息，
		// 宁可不撤也不能删错。
		return
	}
	// 用 AfterFunc 而不是睡眠的 goroutine：它走运行时计时器，
	// 几百条待撤回的告警也只是几百个定时器项。
	time.AfterFunc(ttl, func() {
		b.TG.Call("deleteMessage", map[string]any{
			"chat_id": chatID, "message_id": msgID,
		})
	})
}

// seniorityWord 把资历压成一个词，供群内告警用。
func seniorityWord(p senderProfile) string {
	switch {
	case !p.AgeKnown:
		return fmt.Sprintf("群内 %d 条", p.MsgsInGroup)
	case p.AgeHours < 72:
		return fmt.Sprintf("新人 · 进群 %d 小时 · 群内 %d 条", p.AgeHours, p.MsgsInGroup)
	default:
		return fmt.Sprintf("进群 %d 天 · 群内 %d 条", p.AgeHours/24, p.MsgsInGroup)
	}
}

// renderAdAlert 渲染私聊告警正文与按钮。
func renderAdAlert(b *core.Bot, m *tg.Message, v adVerdict, act adAction,
	note string, p senderProfile, logID int64, dryrun bool) (string, map[string]any) {

	var sb strings.Builder
	if dryrun {
		sb.WriteString("🧪 <b>演练模式（未实际处置）</b>\n\n")
	}
	sb.WriteString("🚫 <b>反广告命中</b>\n\n")

	title := m.Chat.Title
	if title == "" {
		title = "（无标题）"
	}
	fmt.Fprintf(&sb, "群组: %s (<code>%d</code>)\n", html.EscapeString(title), m.Chat.ID)
	fmt.Fprintf(&sb, "用户: %s (<code>%d</code>)\n",
		html.EscapeString(senderName(m.From)), m.From.ID)

	ageDesc := fmt.Sprintf("进群 %d 小时", p.AgeHours)
	if !p.AgeKnown {
		ageDesc = fmt.Sprintf("已观察到 %d 小时（进群时间未知）", p.AgeHours)
	}
	fmt.Fprintf(&sb, "画像: %s · 群内 %d 条 · 历史命中 %d 次\n",
		ageDesc, p.MsgsInGroup, p.PriorAdHits)

	kind := v.Kind
	if kind == "" {
		kind = "未分类"
	}
	fmt.Fprintf(&sb, "判定: 广告 (%s) · 置信度 %.0f%% · 来源 %s\n",
		html.EscapeString(kind), v.Confidence*100, v.Decider)
	if v.Reason != "" {
		fmt.Fprintf(&sb, "理由: %s\n", html.EscapeString(v.Reason))
	}

	fmt.Fprintf(&sb, "\n原文:\n<code>%s</code>\n",
		html.EscapeString(core.TruncateRunes(displayText(m), alertTextLimit)))

	if dryrun {
		fmt.Fprintf(&sb, "\n<b>本应执行</b>: %s（演练模式下未执行）", actionDesc(act))
	} else if note != "" {
		// 如实说哪一步没做成。谎报「已执行」会让管理员不去补刀，
		// 广告就一直留在群里 —— 而这恰恰是最需要人介入的情况。
		fmt.Fprintf(&sb, "\n已执行: %s\n⚠️ %s", actionDesc(act), html.EscapeString(note))
	} else {
		fmt.Fprintf(&sb, "\n已执行: %s", actionDesc(act))
	}

	return sb.String(), adAlertKB(act, note, logID, dryrun)
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
	if dryrun || !act.Mute || strings.Contains(note, noteMuteFailed) {
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
		return "删除消息 + 禁言"
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
	err := s.Read.QueryRow(`SELECT id,chat_id,user_id,message_id,text,verdict,
		confidence,decider,ad_kind,action,reason,prompt_tokens,completion_tokens,
		quota_cost,created_at FROM antiad_log WHERE id=?`, id).
		Scan(&r.ID, &r.ChatID, &r.UserID, &r.MessageID, &r.Text, &r.Verdict,
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
