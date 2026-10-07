package antiad

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"math/big"
	"regexp"
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// ---- 解禁码：格式、生成、识别 ----

// unlockAlphabet 是 Crockford base32：去掉了容易看混的 I、L、O、U。
const unlockAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// unlockCodeRe 识别解禁码。前后加词边界，避免把一串更长的字母数字当成码。
var unlockCodeRe = regexp.MustCompile(`(?i)\bMSU-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}\b`)

// unlockCodeTTL 是解禁码的有效期（常量，不进设置）。
const unlockCodeTTL = 72 * time.Hour

// newUnlockCode 生成一个解禁码，约 40 位熵。
func newUnlockCode() (string, error) {
	var sb strings.Builder
	sb.WriteString("MSU-")
	for i := 0; i < 8; i++ {
		if i == 4 {
			sb.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(unlockAlphabet))))
		if err != nil {
			return "", err
		}
		sb.WriteByte(unlockAlphabet[n.Int64()])
	}
	return sb.String(), nil
}

// issueUnlockCode 给申诉单签发解禁码（唯一索引兜底，撞码时重试）。
//
// 带状态条件：并发提交（同一张单的两个 POST）只该有一个签出码，否则先返回
// 的那个码会被后一次 UPDATE 覆盖成无效码，用户拿着它去兑换只会得到已失效的提示。
func issueUnlockCode(sh *core.Shared, appealID int64) (string, bool) {
	for attempt := 0; attempt < 5; attempt++ {
		code, err := newUnlockCode()
		if err != nil {
			return "", false
		}
		expires := time.Now().Add(unlockCodeTTL).Unix()
		res, err := sh.Store.Write.Exec(`UPDATE appeals SET code=?, code_expires=?,
			status='code', updated_at=? WHERE id=? AND status IN ('web','noweb')`,
			code, expires, time.Now().Unix(), appealID)
		if err != nil {
			// 撞码（唯一索引）就重生成；其他错误也重试，最后统一失败。
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return code, true
		}
		// 状态已变（并发的另一路先处理了）：不再重试。
		return "", false
	}
	return "", false
}

// findUnlockCode 从一段文本里找出第一个解禁码并转成大写。
func findUnlockCode(text string) (string, bool) {
	m := unlockCodeRe.FindString(text)
	if m == "" {
		return "", false
	}
	return strings.ToUpper(m), true
}

// FindUnlockCode 是 findUnlockCode 的导出版，供 dispatch 识别私聊兑换。
func FindUnlockCode(text string) (string, bool) { return findUnlockCode(text) }

// looksLikeUnlockCode 报告这段文本是否含解禁码。
func looksLikeUnlockCode(text string) bool {
	_, ok := findUnlockCode(text)
	return ok
}

// loadAppealByCode 按解禁码查申诉单。
func loadAppealByCode(s *store.Store, code string) (appealRec, bool) {
	return scanAppeal(s.Read.QueryRow(`SELECT `+AppealColumns+`
		FROM appeals WHERE code=?`, code))
}

// ---- 群内兑换 ----

