package panel

// 公开网页 SPA 的外壳托管：入群验证页（jv）、申诉验证页（ap）、原文查看页
// （v）、申诉详情页（apv）共用一份 public.html 产物。壳本身不含任何数据，
// 数据接口在 antiad.WebHandler（_w 下的 ?json=1 与 POST），签名校验在那里。
//
// main.go 的路由层用 antiad.IsWebPagePath 判断「GET/HEAD + 无 json=1」的
// 页面请求，先交给本 Handler 发壳。

import (
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	"menshen/internal/core"
)

// PublicShellHandler 托管公开网页 SPA 的入口页；产物缺失时返回 503 与
// 构建提示（与 Mini App 的缺产物行为一致）。
//
// CSP 按配置的入群验证提供方动态组装：浏览器会加载对应的验证码组件，
// 少放行一个来源就会静默拦下它（组件不显示、验证永远不通过）。
func PublicShellHandler(sh *core.Shared) http.Handler {
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
		h.Set("Content-Security-Policy", publicShellCSP(sh))
		h.Set("X-Robots-Tag", "noindex, nofollow")
		h.Set("Referrer-Policy", "no-referrer")
		writeHTML(w, r, http.StatusOK, string(page))
	})
}

// publicShellCSP 组装公开网页的 CSP。
//
//   - 脚本只有同源产物与验证码组件，没有内联脚本，所以不需要 nonce；
//   - MUI/Emotion 在运行时注入 style 标签，style-src 必须放行 inline；
//   - Turnstile 的来源始终放行（申诉页固定用它）；
//   - 再按 config 里配置的入群验证提供方追加来源：reCAPTCHA / hCaptcha
//     各自有固定的脚本与 iframe 域，Cap 的实例地址是用户自填的，运行时
//     取它的 origin；
//   - default-src 'none' 兜底，没显式放行的能力一律没有。
func publicShellCSP(sh *core.Shared) string {
	script := []string{"'self'", "https://challenges.cloudflare.com"}
	frame := []string{"https://challenges.cloudflare.com"}
	connect := []string{"'self'", "https://challenges.cloudflare.com"}
	extra := ""

	provider := ""
	endpoint := ""
	if sh != nil {
		provider = sh.Cfg.CaptchaProvider
		endpoint = sh.Cfg.CaptchaEndpoint
	}
	switch provider {
	case "recaptcha":
		script = append(script, "https://www.google.com", "https://www.gstatic.com")
		frame = append(frame, "https://www.google.com")
		connect = append(connect, "https://www.google.com")
	case "hcaptcha":
		script = append(script, "https://js.hcaptcha.com")
		frame = append(frame, "https://newassets.hcaptcha.com", "https://js.hcaptcha.com")
		connect = append(connect,
			"https://api.hcaptcha.com", "https://hcaptcha.com", "https://*.hcaptcha.com")
	case "cap":
		// 组件与 wasm 从 jsdelivr 拉取；wasm 实例化需要 wasm-unsafe-eval。
		script = append(script, "https://cdn.jsdelivr.net", "'wasm-unsafe-eval'")
		connect = append(connect, "https://cdn.jsdelivr.net")
		if o := endpointOrigin(endpoint); o != "" {
			connect = append(connect, o)
		}
		// Cap 用 Web Worker 跑工作量证明。
		extra = "worker-src 'self' blob:; "
	}

	return "default-src 'none'; " +
		"script-src " + strings.Join(script, " ") + "; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; font-src 'self' data:; " +
		"connect-src " + strings.Join(connect, " ") + "; " +
		"frame-src " + strings.Join(frame, " ") + "; " + extra +
		"base-uri 'none'; form-action 'self'"
}

// publicShellCSPBase 是没有配置入群验证时的 CSP（只有 Turnstile）。
// 测试与文档引用它，避免两处各写一份字面量。
const publicShellCSPBase = "default-src 'none'; " +
	"script-src 'self' https://challenges.cloudflare.com; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self' data:; " +
	"connect-src 'self' https://challenges.cloudflare.com; " +
	"frame-src https://challenges.cloudflare.com; " +
	"base-uri 'none'; form-action 'self'"

// endpointOrigin 取端点 URL 的 origin（scheme://host）。解析失败返回空串。
func endpointOrigin(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
