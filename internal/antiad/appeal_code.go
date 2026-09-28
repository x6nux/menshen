package antiad

import (
	"crypto/rand"
	"math/big"
	"regexp"
	"strings"
	"time"

	"menshen/internal/core"
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
func issueUnlockCode(sh *core.Shared, appealID int64) (string, bool) {
	for attempt := 0; attempt < 5; attempt++ {
		code, err := newUnlockCode()
		if err != nil {
			return "", false
		}
		expires := time.Now().Add(unlockCodeTTL).Unix()
		_, err = sh.Store.Write.Exec(`UPDATE appeals SET code=?, code_expires=?,
			status='code', updated_at=? WHERE id=?`,
			code, expires, time.Now().Unix(), appealID)
		if err == nil {
			return code, true
		}
		// 撞码（唯一索引）就重生成；其他错误也重试，最后统一失败。
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

// looksLikeUnlockCode 报告这段文本是否含解禁码。
func looksLikeUnlockCode(text string) bool {
	_, ok := findUnlockCode(text)
	return ok
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
