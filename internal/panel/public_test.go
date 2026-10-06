package panel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"menshen/internal/config"
	"menshen/internal/core"
)

// TestPublicShellHandler：公开网页壳带统一 CSP（放行 Turnstile、无内联
// nonce），产物存在时返回 public.html。
//
// CSP 少了收尾引号这类错误会让整个 script-src 静默失效（历史真实事故），
// 所以这里逐段钉住关键指令。
func TestPublicShellHandler(t *testing.T) {
	base := publicShellCSP(nil)
	for _, want := range []string{
		"default-src 'none'",
		"script-src 'self' https://challenges.cloudflare.com",
		"style-src 'self' 'unsafe-inline'",
		"frame-src https://challenges.cloudflare.com",
		"connect-src 'self' https://challenges.cloudflare.com",
	} {
		if !strings.Contains(base, want) {
			t.Errorf("公开网页 CSP 缺少 %q：%s", want, base)
		}
	}
	if strings.Contains(base, "nonce") {
		t.Error("公开网页已无内联脚本，CSP 不该再依赖 nonce")
	}
	if base != publicShellCSPBase {
		t.Errorf("无配置时应等于基准 CSP，得到 %s", base)
	}

	if _, ok := miniAppDistFS(); !ok {
		t.Skip("前端未构建，跳过壳内容断言")
	}
	sh := &core.Shared{Cfg: &config.Config{}}
	w := httptest.NewRecorder()
	PublicShellHandler(sh).ServeHTTP(w,
		httptest.NewRequest(http.MethodGet, "/x/_w/ap/1/sig", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET 壳应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Security-Policy"); got != base {
		t.Errorf("壳响应应带 CSP，得到 %q", got)
	}
	if !strings.Contains(w.Body.String(), `id="root"`) {
		t.Errorf("壳应返回 public.html：%s", w.Body.String())
	}
}

// TestPublicShellCSPProviders：入群验证的提供方要各自放行其脚本 / iframe /
// 连接来源。少放行一个来源时组件静默不显示，验证永远不通过。
func TestPublicShellCSPProviders(t *testing.T) {
	sh := &core.Shared{Cfg: &config.Config{
		CaptchaProvider: "hcaptcha",
		CaptchaDemo:     true, // 演示页恒定带内置 cap
	}}
	got := publicShellCSP(sh)
	for _, want := range []string{
		"https://js.hcaptcha.com", "https://newassets.hcaptcha.com",
		"https://api.hcaptcha.com",
		// cap（内置）也要放行 CDN 与 worker
		"https://cdn.jsdelivr.net", "'wasm-unsafe-eval'", "worker-src 'self' blob:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("CSP 缺少 %q：%s", want, got)
		}
	}
	if strings.Contains(got, "google.com") {
		t.Errorf("已移除 reCAPTCHA，CSP 不该再放行 google：%s", got)
	}
}
