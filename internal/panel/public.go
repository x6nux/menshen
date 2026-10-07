package panel

// 公开网页 SPA 的外壳托管：入群验证页（jv）、申诉验证页（ap）、原文查看页
// （v）、申诉详情页（apv）共用一份 public.html 产物。壳本身不含任何数据，
// 数据接口在 antiad.WebHandler（_w 下的 ?json=1 与 POST），签名校验在那里。
//
// main.go 的路由层用 antiad.IsWebPagePath 判断 GET/HEAD 且无 json=1 的
// 页面请求，先交给本 Handler 发壳。

import (
	"io/fs"
	"net/http"
	"strings"

	"menshen/internal/antiad"
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
//   - 再按 config 里出现的提供方追加来源：captcha_provider 与演示页
//     captcha_demo_keys 里列出的每一家。演示页可能一次展示多家，少放行一个
//     来源时那个组件会静默不显示、验证永远不通过；
//   - Cap 是内置的 Go 服务（组件从 jsdelivr 拉、挑战走同源），只放行 CDN；
//   - default-src 'none' 兜底，没显式放行的能力一律没有。
func publicShellCSP(sh *core.Shared) string {
	script := []string{"'self'", "https://challenges.cloudflare.com"}
	frame := []string{"https://challenges.cloudflare.com"}
	connect := []string{"'self'", "https://challenges.cloudflare.com"}
	extra := ""

	provs := map[string]bool{}
	if sh != nil {
		// 提供方与测试台都存在 settings 表里（网页面板配置），现读现拼：
		// 面板上换一家或补一组密钥，下一个请求的 CSP 就跟上。
		if p := antiad.CaptchaProviderName(sh); p != "" {
			provs[p] = true
		}
		for _, name := range antiad.CaptchaDemoProviderNames(sh) {
			provs[name] = true
		}
		// 演示页恒定带上内置 Cap（无需密钥），所以要一并放行它的来源。
		if antiad.CaptchaDemoOn(sh) {
			provs["cap"] = true
		}
	}
	if provs["hcaptcha"] {
		script = append(script, "https://js.hcaptcha.com")
		frame = append(frame, "https://newassets.hcaptcha.com", "https://js.hcaptcha.com")
		connect = append(connect,
			"https://api.hcaptcha.com", "https://hcaptcha.com", "https://*.hcaptcha.com")
	}
	if provs["cap"] {
		// 组件从 jsdelivr 拉取；挑战 / 兑换走同源（'self' 已放行）。
		script = append(script, "https://cdn.jsdelivr.net", "'wasm-unsafe-eval'")
		connect = append(connect, "https://cdn.jsdelivr.net")
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
