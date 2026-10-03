package antiad

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"menshen/internal/testutil"
)

// TestAdminLoginAndSession：登录链接与会话 cookie 的签名、过期与角色门槛。
func TestAdminLoginAndSession(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1) // 1 是主管
	b.Cfg.PublicURL = "https://ad.example.com"
	if err := EnsureWebSecret(b.Shared); err != nil {
		t.Fatal(err)
	}

	url := AdminLoginURL(b.Shared, 1, 42)
	if !strings.HasPrefix(url, "https://ad.example.com/admin/login/42/1/") {
		t.Fatalf("登录链接形状不对：%s", url)
	}
	segs := strings.Split(strings.TrimPrefix(url, "https://ad.example.com/admin/login/"), "/")
	if len(segs) != 4 {
		t.Fatalf("登录链接应带 botID/uid/exp/sig：%s", url)
	}
	exp, _ := strconv.ParseInt(segs[2], 10, 64)
	if err := VerifyAdminLogin(b.Shared, 1, 42, exp, segs[3]); err != nil {
		t.Fatalf("有效登录链接应通过：%v", err)
	}
	if err := VerifyAdminLogin(b.Shared, 1, 42, exp, "deadbeef"); err == nil {
		t.Error("坏签名不该通过")
	}
	past := time.Now().Add(-time.Minute).Unix()
	if err := VerifyAdminLogin(b.Shared, 1, 42, past, adminLoginSig(b.Shared, 1, 42, past)); err == nil ||
		!strings.Contains(err.Error(), "过期") {
		t.Errorf("过期链接应提示过期，得到 %v", err)
	}
	if err := VerifyAdminLogin(b.Shared, 999, 42, exp,
		adminLoginSig(b.Shared, 999, 42, exp)); err == nil ||
		!strings.Contains(err.Error(), "管理员") {
		t.Errorf("非管理员应被拒绝，得到 %v", err)
	}

	// 会话 cookie：正常通过，篡改/过期不通过。
	val := AdminSessionValue(b.Shared, 1)
	if uid, ok := VerifyAdminSession(b.Shared, val); !ok || uid != 1 {
		t.Errorf("有效会话应通过，uid=%d ok=%v", uid, ok)
	}
	if _, ok := VerifyAdminSession(b.Shared, val+"x"); ok {
		t.Error("篡改会话不该通过")
	}
	sessPast := time.Now().Add(-time.Minute).Unix()
	if _, ok := VerifyAdminSession(b.Shared,
		fmt.Sprintf("1:%d:%s", sessPast, adminSessionSig(b.Shared, 1, sessPast))); ok {
		t.Error("过期会话不该通过")
	}

	// 网页子系统不可用（没有 public_url）时不发链接。
	b2, _ := testutil.NewTestBot(t, 1)
	if u := AdminLoginURL(b2.Shared, 1, 42); u != "" {
		t.Errorf("网页不可用时不该生成登录链接，得到 %q", u)
	}
}
