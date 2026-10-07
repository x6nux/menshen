package antiad

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// ---- 入群人机验证 ----
//
// 新人进群先禁言，在群里发一条带验证链接的提示；本人用浏览器完成人机
// 验证（Turnstile / hCaptcha / 内置 Cap）后自动解除。它拦在门口，
// 与冷判定独立：冷判定看资料、验证看是否为人，两者可以同时开。
//
// 与 join_mutes 分开记：那是可申诉的处罚（资料/前置号判为广告），入群
// 验证是自助门槛，通过即解除。混表会让申诉页把尚未完成验证列成处罚。

// join_verify.status 的取值。
const (
	jvPending  = "pending"
	jvVerified = "verified"
	jvFailed   = "failed"
	jvExpired  = "expired"
)

// jvMaxAttempts 是同一张验证单允许的失败次数。达到上限只标记，不再受理；
// 实际处理是等窗口到期由 SweepJoinVerifies 踢出。
const jvMaxAttempts = 5

// jvSweepLimit 是每分钟复查的待验证行数上限。
const jvSweepLimit = 200

// joinVerifyRec 是 join_verify 的一行。
type joinVerifyRec struct {
	ID         int64
	BotID      int64
	ChatID     int64
	UserID     int64
	Status     string
	Attempts   int64
	NoticeMsg  int64
	CreatedAt  int64
	VerifiedAt int64
}

// joinVerifyEnabled 报告该群是否走先验证再进群流程。
//
// 三个条件缺一不可：设置打开、网页子系统可用、验证码提供方配置齐全。
// 缺项时静默按关闭处理会在已经配置却不产生验证时无从查起，所以每处都在
// 调用点留了日志（见 startJoinVerify 与面板）。
func joinVerifyEnabled(b *core.Bot) bool {
	if b.Cache.Snap().BotSettingInt(b.BotID(), "antiad_joinverify", 0) != 1 {
		return false
	}
	if !WebAvailable(b.Shared) {
		return false
	}
	return captchaOf(b.Shared).enabled(b.Shared)
}

// joinVerifyWindow 是验证链接的有效时长；0 表示不限时（也不踢人）。
func joinVerifyWindow(b *core.Bot) time.Duration {
	m := b.Cache.Snap().BotSettingInt(b.BotID(), "antiad_joinverify_minutes", 10)
	if m <= 0 {
		return 0
	}
	return time.Duration(m) * time.Minute
}

func joinVerifySig(sh *core.Shared, id, uid int64) string {
	return webSig(sh, fmt.Sprintf("jv:%d:%d", id, uid))
}

// JoinVerifyURL 是给新人的验证页链接。
func JoinVerifyURL(sh *core.Shared, id, uid int64) string {
	return webURL(sh, fmt.Sprintf("jv/%d/%s", id, joinVerifySig(sh, id, uid)))
}

func loadJoinVerify(s *store.Store, id int64) (joinVerifyRec, bool) {
	var r joinVerifyRec
	err := s.Read.QueryRow(`SELECT id,bot_id,chat_id,user_id,status,attempts,
		notice_msg,created_at,verified_at FROM join_verify WHERE id=?`, id).
		Scan(&r.ID, &r.BotID, &r.ChatID, &r.UserID, &r.Status, &r.Attempts,
			&r.NoticeMsg, &r.CreatedAt, &r.VerifiedAt)
	return r, err == nil
}

// pendingJoinVerify 找此人当前待完成的验证单（没有则返回 false）。
func pendingJoinVerify(s *store.Store, chatID, uid int64) (joinVerifyRec, bool) {
	var r joinVerifyRec
	err := s.Read.QueryRow(`SELECT id,bot_id,chat_id,user_id,status,attempts,
		notice_msg,created_at,verified_at FROM join_verify
		WHERE chat_id=? AND user_id=? AND status=? ORDER BY id DESC LIMIT 1`,
		chatID, uid, jvPending).
		Scan(&r.ID, &r.BotID, &r.ChatID, &r.UserID, &r.Status, &r.Attempts,
			&r.NoticeMsg, &r.CreatedAt, &r.VerifiedAt)
	return r, err == nil
}

