package panel

// 网页版管理员面板（/admin）：与 Telegram Mini App 分开的独立界面入口。
//
// /admin 与 Mini App 是两条独立链路：
//
//   - 页面壳是独立的 admin.html 产物（Vite base 固定 /admin/，见
//     web/vite.admin.config.ts），浏览器不加载 Telegram SDK；
//   - API 走 /admin/api/*，只认 bot 私聊里签发的 HttpOnly 会话 cookie
//     （见 antiad/adminweb.go），不接受 initData；
//   - 写操作与校验与 Mini App 共用（miniDispatch），管理操作只实现一份。

import (
	"fmt"
	"html"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
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

// webAuthHeader 是网页版 API 请求必须带的头，也是与 Mini App API 的边界：
// 少了它 /admin/api 直接拒绝，避免把带 cookie 的普通表单提交当成合法请求。
const webAuthHeader = "X-Web"

// AdminPanelHandler 处理 /admin 下的网页版管理员面板。
//
//   - GET /admin/login/<botID>/<uid>/<exp>/<sig>：验签后种会话 cookie 并跳 /admin/
//   - GET /admin/logout：清 cookie 回 /admin/
//   - GET /admin/assets/*：面板前端静态资源
//   - POST /admin/api[/…]：面板 API；其余方法 405
//   - 其余 /admin/*：托管面板入口页（SPA 回退）
func AdminPanelHandler(sh *core.Shared) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Path
		// 含 .. 的路径一律 404：path.Clean 会把 .. 解析到 assets 之外。
		if containsDotDot(raw) {
			http.NotFound(w, r)
			return
		}
		p := path.Clean(raw)
		switch {
		case strings.HasPrefix(p, "/admin/login/"):
			webAdminLogin(sh, w, r)
		case p == "/admin/logout":
			webAdminLogout(w, r)
		case p == "/admin/api" || strings.HasPrefix(p, "/admin/api/"):
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			op := ""
			if strings.HasPrefix(p, "/admin/api/") {
				op = strings.TrimPrefix(p, "/admin/api/")
			}
			adminAPI(sh, w, r, op)
		case p == "/admin/assets" || strings.HasPrefix(p, "/admin/assets/"):
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			adminAsset(w, r, p)
		case p == "/admin" || strings.HasPrefix(p, "/admin/"):
			if !allowPageMethod(w, r) {
				return
			}
			// 产物根下的普通文件优先于 SPA 回退；其余路径回退入口页，
			// 刷新/深链不白屏。产物缺失时 adminIndex 自己渲染构建提示页。
			if adminDistFile(w, r, p, "public, max-age=3600") {
				return
			}
			adminIndex(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

// adminAuth 校验网页版会话，返回操作者 uid；失败时已写好响应。
func adminAuth(sh *core.Shared, w http.ResponseWriter, r *http.Request) (int64, bool) {
	// 自定义头是 CSRF 兜底：没有它，带 cookie 的跨站表单就能打到 /admin/api。
	if r.Header.Get(webAuthHeader) != "1" {
		miniErr(w, http.StatusForbidden, "缺少网页版请求头")
		return 0, false
	}
	// 与 Mini App 的 miniAuth 一样先确认目标 bot 存在：部分 op 直接读
	// X-Bot-Id 做范围过滤，未知 id 会让它们按空范围静默返回。
	botID, _ := strconv.ParseInt(r.Header.Get(miniBotIDHeader), 10, 64)
	if sh.Cache.Snap().Bots[botID] == nil {
		miniErr(w, http.StatusBadRequest, "未知的 bot")
		return 0, false
	}
	c, err := r.Cookie(webSessionCookie)
	if err != nil {
		miniErr(w, http.StatusUnauthorized,
			"网页版登录已过期，请在 Telegram 私聊里重新获取登录链接")
		return 0, false
	}
	uid, ok := antiad.VerifyAdminSession(sh, c.Value)
	if !ok {
		miniErr(w, http.StatusUnauthorized,
			"网页版登录已过期，请在 Telegram 私聊里重新获取登录链接")
		return 0, false
	}
	return uid, true
}

// adminAPI 是 /admin/api/* 的分发：会话鉴权后交给与 Mini App 共用的操作实现。
func adminAPI(sh *core.Shared, w http.ResponseWriter, r *http.Request, op string) {
	uid, ok := adminAuth(sh, w, r)
	if !ok {
		return
	}
	miniDispatch(sh, w, r, uid, "", op)
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

// adminIndex 输出面板入口页；产物缺失时返回 503 与构建提示。
func adminIndex(w http.ResponseWriter, r *http.Request) {
	dist, ok := adminDistFS()
	if !ok {
		writeHTML(w, r, http.StatusServiceUnavailable, adminMissingHTML)
		return
	}
	page, err := fs.ReadFile(dist, "admin.html")
	if err != nil {
		http.Error(w, "admin.html 读取失败", http.StatusInternalServerError)
		return
	}
	writeHTML(w, r, http.StatusOK, string(page))
}

// adminDistFile 服务 /admin 产物目录下的普通文件（assets/* 与根下文件共用）。
// 命中并已写出响应返回 true；目录与缺失返回 false，由调用方决定 404 还是回退。
func adminDistFile(w http.ResponseWriter, r *http.Request, p, cache string) bool {
	dist, ok := adminDistFS()
	if !ok {
		return false
	}
	name := strings.TrimPrefix(p, "/admin/")
	info, err := fs.Stat(dist, name)
	if err != nil || info.IsDir() {
		return false
	}
	w.Header().Set("Cache-Control", cache)
	http.StripPrefix("/admin/", http.FileServerFS(dist)).ServeHTTP(w, r)
	return true
}

// adminAsset 输出 /admin/assets/* 下的静态资源：只服务真实存在的普通文件，
// 目录与缺失一律 404，不允许把产物目录列举出去。文件名带内容哈希，长期强缓存。
func adminAsset(w http.ResponseWriter, r *http.Request, p string) {
	if !adminDistFile(w, r, p, "public, max-age=31536000, immutable") {
		http.NotFound(w, r)
	}
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

// adminMissingHTML 是面板产物缺失时的占位页：文案直接给出构建方式。
const adminMissingHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>门神管理面板</title>
</head>
<body style="font:15px/1.7 -apple-system,BlinkMacSystemFont,sans-serif;max-width:34em;margin:12vh auto;padding:0 1.5em;color:#1a1a1a">
<h1 style="font-size:20px">管理面板未构建</h1>
<p>当前二进制没有内嵌管理面板前端产物。</p>
<p>本地开发：<code>npm --prefix web run build</code> 后重新 <code>go build</code>；<br>
部署构建：<code>docker build</code>（Dockerfile 会先构建前端再嵌入）。</p>
</body>
</html>`
