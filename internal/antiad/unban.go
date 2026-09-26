package antiad

import (
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// unbanPayload 把群号编码进 deep link 的 start 参数。
//
// Telegram 的 start payload 只允许 A-Z a-z 0-9 _ -，而群号是负数，
// 减号在这个字符集里但放在开头容易被各种中间层吃掉，所以统一去掉符号
// 再加前缀。群号本身不是秘密（群成员都看得到），不需要加密。
func unbanPayload(chatID int64) string {
	return "ub" + strconv.FormatInt(-chatID, 10)
}

// parseUnbanPayload 还原群号。第二个返回值为假表示这不是解除限制的入口。
func parseUnbanPayload(p string) (int64, bool) {
	rest, ok := strings.CutPrefix(p, "ub")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return -n, true
}

// joinMuteRec 是 join_mutes 的一行。
type joinMuteRec struct {
	ChatID    int64
	UserID    int64
	BotID     int64
	Reason    string
	NoticeMsg int64
	Attempts  int64
	CreatedAt int64
}

func saveJoinMute(b *core.Bot, chatID, uid int64, reason string, noticeMsg int64) {
	if _, err := b.Store.Write.Exec(`INSERT INTO join_mutes
		(chat_id,user_id,bot_id,reason,notice_msg,attempts,created_at)
		VALUES (?,?,?,?,?,0,?)
		ON CONFLICT(chat_id,user_id) DO UPDATE SET
		  reason=excluded.reason, notice_msg=excluded.notice_msg`,
		chatID, uid, b.BotID(), core.TruncateRunes(reason, 300), noticeMsg,
		time.Now().Unix()); err != nil {
		slog.Error("冷判定：限制记录落库失败", "chat", chatID, "uid", uid, "err", err)
	}
}

func loadJoinMute(s *store.Store, chatID, uid int64) (joinMuteRec, bool) {
	var r joinMuteRec
	err := s.Read.QueryRow(`SELECT chat_id,user_id,bot_id,reason,notice_msg,
		attempts,created_at FROM join_mutes WHERE chat_id=? AND user_id=?`,
		chatID, uid).Scan(&r.ChatID, &r.UserID, &r.BotID, &r.Reason,
		&r.NoticeMsg, &r.Attempts, &r.CreatedAt)
	if err != nil {
		return r, false
	}
	return r, true
}

func dropJoinMute(b *core.Bot, chatID, uid int64) {
	if _, err := b.Store.Write.Exec(
		`DELETE FROM join_mutes WHERE chat_id=? AND user_id=?`,
		chatID, uid); err != nil {
		slog.Error("冷判定：清除限制记录失败", "chat", chatID, "uid", uid, "err", err)
	}
}

func bumpJoinAttempts(b *core.Bot, chatID, uid int64) {
	if _, err := b.Store.Write.Exec(`UPDATE join_mutes SET attempts = attempts + 1
		WHERE chat_id=? AND user_id=?`, chatID, uid); err != nil {
		slog.Error("冷判定：更新尝试次数失败", "chat", chatID, "uid", uid, "err", err)
	}
}

// unbanAttempt 是自助解除的重试状态，只存内存。
type unbanAttempt struct {
	n    int
	next time.Time
}

// nextUnbanDelay 按尝试次数给出下一次可重试的间隔：base × 2^(n-1)，
// 封顶 1 小时。
//
// 不限次数是刻意的：改简介改不到位的普通人不该被一次失败挡死。但间隔
// 必须递增 —— 每次重试都要跑一轮 AI，有耐心的攻击者在固定间隔下能
// 一直磨开销，而指数退避让第 6 次就要等半小时，磨不动多少。
func nextUnbanDelay(base time.Duration, n int) time.Duration {
	if n <= 0 {
		return 0
	}
	// 移位前先封顶：n 大到一定程度，base<<(n-1) 会整体移出 int64，
	// 结果翻成 0 或负数，等待时长归零 —— 闸门恰好在磨得最久的那个人
	// 面前失效，正是攻击者最想要的那一端。
	if n > 40 {
		return time.Hour
	}
	d := base << (n - 1)
	if d > time.Hour || d <= 0 {
		return time.Hour
	}
	return d
}

// unbanGateCheck 报告此人现在能否发起一次解除尝试，不能时返回还要等多久。
func unbanGateCheck(sh *core.Shared, uid int64) (bool, time.Duration) {
	v, ok := sh.UnbanGate.Load(uid)
	if !ok {
		return true, 0
	}
	a := v.(unbanAttempt)
	if wait := time.Until(a.next); wait > 0 {
		return false, wait
	}
	return true, 0
}

// unbanGateBump 记一次尝试并排好下一次的时间。
func unbanGateBump(sh *core.Shared, uid int64) {
	base := time.Duration(
		sh.Cache.Snap().SettingInt("antiad_unban_base", 60)) * time.Second
	n := 1
	if v, ok := sh.UnbanGate.Load(uid); ok {
		n = v.(unbanAttempt).n + 1
	}
	sh.UnbanGate.Store(uid, unbanAttempt{n: n, next: time.Now().Add(nextUnbanDelay(base, n))})
}

// unbanGateClear 在成功解除后清掉计数，下次再被限制时从头开始。
func unbanGateClear(sh *core.Shared, uid int64) { sh.UnbanGate.Delete(uid) }

// gcUnbanGate 清理早已过期的重试记录，防止 map 无限增长。
func GCUnbanGate(sh *core.Shared) {
	now := time.Now()
	sh.UnbanGate.Range(func(k, v any) bool {
		a := v.(unbanAttempt)
		// 多留一小时：刚过期就删的话，一个人隔几分钟试一次，
		// 每次都被当成「第一次」，退避就永远不会增长。
		if now.After(a.next.Add(time.Hour)) {
			sh.UnbanGate.Delete(k)
		}
		return true
	})
}

// handleStartPayload 处理 deep link。返回 true 表示已接管这条消息。
//
// 它排在私聊的权限判断之前：被限制发言的是普通用户，按「非管理员一律
// 忽略」处理会把整条自助解除通道堵死。
func HandleStartPayload(b *core.Bot, m *tg.Message, payload string) bool {
	chatID, ok := parseUnbanPayload(payload)
	if !ok {
		return false
	}
	uid := m.From.ID

	rec, muted := loadJoinMute(b.Store, chatID, uid)
	if !muted {
		b.Send(m.Chat.ID, "你在该群没有待解除的限制。", nil)
		return true
	}

	if ok, wait := unbanGateCheck(b.Shared, uid); !ok {
		b.Send(m.Chat.ID, fmt.Sprintf(
			"请求太频繁，请在 %s 后再试。\n\n这段时间正好用来修改你的账号资料。",
			humanDuration(wait)), nil)
		return true
	}

	sendUnbanCaptcha(b, m.Chat.ID, uid, chatID, rec.Reason)
	return true
}

// sendUnbanCaptcha 出一道人机验证题。
//
// 它挡的不是人而是脚本：每答对一次就要跑一轮 AI 重判，没有这道门槛，
// 一个循环就能把判定开销刷上去。
func sendUnbanCaptcha(b *core.Bot, dmChat, uid, groupID int64, reason string) {
	c := b.Captcha.Issue(uid)

	var sb strings.Builder
	sb.WriteString("🔓 <b>申请解除限制</b>\n\n")
	fmt.Fprintf(&sb, "群组: <code>%d</code>\n", groupID)
	fmt.Fprintf(&sb, "限制原因: %s\n\n", html.EscapeString(core.TruncateRunes(reason, 200)))
	sb.WriteString("请先按上面的原因修改你的<b>用户名、昵称或个人简介</b>，" +
		"改完之后回答下面的问题，系统会重新检查：\n\n")
	fmt.Fprintf(&sb, "<b>%s</b>", html.EscapeString(c.Question))

	row := make([][2]string, 0, len(c.Options))
	for _, o := range c.Options {
		// a:cap:<答案>:<群号绝对值>，远在 64 字节以内
		row = append(row, [2]string{strconv.Itoa(o),
			fmt.Sprintf("a:cap:%d:%d", o, -groupID)})
	}
	b.Send(dmChat, sb.String(), tg.InlineKB(row))
}

// handleCaptchaCallback 处理验证码作答，答对即重新判定画像。
func HandleCaptchaCallback(b *core.Bot, q *tg.CallbackQuery) {
	parts := strings.Split(q.Data, ":")
	if len(parts) != 4 {
		b.AnswerCallback(q.ID, "")
		return
	}
	ans, err1 := strconv.Atoi(parts[2])
	neg, err2 := strconv.ParseInt(parts[3], 10, 64)
	if err1 != nil || err2 != nil {
		b.AnswerCallback(q.ID, "参数无效")
		return
	}
	groupID := -neg
	uid := q.From.ID
	dmChat := q.Message.Chat.ID

	rec, muted := loadJoinMute(b.Store, groupID, uid)
	if !muted {
		b.AnswerCallback(q.ID, "该限制已解除")
		b.Edit(dmChat, q.Message.MessageID, "该限制已经解除了。", nil)
		return
	}

	if !b.Captcha.Check(uid, ans) {
		b.AnswerCallback(q.ID, "答错了，请重新申请")
		b.Edit(dmChat, q.Message.MessageID,
			"❌ 验证未通过。请回到群里重新点击「我要解除限制」。", nil)
		return
	}

	// 验证码过了才计入重试闸：答错不该消耗退避额度，否则手滑一次就要
	// 等上几分钟，而脚本本来也不会答错。
	unbanGateBump(b.Shared, uid)
	bumpJoinAttempts(b, groupID, uid)

	b.AnswerCallback(q.ID, "验证通过，正在重新检查")
	b.Edit(dmChat, q.Message.MessageID, "✅ 验证通过，正在重新检查你的账号资料……", nil)

	if !b.AdSubmit(func() { recheckAndLift(b, dmChat, groupID, q.From, rec) }) {
		b.Send(dmChat, "系统繁忙，请稍后再点一次「我要解除限制」。", nil)
	}
}

// recheckAndLift 重新判定账号画像，通过则解除限制。
func recheckAndLift(b *core.Bot, dmChat, groupID int64, u *tg.TGUser, rec joinMuteRec) {
	snap := b.Cache.Snap()

	// 必须绕开简介缓存：对方刚改完资料，读到一小时前的旧值会让他
	// 无论怎么改都通不过——这正是这条链路最容易出的问题。
	b.BioCache.Delete(u.ID)
	bio := userBio(b, u.ID)

	gm, _ := loadMember(b.Store, groupID, u.ID)
	p := buildProfile(b, &tg.Message{From: u}, gm, time.Now().Unix())
	p.Bio = bio

	conf, _ := snap.ChatConf(b.BotID(), groupID)
	st := adState{
		Chat:      adChatInfo{ID: groupID, Title: conf.Title},
		Sender:    p,
		JoinCheck: true,
	}
	st.KnownAdPatterns, st.KnownFalsePositives = splitDigest(snap.Setting("antiad_digest"))

	v, err := judgeJoin(b, snap, st)
	if err != nil {
		// 判定失败时**放行**，与整条链路同向：系统自己的故障不该让
		// 一个已经在申诉的人继续被禁着。
		slog.Warn("自助解除：重判失败，按通过处理",
			"chat", groupID, "uid", u.ID, "err", err)
		liftJoinMute(b, dmChat, groupID, u, rec, "系统暂时无法复核，已先行解除")
		return
	}

	line := float64(snap.BotSettingInt(b.BotID(), "antiad_cold_conf", 85))
	if v.IsAd && v.Confidence*100 >= line {
		reason := strings.TrimSpace(v.Reason)
		if reason == "" {
			reason = "账号资料中仍含有推广或引流内容"
		}
		// 理由要更新：他可能改掉了一处，而模型这次指出的是另一处。
		saveJoinMute(b, groupID, u.ID, reason, rec.NoticeMsg)
		b.Send(dmChat, fmt.Sprintf(
			"❌ <b>仍未通过</b>\n\n%s\n\n请按上面的说明继续修改，改完可以再次申请。",
			html.EscapeString(core.TruncateRunes(reason, 300))), nil)
		return
	}

	liftJoinMute(b, dmChat, groupID, u, rec, "")
}

// liftJoinMute 解除限制并收尾：恢复权限、撤掉群里那条通知、清记录。
func liftJoinMute(b *core.Bot, dmChat, groupID int64, u *tg.TGUser,
	rec joinMuteRec, note string) {

	ok, desc := Unmute(b, groupID, u.ID)
	if !ok {
		// 如实告诉对方没做成，别让他以为已经能说话了。
		b.Send(dmChat, "⚠️ 复核通过，但解除限制时出错："+
			html.EscapeString(core.TruncateRunes(desc, 100))+
			"\n\n请联系群管理员处理。", nil)
		return
	}

	dropJoinMute(b, groupID, u.ID)
	unbanGateClear(b.Shared, u.ID)

	// 群里那条「已被限制」的通知留着会造成误解，撤掉。
	if rec.NoticeMsg != 0 {
		b.TG.Call("deleteMessage", map[string]any{
			"chat_id": groupID, "message_id": rec.NoticeMsg,
		})
	}

	msg := "✅ <b>限制已解除</b>\n\n你现在可以在群里正常发言了。"
	if note != "" {
		msg += "\n\n<i>" + html.EscapeString(note) + "</i>"
	}
	b.Send(dmChat, msg, nil)
	slog.Info("自助解除：已放行", "chat", groupID, "uid", u.ID, "尝试次数", rec.Attempts+1)
}

// humanDuration 把等待时长说成人话。
func humanDuration(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%d 小时", int(d.Hours())+1)
	case d >= time.Minute:
		return fmt.Sprintf("%d 分钟", int(d.Minutes())+1)
	default:
		return fmt.Sprintf("%d 秒", int(d.Seconds())+1)
	}
}
