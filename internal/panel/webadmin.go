package panel

// 网页版管理面板（/admin）的入口与登录。
//
// 与 Mini App 共用同一份 SPA 产物：/admin/ 返回 index.html，资产仍走
// /miniapp/assets/*（Vite base 固定，绝对路径与页面路径无关）。区别只在
// 鉴权方式：Telegram 里用 initData，浏览器里用 bot 生成的签名链接换来的
// HttpOnly 会话 cookie（见 antiad/adminweb.go）。

import (
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"menshen/internal/antiad"
	"menshen/internal/core"
)

// webSessionCookie 是网页版会话 cookie。HttpOnly：JS 拿不到会话值；
// CSRF 由 SameSite=Lax + API 侧要求 X-Web: 1 头兜底（自定义头触发 CORS
// 预检，而本服务不返回任何 CORS 头，跨站请求发不出来）。
const webSessionCookie = "menshen_web"

// webAuthHeader 是网页版 API 请求必须带的头，也是会话模式与 initData
// 模式的切换开关（见 miniAuth）。
const webAuthHeader = "X-Web"

// WebAdminHandler 处理 /admin 下的网页版管理面板。
//
//   - GET /admin/login/<uid>/<exp>/<sig>：验签后种会话 cookie 并跳 /admin/
//   - GET /admin/logout：清 cookie 回 /admin/
//   - 其余 /admin/*：托管 SPA 入口页
func WebAdminHandler(sh *core.Shared) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/admin/login/"):
			webAdminLogin(sh, w, r)
		case r.URL.Path == "/admin/logout":
			webAdminLogout(w, r)
		case r.URL.Path == "/admin" || strings.HasPrefix(r.URL.Path, "/admin/"):
			miniAppIndex(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

// webAdminLogin 校验签名链接、种会话 cookie，然后跳进面板。
func webAdminLogin(sh *core.Shared, w http.ResponseWriter, r *http.Request) {
	segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// admin / login / <botID> / <uid> / <exp> / <sig>
	if len(segs) != 6 {
		writeAdminLoginError(w, "登录链接格式不对。请在 Telegram 私聊里重新获取。")
		return
	}
	botID, err0 := strconv.ParseInt(segs[2], 10, 64)
	uid, err1 := strconv.ParseInt(segs[3], 10, 64)
	exp, err2 := strconv.ParseInt(segs[4], 10, 64)
	if err0 != nil || err1 != nil || err2 != nil {
		writeAdminLoginError(w, "登录链接格式不对。请在 Telegram 私聊里重新获取。")
		return
	}
	if err := antiad.VerifyAdminLogin(sh, uid, botID, exp, segs[5]); err != nil {
		writeAdminLoginError(w, err.Error()+"。")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     webSessionCookie,
		Value:    antiad.AdminSessionValue(sh, uid),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(sh.Cfg.PublicURL, "https://"),
		Expires:  time.Now().Add(antiad.AdminSessionTTL),
	})
	slog.Info("网页版：登录成功", "uid", uid, "bot", botID)
	http.Redirect(w, r, "/admin/?bot="+strconv.FormatInt(botID, 10), http.StatusFound)
}

// webAdminLogout 清会话 cookie。用 MaxAge=-1 立即失效。
func webAdminLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: webSessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/admin/", http.StatusFound)
}

// writeAdminLoginError 是登录失败页：给一句可操作的中文原因。
func writeAdminLoginError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprintf(w, `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>无法登录</title></head>
<body style="font:15px/1.7 -apple-system,BlinkMacSystemFont,sans-serif;max-width:34em;margin:12vh auto;padding:0 1.5em;color:#1a1a1a">
<h1 style="font-size:20px">无法登录</h1>
<p>%s</p>
<p style="color:#666">在 Telegram 里打开与 bot 的私聊，点菜单里的「网页版」重新获取登录链接。</p>
</body></html>`, html.EscapeString(msg))
}