// handleGroupRedeem 尝试把一条群消息当作解禁码兑换。
//
// 以下四条全部满足才截下这条消息，否则照常留底、照常判定：广告号只要
// 在消息里塞一段长得像解禁码的文字，就能躲过判定。
//
//  1. 消息里有符合格式的串
//  2. 解禁码存在，且申诉单的 bot_id 等于本 bot（同群多个 menshen bot 各管各的码）
//  3. 发送人不是申诉人本人
//  4. 发送人有权限（主管理员 / bot 归属人 / 本群 TG 管理员）
func handleGroupRedeem(b *core.Bot, conf store.BotChat, m *tg.Message) bool {
	code, ok := findUnlockCode(m.Text)
	if !ok {
		return false
	}
	ap, ok := loadAppealByCode(b.Store, code)
	if !ok || ap.BotID != b.BotID() {
		return false
	}
	if m.From.ID == ap.UserID {
		return false
	}
	if !canMarkAd(b, conf.ChatID, m.From.ID) {
		return false
	}

	// 到这里才截下这条消息。
	deleteMsg := func() {
		b.TG.Call("deleteMessage", map[string]any{
			"chat_id": conf.ChatID, "message_id": m.MessageID})
	}
	reply := func(text string) {
		ttl := time.Duration(b.Cache.Snap().BotSettingInt(
			b.BotID(), "antiad_alert_ttl", 0)) * time.Second
		groupNotice(b, conf.ChatID, text, nil, ttl)
	}

	now := time.Now().Unix()
	if ap.Status != "code" || ap.CodeExpires < now {
		reply("该解禁码已失效。")
		deleteMsg()
		return true
	}
	var redeemed int
	b.Store.Read.QueryRow(`SELECT COUNT(*) FROM appeal_redeems
		WHERE appeal_id=? AND chat_id=?`, ap.ID, conf.ChatID).Scan(&redeemed)
	if redeemed > 0 {
		reply("该解禁码已在本群用过。")
		deleteMsg()
		return true
	}

	redeemInChat(b, ap, conf.ChatID, m.From.ID)
	if err := b.Cache.Reload(); err != nil {
		slog.Error("申诉：群内兑换后刷新快照失败", "err", err)
	}
	deleteMsg()
	reply(fmt.Sprintf("✅ %s 已解除限制，24 小时内不受反广告检查。",
		userLink(ap.UserID)))
	sendWhitelistNotice(b, ap.UserID)
	slog.Info("申诉：解禁码在群内兑换", "appeal", ap.ID,
		"chat", conf.ChatID, "by", m.From.ID)
	return true
}

// redeemInChat 在一个群里执行兑换动作（范围只限本群）。
func redeemInChat(b *core.Bot, ap appealRec, chatID, byUID int64) {
	// 1) 恢复权限：全部权限置 true，对没被禁言的人没有任何影响。
	NoteLifted(b.Shared, chatID, ap.UserID)
	if ok, desc := Unmute(b, chatID, ap.UserID); !ok {
		slog.Warn("申诉：兑换时解除禁言失败", "chat", chatID,
			"uid", ap.UserID, "err", desc)
	}
	// 1.1) 记录上是封禁的还要解封：只发权限全开不会把人放回群里。
	// only_if_banned 是必须的——对没被封的人解封等于把他踢出去。
	b.CallOK("unbanChatMember", map[string]any{
		"chat_id": chatID, "user_id": ap.UserID, "only_if_banned": true})
	// 永久禁言不会自己到期：解除后必须落标记，否则申诉入口会一直把
	// 这个人列为仍在限制中。
	MarkPenaltiesLifted(b, chatID, ap.UserID)
	// 2) 冷判定的限制记录与群通知。
	if rec, ok := loadJoinMute(b.Store, chatID, ap.UserID); ok {
		dropJoinMute(b, chatID, ap.UserID)
		if rec.NoticeMsg != 0 {
			b.TG.Call("deleteMessage", map[string]any{
				"chat_id": chatID, "message_id": rec.NoticeMsg})
		}
	}
	// 3) 联合封禁名单里的人：本群解封。名单本身不动（那是全平台的决定）。
	if _, ok := b.Cache.Snap().Gban[ap.UserID]; ok {
		b.CallOK("unbanChatMember", map[string]any{
			"chat_id": chatID, "user_id": ap.UserID, "only_if_banned": true})
	}
	// 4) 白名单 24 小时。只写库不刷快照：一次兑换可能覆盖很多群，
	// 调用方在整轮结束后统一刷一次。
	writeWhitelistQuiet(b.Shared, ap.BotID, chatID, ap.UserID, 24*time.Hour, "appeal", byUID)
	// 5) 兑换记录：主键保证每个群只兑换一次。
	b.Store.Write.Exec(`INSERT OR IGNORE INTO appeal_redeems
		(appeal_id,chat_id,by_uid,at) VALUES (?,?,?,?)`,
		ap.ID, chatID, byUID, time.Now().Unix())
}

// writeWhitelist 写一条白名单并刷新快照。
func writeWhitelist(sh *core.Shared, botID, chatID, uid int64,
	ttl time.Duration, source string, byUID int64) error {

	if err := writeWhitelistQuiet(sh, botID, chatID, uid, ttl, source, byUID); err != nil {
		return err
	}
	return sh.Cache.Reload()
}

