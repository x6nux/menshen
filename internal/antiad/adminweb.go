package antiad

// 网页版管理面板（/admin）的登录基础设施。
//
// 浏览器里没有 Telegram initData，登录靠 bot 私聊里生成的**签名链接**：
//   - 登录链接：<public_url>/admin/login/<bot_id>/<uid>/<exp>/<sig>，10 分钟内有效，
//     一次性语义（签名绑定 uid 与过期时间）；
//   - 打开后由 panel 种 HttpOnly 会话 cookie（12 小时），签名密钥仍是
//     web_secret——轮换密钥即让所有已发出的链接与会话一起失效。
//
// 两条签名用不同的用途前缀（adm: 链接 / adms: 会话），一处的签名拿不到
// 另一处用。

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"menshen/internal/core"
)

// adminLoginWindow 是登录链接的有效期；过期后在 bot 里重新获取即可。
const adminLoginWindow = 10 * time.Minute

// AdminSessionTTL 是网页版会话 cookie 的有效期。
const AdminSessionTTL = 12 * time.Hour

// adminLoginSig 把 botID 一起签进消息：登录后跳到该 bot 的面板上下文
// （API 的 X-Bot-Id），被篡改会验签失败。
func adminLoginSig(sh *core.Shared, uid, botID, exp int64) string {
	return webSig(sh, fmt.Sprintf("adm:%d:%d:%d", uid, botID, exp))
}

func adminSessionSig(sh *core.Shared, uid, exp int64) string {
	return webSig(sh, fmt.Sprintf("adms:%d:%d", uid, exp))
}

// AdminLoginURL 生成网页版管理面板的登录链接；网页不可用时返回空串。
func AdminLoginURL(sh *core.Shared, uid, botID int64) string {
	if !WebAvailable(sh) || uid == 0 || botID == 0 {
		return ""
	}
	exp := time.Now().Add(adminLoginWindow).Unix()
	base := strings.TrimRight(sh.Cfg.PublicURL, "/")
	return fmt.Sprintf("%s/admin/login/%d/%d/%d/%s",
		base, botID, uid, exp, adminLoginSig(sh, uid, botID, exp))
}

// VerifyAdminLogin 校验登录链接；返回可读的中文错误。
func VerifyAdminLogin(sh *core.Shared, uid, botID, exp int64, sig string) error {
	if uid == 0 || botID == 0 {
		return errors.New("登录链接缺少用户信息")
	}
	if exp < time.Now().Unix() {
		return errors.New("登录链接已过期，请在 bot 私聊里重新获取")
	}
	if !webSigOK(sh, fmt.Sprintf("adm:%d:%d:%d", uid, botID, exp), sig) {
		return errors.New("登录链接无效")
	}
	if !sh.IsStaff(uid) {
		return errors.New("你不是本服务的管理员")
	}
	return nil
}

// AdminSessionValue 生成要写入 cookie 的会话值 <uid>:<exp>:<sig>。
func AdminSessionValue(sh *core.Shared, uid int64) string {
	exp := time.Now().Add(AdminSessionTTL).Unix()
	return fmt.Sprintf("%d:%d:%s", uid, exp, adminSessionSig(sh, uid, exp))
}

// VerifyAdminSession 校验会话值：格式、过期时间、签名与管理员身份。
func VerifyAdminSession(sh *core.Shared, value string) (int64, bool) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return 0, false
	}
	uid, err1 := strconv.ParseInt(parts[0], 10, 64)
	exp, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil || uid == 0 || exp < time.Now().Unix() {
		return 0, false
	}
	if !webSigOK(sh, fmt.Sprintf("adms:%d:%d", uid, exp), parts[2]) {
		return 0, false
	}
	if !sh.IsStaff(uid) {
		return 0, false
	}
	return uid, true
}
