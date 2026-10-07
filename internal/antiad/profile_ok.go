package antiad

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strings"
	"time"

	"menshen/internal/core"
)

// ---- 资料临时放行（复判确认资料没问题） ----
//
// 账号资料（昵称、用户名、简介、挂的链接）长期不变，初判可能仅凭资料判为
// 广告；复判看过上下文判为正常后，若不给资料放行，下一条消息仍会被同一份
// 资料拖入删除。因此复判可给资料一个临时放行：期间资料不再作为广告证据，
// 正文、引用、历史照常判断——只放行资料，不放行整个账号。
//
// 放行时长由复判模型在 1~72 小时之间给出，非正数表示不放行；放行绑定资料
// 内容，资料变更后重新判定。

const (
	// profileOKMaxHours 是放行时长的上限。资料长期不放行会使用户反复受到同一
	// 误判影响；但没有上限等于让模型给出永久放行。
	profileOKMaxHours = 72
	// profileOKMinHours 是模型给出正数时长时的下限。
	profileOKMinHours = 1
)

// profileOKClause 说明资料已被复判放行这一信号，两级判定与冷判定共用。
const profileOKClause = "sender.profile_ok 为 true 表示这个账号的**资料**（昵称、" +
	"用户名、简介与挂的链接）不久前已被复判确认不构成广告，在 profile_ok_until " +
	"之前不得再凭资料判为广告（名字奇怪、只有一个频道/bot 链接、写着联系方式，" +
	"都不算理由）；正文、引用、历史照常判断 —— 放行只免掉资料这一路，他之后" +
	"发广告照样按广告处置。"

// profileHash 是资料指纹：用户名 + 昵称 + 简介。它绑定放行所针对的那份
// 资料——资料变更后自动失效，避免放行被继续沿用。
func profileHash(p senderProfile) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.Join([]string{
		strings.TrimSpace(p.Username), strings.TrimSpace(p.FirstName),
		strings.TrimSpace(p.LastName), strings.TrimSpace(p.Bio)}, "\x00"))))
	return hex.EncodeToString(sum[:])[:16]
}

// clampProfileHours 把模型给的时长夹到 0 或 [1, 72]。
func clampProfileHours(h int) int {
	if h <= 0 {
		return 0
	}
	if h < profileOKMinHours {
		return profileOKMinHours
	}
	if h > profileOKMaxHours {
		return profileOKMaxHours
	}
	return h
}

// GrantProfileOK 给这个账号的资料一个临时放行，返回放行到什么时候
// （0 表示没放）。
func GrantProfileOK(b *core.Bot, p senderProfile, hours int, reason string) int64 {
	hours = clampProfileHours(hours)
	if hours == 0 || p.UserID <= 0 {
		return 0
	}
	ph := profileHash(p)
	if ph == "" {
		return 0
	}
	now := time.Now().Unix()
	until := now + int64(hours)*3600
	if _, err := b.Store.Write.Exec(`INSERT INTO profile_ok
		(bot_id,user_id,phash,hours,reason,created_at,expires_at)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(bot_id,user_id) DO UPDATE SET phash=excluded.phash,
			hours=excluded.hours, reason=excluded.reason,
			created_at=excluded.created_at, expires_at=excluded.expires_at`,
		b.BotID(), p.UserID, ph, int64(hours),
		core.TruncateRunes(reason, 200), now, until); err != nil {
		slog.Error("反广告：资料放行落库失败", "uid", p.UserID, "err", err)
		return 0
	}
	if err := b.Cache.Reload(); err != nil {
		slog.Error("反广告：资料放行后刷新缓存失败", "err", err)
	}
	slog.Info("反广告：资料临时放行", "uid", p.UserID, "小时", hours, "理由", reason)
	return until
}

// DropProfileOK 撤销某个人的资料放行。资料本身又判成广告号时用它：
// 那份资料重新成了广告证据，之前的放行不该继续挡着。
func DropProfileOK(b *core.Bot, uid int64, why string) {
	if err := RevokeProfileOK(b.Shared, b.BotID(), uid, why); err != nil {
		slog.Error("反广告：撤销资料放行失败", "uid", uid, "err", err)
	}
}

// RevokeProfileOK 撤销某个 bot 给某人的资料放行（面板上的撤销操作也走这里）。
func RevokeProfileOK(sh *core.Shared, botID, uid int64, why string) error {
	res, err := sh.Store.Write.Exec(
		`DELETE FROM profile_ok WHERE bot_id=? AND user_id=?`, botID, uid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	slog.Info("反广告：撤销资料放行", "bot", botID, "uid", uid, "原因", why)
	return sh.Cache.Reload()
}

// markProfileOK 把资料已放行标进画像，提示词据此不再凭资料判广告。
// 频道身份（负 ID）不适用：其资料是频道名与频道简介，处置路径也不同，
// 放行没有意义。
func markProfileOK(b *core.Bot, p *senderProfile) {
	if p.UserID <= 0 || p.IsChannel {
		return
	}
	until := b.Cache.Snap().ProfileAllowed(b.BotID(), p.UserID,
		profileHash(*p), time.Now().Unix())
	if until == 0 {
		return
	}
	p.ProfileOK = true
	p.ProfileOKUntil = time.Unix(until, 0).In(
		b.Cache.Snap().Location()).Format("2006-01-02 15:04")
}

// profileOKNote 是写进流水处置说明的那句，管理员在记录卡片上能看到。
func profileOKNote(hours int) string {
	return "资料放行 " + itoaSmall(hours) + " 小时"
}

// itoaSmall 把小的正整数转成字符串（避免为一句提示引入 strconv 依赖面）。
func itoaSmall(n int) string {
	if n <= 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
