package panel

// 公开网页 SPA 的外壳托管：申诉验证页（ap）、原文查看页（v）、申诉详情页
// （apv）共用一份 public.html 产物。壳本身不含任何数据，数据接口在
// antiad.WebHandler（_w 下的 ?json=1 与 POST），签名校验发生在那里。
//
// main.go 的路由层用 antiad.IsWebPagePath 判断「GET/HEAD + 无 json=1」的
// 页面请求，先交给本 Handler 发壳。

import (
	"io/fs"
	"net/http"
)

// publicShellCSP 是公开网页的统一 CSP。
//
//   - 脚本只有同源产物与 Turnstile，没有内联脚本，所以不再需要 nonce；
//   - MUI/Emotion 在运行时注入 style 标签，style-src 必须放行 inline；
//   - Turnstile 需要 script / frame / connect 三处 challenges.cloudflare.com；
//   - default-src 'none' 兜底，没显式放行的能力一律没有。
const publicShellCSP = "default-src 'none'; " +
	"script-src 'self' https://challenges.cloudflare.com; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self' data:; " +
	"connect-src 'self' https://challenges.cloudflare.com; " +
	"frame-src https://challenges.cloudflare.com; " +
	"base-uri 'none'; form-action 'self'"

// PublicShellHandler 托管公开网页 SPA 的入口页；产物缺失时返回 503 与
// 构建提示（与 Mini App 的缺产物行为一致）。
func PublicShellHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dist, ok := miniAppDistFS()
		if !ok {
			writeHTML(w, r, http.StatusServiceUnavailable, miniAppMissingHTML)
			return
		}
		page, err := fs.ReadFile(dist, "public.html")
		if err != nil {
			writeHTML(w, r, http.StatusServiceUnavailable, miniAppMissingHTML)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", publicShellCSP)
		h.Set("X-Robots-Tag", "noindex, nofollow")
		h.Set("Referrer-Policy", "no-referrer")
		writeHTML(w, r, http.StatusOK, string(page))
	})
}