// writeWhitelistQuiet 只写库、不刷新快照。给一次操作要写多条的调用方用：
// 逐条刷新会在兑换这类循环里重建 N 次全量快照，调用方最后刷一次即可。
func writeWhitelistQuiet(sh *core.Shared, botID, chatID, uid int64,
	ttl time.Duration, source string, byUID int64) error {

	expires := int64(0)
	if ttl > 0 {
		expires = time.Now().Add(ttl).Unix()
	}
	_, err := sh.Store.Write.Exec(`INSERT INTO ad_whitelist
		(bot_id,chat_id,user_id,expires_at,source,by_uid,created_at)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(bot_id,chat_id,user_id) DO UPDATE SET
		  expires_at=excluded.expires_at, source=excluded.source,
		  by_uid=excluded.by_uid, created_at=excluded.created_at`,
		botID, chatID, uid, expires, source, byUID, time.Now().Unix())
	return err
}

// AddWhitelist 是 writeWhitelist 的导出版，供面板与 Mini App 写白名单
// （与解禁码兑换走同一条路径，保证快照刷新与范围规则一致）。
func AddWhitelist(sh *core.Shared, botID, chatID, uid int64,
	ttl time.Duration, source string, byUID int64) error {
	return writeWhitelist(sh, botID, chatID, uid, ttl, source, byUID)
}

// RemoveWhitelist 删除一条白名单（bot_id=0 是全平台那一条）。
func RemoveWhitelist(sh *core.Shared, botID, chatID, uid int64) error {
	if _, err := sh.Store.Write.Exec(`DELETE FROM ad_whitelist
		WHERE bot_id=? AND chat_id=? AND user_id=?`, botID, chatID, uid); err != nil {
		return err
	}
	return sh.Cache.Reload()
}

// sendWhitelistNotice 私聊申诉人白名单的期限与提醒。
func sendWhitelistNotice(b *core.Bot, uid int64) {
	b.Send(uid, "✅ <b>限制已解除</b>\n\n"+
		"接下来 24 小时内你不受反广告检查。请在此期间修改资料里的推广内容、"+
		"停止发广告；期满后恢复正常检查，再次命中照常处理。", nil)
}

// ---- 私聊兑换 ----

