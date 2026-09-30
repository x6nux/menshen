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

// unbanPayload 把冷判定记录号编码进 deep link 的 start 参数。
//
// Telegram 的 start payload 只允许 A-Z a-z 0-9 _ -。记录号本身不是秘密
// （只在管理员私聊的卡片与群里出现过），不需要加密；它的用途是让管理员
// 点进来直接落到那条冷判定记录，被限制的人看到的仍是申诉入口。
func unbanPayload(logID int64) string {
	return "ub" + strconv.FormatInt(logID, 10)
}

// ParseUnbanPayload 还原冷判定记录号。第二个返回值为假表示这不是它的入口。
func ParseUnbanPayload(p string) (int64, bool) {
	rest, ok := strings.CutPrefix(p, "ub")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// logPayload 把记录号编码进 deep link 的 start 参数。
//
// 「打开 bot 处理」的按钮必须带记录号：不带的话管理员点进去只落到主菜单，
// 看不出是哪条案件；被罚的人也说不清在申诉哪一次。记录号只在管理员私聊
// 与群里出现过，本身不是秘密，不需要加密。
func logPayload(logID int64) string {
	return "log" + strconv.FormatInt(logID, 10)
}

// ParseLogPayload 还原记录号；第二个返回值为假表示这不是记录卡片的入口。
//
// 权限不在这里判：解析出来只表示「想打开哪条记录」，能不能看由面板的
// CanManageBot 兜底；普通用户一律走申诉入口，看到的只有自己的限制。
func ParseLogPayload(p string) (int64, bool) {
	rest, ok := strings.CutPrefix(p, "log")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
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
	// 这是我们主动解除的：外部解除复查别把它当成「被别的 bot 抹掉了」
	// 又给施加回去（解除要发 TG 调用，chat_member 更新回流有几秒延迟）。
	NoteLifted(chatID, uid)
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

// HandleStartPayload 与验证码流程已由申诉通道取代（见 appeal.go）：
// deep link 现在进入「列出有效限制 → 写理由/直接申诉 → AI 复判 → 网页验证」。
// 这里只保留 join_mutes 的读写、退避闸与解除动作，供申诉流程复用。

// liftJoinMute 解除限制并收尾：恢复权限、撤掉群里那条通知、清记录。
func liftJoinMute(b *core.Bot, dmChat, groupID int64, u *tg.TGUser,
	rec joinMuteRec, note string) {

	NoteLifted(groupID, u.ID)
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
