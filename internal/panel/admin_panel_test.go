package panel

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"menshen/internal/antiad"
	"menshen/internal/testutil"
)

// TestAdminPanelLoginAndCookieAuth：签名链接登录 → HttpOnly 会话 cookie →
// 带 X-Web 头的 /admin/api 请求通过；缺头/坏 cookie/坏链接都不放行。
// 同时守「面板与 Mini App 已拆开」：会话 cookie 打不到 /miniapp/api。
func TestAdminPanelLoginAndCookieAuth(t *testing.T) {
	env := newMiniEnv(t)
	env.sh.Cfg.PublicURL = "https://ad.example.com"
	if err := antiad.EnsureWebSecret(env.sh); err != nil {
		t.Fatal(err)
	}
	const adminUID = int64(777) // newTestRegistry 的主管
	handler := AdminPanelHandler(env.sh)

	link := antiad.AdminLoginURL(env.sh, adminUID, testutil.TestBotID)
	if link == "" {
		t.Fatal("应能生成登录链接")
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}

	// 登录：302 + HttpOnly/SameSite=Lax 会话 cookie。
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, u.Path, nil))
	if w.Code != http.StatusFound {
		t.Fatalf("有效链接应 302，得到 %d：%s", w.Code, w.Body.String())
	}
	var sess *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == webSessionCookie {
			sess = c
		}
	}
	if sess == nil || !sess.HttpOnly || sess.SameSite != http.SameSiteLaxMode {
		t.Fatalf("登录应种 HttpOnly/SameSite=Lax cookie，得到 %+v", sess)
	}

	// 面板 API 在 /admin/api：cookie + X-Web → 通过。
	adminReq := func(op string, cookie *http.Cookie, withHeader bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/admin/api/"+op, strings.NewReader("{}"))
		req.Header.Set(miniBotIDHeader, strconv.FormatInt(testutil.TestBotID, 10))
		if withHeader {
			req.Header.Set(webAuthHeader, "1")
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	if w := adminReq("state", sess, true); w.Code != http.StatusOK {
		t.Fatalf("带会话 cookie 应 200，得到 %d：%s", w.Code, w.Body.String())
	}

	// 不带 X-Web 头：CSRF 兜底，直接 403，不落到 cookie 鉴权。
	if w := adminReq("state", sess, false); w.Code != http.StatusForbidden {
		t.Errorf("缺 X-Web 头应 403，得到 %d", w.Code)
	}

	// 篡改 cookie → 401 中文提示。
	bad := *sess
	bad.Value += "x"
	w = adminReq("state", &bad, true)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("篡改 cookie 应 401，得到 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "登录已过期") {
		t.Errorf("401 文案应指路重新获取链接：%s", w.Body.String())
	}

	// 会话 cookie 打不到 Mini App API：面板与 Mini App 的鉴权已分开。
	req := httptest.NewRequest(http.MethodPost, "/miniapp/api/state", strings.NewReader("{}"))
	req.Header.Set(miniBotIDHeader, strconv.FormatInt(testutil.TestBotID, 10))
	req.Header.Set(webAuthHeader, "1")
	req.AddCookie(sess)
	w = httptest.NewRecorder()
	env.h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("Mini App API 不该认网页版会话，得到 %d", w.Code)
	}

	// 坏签名链接 → 403 说明原因。
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/admin/login/42/777/9999999999/deadbeef", nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("坏链接应 403，得到 %d", w.Code)
	}

	// /admin/ 返回面板入口页（前端未构建时是 503 提示页，两种都算壳）。
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/", nil))
	if w.Code != http.StatusOK && w.Code != http.StatusServiceUnavailable {
		t.Errorf("/admin/ 应返回面板壳，得到 %d", w.Code)
	}

	// 退出：清 cookie。
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/logout", nil))
	if w.Code != http.StatusFound {
		t.Errorf("logout 应 302，得到 %d", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == webSessionCookie && c.MaxAge != -1 && c.Value != "" {
			t.Errorf("logout 应清会话 cookie，得到 %+v", c)
		}
	}
}

// TestAdminPanelRoutes：/admin 的边界与 Mini App 一致——API 只收 POST，
// 页面只收 GET/HEAD，含 .. 的路径 404。
func TestAdminPanelRoutes(t *testing.T) {
	env := newMiniEnv(t)
	handler := AdminPanelHandler(env.sh)

	cases := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/admin/api/state", http.StatusMethodNotAllowed},
		{http.MethodGet, "/admin/assets/x.js", http.StatusNotFound},
		{http.MethodPost, "/admin/", http.StatusMethodNotAllowed},
		{http.MethodGet, "/admin/assets/../admin.html", http.StatusNotFound},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))
		if w.Code != c.want {
			t.Errorf("%s %s 应 %d，得到 %d", c.method, c.path, c.want, w.Code)
		}
	}
}