// startJoinVerify 执行门口动作：禁言 + 落库 + 群内发验证链接。
func startJoinVerify(b *core.Bot, conf store.BotChat, u *tg.TGUser) {
	if conf.Dryrun {
		slog.Info("入群验证：演练模式，不限制新人", "chat", conf.ChatID, "uid", u.ID)
		return
	}
	// 群内静默时不发送任何消息，链接无法送达 —— 此时禁言会让本人无法
	// 完成验证。这种情况跳过，不施加禁言。
	if groupSilent(b) {
		slog.Warn("入群验证：群内静默下无法送达验证链接，跳过", "chat", conf.ChatID)
		return
	}
	// 同一次入群 TG 会推两份（chat_member 与 service 消息），避免重复禁言与发链接。
	if _, ok := pendingJoinVerify(b.Store, conf.ChatID, u.ID); ok {
		return
	}
	if ok, desc := b.CallOK("restrictChatMember", map[string]any{
		"chat_id": conf.ChatID, "user_id": u.ID,
		"permissions": MutedPermissions(),
	}); !ok {
		slog.Warn("入群验证：限制发言失败，放行", "chat", conf.ChatID, "uid", u.ID, "tg", desc)
		return
	}
	res, err := b.Store.Write.Exec(`INSERT INTO join_verify
		(bot_id,chat_id,user_id,status,created_at) VALUES (?,?,?,?,?)`,
		b.BotID(), conf.ChatID, u.ID, jvPending, time.Now().Unix())
	if err != nil {
		// 落库失败就发不出可验证的链接（链接要带行号），先解除再放行。
		slog.Error("入群验证：落库失败，放行", "chat", conf.ChatID, "uid", u.ID, "err", err)
		Unmute(b, conf.ChatID, u.ID)
		return
	}
	id, _ := res.LastInsertId()
	msgID := sendGroup(b, conf.ChatID, joinVerifyNotice(b, u), joinVerifyKB(b.Shared, id, u.ID))
	if msgID != 0 {
		if _, err := b.Store.Write.Exec(
			`UPDATE join_verify SET notice_msg=? WHERE id=?`, msgID, id); err != nil {
			slog.Warn("入群验证：记录提示消息号失败", "jv", id, "err", err)
		}
	}
	slog.Info("入群验证：已限制发言，等待验证",
		"chat", conf.ChatID, "uid", u.ID, "jv", id)
}

// joinVerifyNotice 是群内那条提示。只挂用户链接、不写昵称：通知要能让
// 本人知道是针对自己，而被验证的人多半还没发言过。
func joinVerifyNotice(b *core.Bot, u *tg.TGUser) string {
	text := "🛡️ " + userLink(u.ID) + "，为防机器人，请先完成人机验证。\n" +
		"点击下方按钮在浏览器里通过验证后即可发言。"
	if m := b.Cache.Snap().BotSettingInt(b.BotID(), "antiad_joinverify_minutes", 10); m > 0 {
		text += fmt.Sprintf("\n请在 %d 分钟内完成，否则会被移出群聊（可重新加入）。", m)
	}
	return text
}

// joinVerifyKB 是验证按钮。
func joinVerifyKB(sh *core.Shared, id, uid int64) map[string]any {
	link := JoinVerifyURL(sh, id, uid)
	if link == "" {
		return nil
	}
	return tg.InlineKB([][2]string{{"✅ 点此完成验证", tg.URLBtn(link)}})
}

// ---- 网页：数据接口与提交 ----

