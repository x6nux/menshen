package panel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPublicShellHandler：公开网页壳带统一 CSP（放行 Turnstile、无内联
// nonce），产物存在时返回 public.html。
//
// CSP 少了收尾引号这类错误会让整个 script-src 静默失效（历史真实事故），
// 所以这里逐段钉住关键指令。
func TestPublicShellHandler(t *testing.T) {
	for _, want := range []string{
		"default-src 'none'",
		"script-src 'self' https://challenges.cloudflare.com",
		"style-src 'self' 'unsafe-inline'",
		"frame-src https://challenges.cloudflare.com",
		"connect-src 'self' https://challenges.cloudflare.com",
	} {
		if !strings.Contains(publicShellCSP, want) {
			t.Errorf("公开网页 CSP 缺少 %q：%s", want, publicShellCSP)
		}
	}
	if strings.Contains(publicShellCSP, "nonce") {
		t.Error("公开网页已无内联脚本，CSP 不该再依赖 nonce")
	}

	if _, ok := miniAppDistFS(); !ok {
		t.Skip("前端未构建，跳过壳内容断言")
	}
	w := httptest.NewRecorder()
	PublicShellHandler().ServeHTTP(w,
		httptest.NewRequest(http.MethodGet, "/x/_w/ap/1/sig", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET 壳应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Security-Policy"); got != publicShellCSP {
		t.Errorf("壳响应应带 CSP，得到 %q", got)
	}
	if !strings.Contains(w.Body.String(), `id="root"`) {
		t.Errorf("壳应返回 public.html：%s", w.Body.String())
	}
}
