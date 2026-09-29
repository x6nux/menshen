package antiad

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// TestWebSecretGeneratedOnce：签名密钥在启动阶段生成一次，重启不变。
func TestWebSecretGeneratedOnce(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)

	if err := EnsureWebSecret(b.Shared); err != nil {
		t.Fatalf("EnsureWebSecret: %v", err)
	}
	first := b.Cache.Snap().Setting("web_secret")
	if len(first) != 64 {
		t.Fatalf("密钥应为 32 字节十六进制（64 字符），得到 %d 字符", len(first))
	}
	if err := EnsureWebSecret(b.Shared); err != nil {
		t.Fatalf("第二次 EnsureWebSecret: %v", err)
	}
	if got := b.Cache.Snap().Setting("web_secret"); got != first {
		t.Error("已存在密钥时不该重新生成")
	}
}

// TestWebSignatureRequiresSecret：密钥缺失时签名与链接都必须为空。
// 退化成「用空密钥签名」的话，任何人都能按公开格式算出合法 HMAC，
// 顺序枚举 apv/<id> 就能读到申诉理由与原文。
func TestWebSignatureRequiresSecret(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	b.Cfg.PublicURL = "https://ad.example.com"

	if sig := appealSig(b.Shared, 1, 555); sig != "" {
		t.Errorf("缺密钥时签名应为空，得到 %q", sig)
	}
	// 空密钥伪造的签名不得通过。
	mac := hmac.New(sha256.New, []byte(""))
	mac.Write([]byte("apv:1"))
	forged := hex.EncodeToString(mac.Sum(nil))[:32]
	if webSigOK(b.Shared, "apv:1", forged) {
		t.Error("空密钥伪造的签名不应通过校验")
	}
	if webSigOK(b.Shared, "apv:1", "") {
		t.Error("空签名不应通过校验")
	}
	if u := LogViewURL(b.Shared, 1); u != "" {
		t.Errorf("缺密钥时不该发查看页链接，得到 %q", u)
	}

	// 生成密钥后恢复正常。
	if err := EnsureWebSecret(b.Shared); err != nil {
		t.Fatal(err)
	}
	if u := LogViewURL(b.Shared, 1); !strings.Contains(u, "/_w/v/1/") {
		t.Errorf("有密钥后应能拼出链接，得到 %q", u)
	}
}

// TestWebSigPurposeIsolation：不同用途的签名互不通用，篡改 id / uid 失败。
func TestWebSigPurposeIsolation(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	if err := EnsureWebSecret(b.Shared); err != nil {
		t.Fatal(err)
	}

	ap := appealSig(b.Shared, 5, 100)
	if !webSigOK(b.Shared, "ap:5:100", ap) {
		t.Fatal("申诉签名应能验证")
	}
	if webSigOK(b.Shared, "apv:5", ap) {
		t.Error("申诉签名不能当申诉详情页签名用")
	}
	if webSigOK(b.Shared, "ap:6:100", ap) {
		t.Error("篡改申诉单号应失败")
	}
	if webSigOK(b.Shared, "ap:5:101", ap) {
		t.Error("篡改用户应失败")
	}
	if !webSigOK(b.Shared, "v:5", logViewSig(b.Shared, 5)) {
		t.Error("查看页签名应能验证")
	}
	if !webSigOK(b.Shared, "vp:5:1700000000", logViewPostSig(b.Shared, 5, 1700000000)) {
		t.Error("查看页 POST 签名应能验证")
	}
}

// TestParseWebRoute：路径里任一段为 _w 即命中，反代套几层子路径都认。
func TestParseWebRoute(t *testing.T) {
	cases := []struct {
		path string
		ok   bool
		kind string
		id   int64
		sig  string
	}{
		{"/_w/ap/12/abc", true, "ap", 12, "abc"},
		{"/tg/_w/v/7/sig", true, "v", 7, "sig"},
		{"/a/b/c/_w/apv/3/x", true, "apv", 3, "x"},
		{"/ap/12/abc", false, "", 0, ""},
		{"/_w/ap/12", false, "", 0, ""},
		{"/_w/ap/0/abc", false, "", 0, ""},
		{"/_w/ap/xx/abc", false, "", 0, ""},
		{"/healthz", false, "", 0, ""},
	}
	for _, c := range cases {
		rt, ok := parseWebRoute(c.path)
		if ok != c.ok || rt.kind != c.kind || rt.id != c.id || rt.sig != c.sig {
			t.Errorf("parseWebRoute(%q) = (%+v, %v)，期望 kind=%q id=%d sig=%q ok=%v",
				c.path, rt, ok, c.kind, c.id, c.sig, c.ok)
		}
	}
}

// TestWebHandlerRejectsBadSigForKnownKind：未实现/未知用途与坏路径都 404；
// 这里只验证路由入口不 panic、签名不符不会泄漏记录存在性。
func TestWebHandlerRejectsBadSigForKnownKind(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	if err := EnsureWebSecret(b.Shared); err != nil {
		t.Fatal(err)
	}
	h := WebHandler(b.Shared)

	for _, p := range []string{"/_w/ap/1/badsig", "/_w/nope/1/x", "/_w/ap/1"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s 应 404，得到 %d", p, w.Code)
		}
	}
}

// TestWhitelistExemptsFromAdExempt：白名单命中时 adExempt 直接放行，
// 不落到要发 TG API 的群管理员判断上。
func TestWhitelistExemptsFromAdExempt(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	if _, err := b.Store.Write.Exec(`INSERT INTO ad_whitelist
		(bot_id,chat_id,user_id,expires_at,source,by_uid,created_at)
		VALUES (0,0,555,0,'adw',1,0)`); err != nil {
		t.Fatal(err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	if !adExempt(b, b.Cache.Snap(), -100, &tg.TGUser{ID: 555}, false) {
		t.Error("全平台白名单应豁免")
	}
	if n := fake.CountCalls("getChatMember"); n != 0 {
		t.Errorf("白名单命中不该再去查群管理员，调了 %d 次", n)
	}
}