// HandleDirectRedeem 处理管理员私聊里的解禁码。
//
// 权限按申诉单的 bot_id 判，与发给哪个 bot 无关：
//   - 主管理员：全平台，并解除联合封禁（名单是全平台的决定，只有他能动）
//   - 申诉所属 bot 的归属人：该 bot 名下所有群；名单不动，只在范围内解封
//   - 其他人：无权
func HandleDirectRedeem(b *core.Bot, m *tg.Message, code string) {
	dmChat, uid := m.Chat.ID, m.From.ID
	ap, ok := loadAppealByCode(b.Store, code)
	if !ok {
		b.Send(dmChat, "解禁码无效。", nil)
		return
	}
	snap := b.Cache.Snap()
	isMain := b.IsMain(uid)
	owner := int64(0)
	if rec := snap.Bots[ap.BotID]; rec != nil {
		owner = rec.OwnerID
	}
	if !isMain && uid != owner {
		b.Send(dmChat, "你无权兑换这个解禁码。", nil)
		return
	}

	now := time.Now().Unix()
	if ap.Status != "code" || ap.CodeExpires < now {
		b.Send(dmChat, "该解禁码已失效。", nil)
		return
	}

	scopeChats := userChats(b.Store, ap.UserID)
	okCount, failCount := 0, 0
	var failNotes []string

	// 该 bot 的实例不在运行时仍然要处理数据，只是发不了私聊。
	appealBot := botFor(b.Shared, ap.BotID)
	if appealBot == nil {
		appealBot = b
	}

	for _, rec := range snap.Bots {
		if !isMain && rec.BotID != ap.BotID {
			continue
		}
		for _, c := range snap.ChatsOf(rec.BotID) {
			if !scopeChats[c.ChatID] {
				continue
			}
			if b.Reg == nil {
				failCount++
				failNotes = append(failNotes, fmt.Sprintf(
					"群 %d：bot 未在运行", c.ChatID))
				continue
			}
			inst, live := b.Reg.LookupID(rec.BotID)
			if !live {
				failCount++
				failNotes = append(failNotes, fmt.Sprintf(
					"群 %d：bot 未在运行", c.ChatID))
				continue
			}
			redeemInChat(inst, ap, c.ChatID, uid)
			okCount++
		}
	}

	// 联合封禁：全局名单只有主管理员能解（那是全平台的决定）；专属组的
	// 账本归 bot 的归属人，归属人（或主管理员）兑换时一并解除 —— 只解群
	// 里的禁言不动名单的话，他人重新进群又会被拦。
	gbanNote := "联合封禁名单未动（那是全平台的决定）"
	if isMain {
		if _, inList := snap.Gban[ap.UserID]; inList {
			LiftGban(b.Shared, ap.UserID)
			gbanNote = "已解除联合封禁（全平台）"
		} else {
			gbanNote = "此人不在联合封禁名单里"
		}
	}
	if rec := snap.Bots[ap.BotID]; rec != nil {
		if _, inList := snap.GbanOwnBans[rec.OwnerID][ap.UserID]; inList &&
			(isMain || uid == rec.OwnerID) {
			if err := GbanOwnRemoveBan(b.Shared, rec.OwnerID, ap.UserID); err != nil {
				failNotes = append(failNotes, "专属联合封禁解除失败："+err.Error())
			} else if isMain {
				gbanNote += "；已解除专属联合封禁"
			} else {
				gbanNote = "已解除专属联合封禁"
			}
		}
	}

	// 白名单：主管理员写全平台，归属人写该 bot 名下所有群。逐群的白名单行
	// 已经在 redeemInChat 里写完（不刷快照），这里补上最后一条并统一刷新。
	wBot, wChat := ap.BotID, int64(0)
	if isMain {
		wBot, wChat = 0, 0
	}
	if err := writeWhitelistQuiet(b.Shared, wBot, wChat, ap.UserID,
		24*time.Hour, "appeal", uid); err != nil {
		failNotes = append(failNotes, "白名单写入失败："+err.Error())
	}
	if err := b.Cache.Reload(); err != nil {
		slog.Error("申诉：兑换后刷新快照失败", "err", err)
	}

	updateAppeal(b.Shared, ap.ID, `status='redeemed'`)
	b.Store.Write.Exec(`INSERT OR REPLACE INTO appeal_redeems
		(appeal_id,chat_id,by_uid,at) VALUES (?,0,?,?)`, ap.ID, uid, now)

	receipt := fmt.Sprintf("🎟 <b>解禁码已兑换</b>\n\n"+
		"申诉人：%s\n解除范围：%s\n成功 %d 个群，失败 %d 个\n%s",
		userLink(ap.UserID), map[bool]string{true: "全平台", false: "本 bot 名下所有群"}[isMain],
		okCount, failCount, gbanNote)
	if len(failNotes) > 0 {
		receipt += "\n\n失败明细：\n" + strings.Join(failNotes, "\n")
	}
	b.Send(dmChat, receipt, nil)

	appealBot.Send(ap.UserID, "✅ 群管理员已兑换你的解禁码：\n\n"+
		"接下来 24 小时内你不受反广告检查。请在此期间修改资料里的推广内容、"+
		"停止发广告；期满后恢复正常检查，再次命中照常处理。", nil)
	slog.Info("申诉：解禁码私聊兑换", "appeal", ap.ID, "by", uid,
		"全平台", isMain, "群数", okCount, "失败", failCount)
}

// userChats 返回此人在 group_members 里出现过的群集合。
func userChats(s *store.Store, uid int64) map[int64]bool {
	out := map[int64]bool{}
	rows, err := s.Read.Query(
		`SELECT DISTINCT chat_id FROM group_members WHERE user_id=?`, uid)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var c int64
		if rows.Scan(&c) == nil {
			out[c] = true
		}
	}
	return out
}

// sendUnlockCode 把解禁码与用法私聊给申诉人。
func sendUnlockCode(b *core.Bot, dmChat int64, ap appealRec) {
	expires := ""
	if ap.CodeExpires != 0 {
		expires = time.Unix(ap.CodeExpires, 0).Format("01-02 15:04")
	}
	b.Send(dmChat, "🎟 <b>你的解禁码</b>\n\n<code>"+ap.Code+"</code>\n\n"+
		"把它发给<b>群管理员</b>，管理员在群里发出来即可解除该群的限制；"+
		"私聊发给 bot 管理员则解除本 bot 名下所有群。\n\n"+
		"有效期至 "+expires+"。", nil)
}