// handleJoinVerifyPage 处理验证页的 GET ?json=1 与 POST。
func handleJoinVerifyPage(sh *core.Shared, w http.ResponseWriter, r *http.Request, rt webRoute) {
	rec, ok := loadJoinVerify(sh.Store, rt.id)
	if !ok || !webSigOK(sh, fmt.Sprintf("jv:%d:%d", rec.ID, rec.UserID), rt.sig) {
		// 签名错与记录不存在不区分，免得被按编号扫。
		writeWebJSON(w, http.StatusNotFound, map[string]any{"error": "链接无效或已被替换。"})
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Query().Get("json") == "1":
		writeWebJSON(w, http.StatusOK, joinVerifyPageData(botFor(sh, rec.BotID), sh, rec))
	case r.Method == http.MethodPost:
		joinVerifyPost(sh, w, r, rec)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// joinVerifyExpired 报告这张验证单是否已超出窗口。
func joinVerifyExpired(b *core.Bot, rec joinVerifyRec, now int64) bool {
	wd := joinVerifyWindow(b)
	return wd > 0 && now-rec.CreatedAt >= int64(wd/time.Second)
}

// joinVerifyPageData 组装验证页需要的数据。
//
// sitekey 与 provider 交给前端选择渲染哪个组件；cap 还要端点（前端据此拼
// <endpoint>/<sitekey>/ 调用）。窗口分钟数与到期时刻供页面提示倒计时。
func joinVerifyPageData(b *core.Bot, sh *core.Shared, rec joinVerifyRec) map[string]any {
	p := captchaOf(sh)
	data := map[string]any{
		"jv_id":    rec.ID,
		"uid":      rec.UserID,
		"chat_id":  rec.ChatID,
		"provider": p.name,
		"sitekey":  p.siteKey,
		"status":   rec.Status,
		"minutes":  0,
		"expires":  0,
	}
	if b != nil {
		if m := b.Cache.Snap().BotSettingInt(b.BotID(), "antiad_joinverify_minutes", 10); m > 0 {
			data["minutes"] = m
			data["expires"] = rec.CreatedAt + m*60
		}
		if c, ok := b.Cache.Snap().ChatConf(b.BotID(), rec.ChatID); ok && strings.TrimSpace(c.Title) != "" {
			data["chat"] = c.Title
		}
	}
	if p.name == "cap" {
		data["endpoint"] = CapBaseURL(sh)
	}
	return data
}

// joinVerifyPost 处理验证提交。
func joinVerifyPost(sh *core.Shared, w http.ResponseWriter, r *http.Request, rec joinVerifyRec) {
	now := time.Now().Unix()
	if rec.Status != jvPending {
		writeWebJSON(w, http.StatusConflict, map[string]any{
			"ok": false, "msg": "这张验证已经完成或已失效。"})
		return
	}
	b := botFor(sh, rec.BotID)
	if b == nil {
		writeWebJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok": false, "msg": "服务暂时不可用，请稍后重试。"})
		return
	}
	if joinVerifyExpired(b, rec, now) {
		writeWebJSON(w, http.StatusGone, map[string]any{
			"ok": false, "msg": "验证链接已过期，请重新加入群聊再试。"})
		return
	}
	// 限频：每张验证单每分钟 5 次、每个 IP 每分钟 20 次。
	ip := clientIP(sh, r)
	if !sh.AdLimits.Allow(fmt.Sprintf("web:jv:%d", rec.ID), 5) ||
		!sh.AdLimits.Allow("web:ip:"+ip, 20) {
		writeWebJSON(w, http.StatusTooManyRequests, map[string]any{
			"ok": false, "msg": "请求太频繁，请稍后再试。"})
		return
	}

	var body struct {
		Token   string     `json:"token"`
		Signals webSignals `json:"signals"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).
		Decode(&body); err != nil {
		writeWebJSON(w, http.StatusBadRequest, map[string]any{
			"ok": false, "msg": "请求体无法解析。"})
		return
	}

	ua := r.Header.Get("User-Agent")
	hard, soft := evalSignals(ua, body.Signals)
	fp := fingerprintOf(body.Signals)

	result := "pass"
	msg := "验证未通过，请刷新页面重试。"
	if len(hard) > 0 {
		result = "bot"
	} else if ok, why := captchaOf(sh).verify(sh, body.Token, ip); !ok {
		result = captchaOf(sh).name
		slog.Info("入群验证：验证码未通过", "jv", rec.ID,
			"提供方", captchaOf(sh).name, "原因", why)
	}
	recordJoinVerifyAttempt(b, rec, ip, ua, fp, hard, soft)

	if result != "pass" {
		// 失败只累加；达到上限标记 failed（不再受理），实际处理等窗口到期踢出。
		if _, err := b.Store.Write.Exec(
			`UPDATE join_verify SET attempts=attempts+1 WHERE id=?`, rec.ID); err != nil {
			slog.Warn("入群验证：累加失败次数出错", "jv", rec.ID, "err", err)
		}
		if rec.Attempts+1 >= jvMaxAttempts {
			b.Store.Write.Exec(
				`UPDATE join_verify SET status=? WHERE id=? AND status=?`,
				jvFailed, rec.ID, jvPending)
		}
		writeWebJSON(w, http.StatusOK, map[string]any{"ok": false, "msg": msg})
		return
	}

	// 通过：解除限制（若账号另有资料类限制则保留，改由申诉通道处理）。
	muted, note := passJoinVerify(b, rec)
	if muted {
		writeWebJSON(w, http.StatusOK, map[string]any{"ok": true, "msg": note})
		return
	}
	writeWebJSON(w, http.StatusOK, map[string]any{
		"ok": true, "msg": "验证通过，你现在可以在群里发言了。"})
}

// passJoinVerify 收尾：解除验证禁言、撤回提示、标记通过。
//
// 账号同时存在资料类限制（join_mutes）时不解除：该限制有自己的申诉
// 出口，自动放开会让入群验证成为绕过画像限制的通道。返回 true 表示
// 仍处于限制中，note 是给用户看的说明。
func passJoinVerify(b *core.Bot, rec joinVerifyRec) (bool, string) {
	NoteLifted(b.Shared, rec.ChatID, rec.UserID)

	if _, hasProfile := loadJoinMute(b.Store, rec.ChatID, rec.UserID); !hasProfile {
		if ok, desc := Unmute(b, rec.ChatID, rec.UserID); !ok {
			// 对方已不在群是正常的（刚进来又退了），不算失败。
			if MuteMoot(b, rec.ChatID, rec.UserID, desc) == "" {
				slog.Warn("入群验证：解除限制失败", "jv", rec.ID, "tg", desc)
				return true, "验证通过，但解除限制时出错，请联系群管理员。"
			}
		}
	}

	if _, err := b.Store.Write.Exec(`UPDATE join_verify
		SET status=?, verified_at=? WHERE id=?`, jvVerified, time.Now().Unix(), rec.ID); err != nil {
		slog.Error("入群验证：标记通过失败", "jv", rec.ID, "err", err)
	}
	deleteJoinVerifyNotice(b, rec)
	slog.Info("入群验证：验证通过", "chat", rec.ChatID, "uid", rec.UserID, "jv", rec.ID)

	if _, hasProfile := loadJoinMute(b.Store, rec.ChatID, rec.UserID); hasProfile {
		return true, "人机验证已通过。但你的账号资料仍被本群限制，请按群内限制提示里的申诉入口处理。"
	}
	return false, ""
}

// deleteJoinVerifyNotice 撤回群里那条验证提示。
func deleteJoinVerifyNotice(b *core.Bot, rec joinVerifyRec) {
	if rec.NoticeMsg != 0 {
		b.TG.Call("deleteMessage", map[string]any{
			"chat_id": rec.ChatID, "message_id": rec.NoticeMsg,
		})
	}
}

// recordJoinVerifyAttempt 把这次尝试的 IP / UA / 指纹落库，覆盖上一行。
//
// 只留最近一次：与申诉不同，这里要的是当前这个人用的是什么环境，
// 而不是逐次审计。原始的 signals JSON 不落库（join_verify 没有该列）。
func recordJoinVerifyAttempt(b *core.Bot, rec joinVerifyRec, ip, ua, fp string,
	hard, soft []string) {

	flags := strings.Join(append(append([]string{}, hard...), soft...), "；")
	if _, err := b.Store.Write.Exec(`UPDATE join_verify
		SET ip=?,ua=?,fp=?,flags=? WHERE id=?`,
		ip, core.TruncateRunes(ua, 300), fp, flags, rec.ID); err != nil {
		slog.Warn("入群验证：记录验证尝试失败", "jv", rec.ID, "err", err)
	}
}

// ---- 超时清理 ----

// SweepJoinVerifies 处理未完成的验证单：到窗口还没通过就踢出群聊。
//
// 只踢不封：允许本人重新进群再验证一次。删除提示消息，并把记录置为
// expired。
func SweepJoinVerifies(b *core.Bot, now time.Time) {
	wd := joinVerifyWindow(b)
	if wd <= 0 {
		return // 不限时：一直禁言，等本人回来验证
	}
	rows, err := b.Store.Read.Query(`SELECT id,chat_id,user_id,notice_msg,created_at
		FROM join_verify WHERE bot_id=? AND status=? LIMIT ?`,
		b.BotID(), jvPending, jvSweepLimit)
	if err != nil {
		slog.Error("入群验证：读取待验证记录失败", "err", err)
		return
	}
	type item struct {
		id, chatID, uid, noticeMsg, createdAt int64
	}
	var items []item
	for rows.Next() {
		var it item
		if rows.Scan(&it.id, &it.chatID, &it.uid, &it.noticeMsg, &it.createdAt) == nil {
			items = append(items, it)
		}
	}
	rows.Close()

	for _, it := range items {
		if now.Unix()-it.createdAt < int64(wd/time.Second) {
			continue
		}
		conf, ok := chatActive(b, it.chatID)
		if !ok || conf.Dryrun {
			// 群已停用/移除：没法管理，直接结案，避免每分钟反复查。
			if _, err := b.Store.Write.Exec(
				`UPDATE join_verify SET status=? WHERE id=?`, jvExpired, it.id); err != nil {
				slog.Warn("入群验证：标记过期失败", "jv", it.id, "err", err)
			}
			continue
		}
		if ok, desc := kickJoinVerify(b, conf.ChatID, it.uid); !ok {
			slog.Warn("入群验证：超时移出失败", "chat", it.chatID, "uid", it.uid, "tg", desc)
			continue
		}
		if _, err := b.Store.Write.Exec(
			`UPDATE join_verify SET status=? WHERE id=?`, jvExpired, it.id); err != nil {
			slog.Warn("入群验证：标记过期失败", "jv", it.id, "err", err)
		}
		deleteJoinVerifyNotice(b, joinVerifyRec{ChatID: it.chatID, NoticeMsg: it.noticeMsg})
		slog.Info("入群验证：超时未验证，已移出群聊",
			"chat", it.chatID, "uid", it.uid, "jv", it.id)
	}
}

// kickJoinVerify 把未通过验证的人踢出群：封禁后立即解封，本人可重新进群。
// 直接 ban 而不解封的话，本人无法重新进群。
func kickJoinVerify(b *core.Bot, chatID, uid int64) (bool, string) {
	if ok, desc := BanSender(b, chatID, uid); !ok {
		return false, desc
	}
	if ok, desc := b.CallOK("unbanChatMember", map[string]any{
		"chat_id": chatID, "user_id": uid, "only_if_banned": true}); !ok {
		return false, desc
	}
	return true, ""
}
