package antiad

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"menshen/internal/core"
	"menshen/internal/testutil"
)

// capGolden 是用官方 JS 实现（capjs-core 的 prng.js + crypto.js sha256）
// 离线算出来的向量。把 Go 的移植钉在它上面 —— 只要 PRNG 与 FNV-1a 有
// 一点位运算差异，widget 端算出的 salt/target 就对不上，验证必然失败。
var capGolden = []struct {
	token  string
	i      int
	salt   string
	target string
	nonce  int
}{
	{"tok-abc", 1, "b178c9b5a1898fd29e037a8bdb1e2662", "b888", 4620},
	{"tok-abc", 2, "a86595e741f669eb15a2be4e0f00b01b", "f846", 56022},
	{"tok-abc", 50, "63d83252d7da53fb290ad8093373e08c", "1df2", 31898},
	{"eyJhbGciOiJIUzI1NiJ9.payload.sig", 1,
		"37b10ec4db593bf078cff983780495fc", "8084", 96958},
}

func TestCapPRNGGolden(t *testing.T) {
	if got := capFNV1a("tok-abc"); got != 503322256 {
		t.Errorf("fnv1a(tok-abc) = %d, 期望 503322256", got)
	}
	if got := capFNV1a(""); got != 2166136261 {
		t.Errorf("fnv1a(\"\") = %d, 期望 2166136261", got)
	}
	for _, g := range capGolden {
		saltSeed := capFNV1aResume(capFNV1a(g.token), strconv.Itoa(g.i))
		targetSeed := capFNV1aResume(saltSeed, "d")
		if got := capPRNG(saltSeed, 32); got != g.salt {
			t.Errorf("%s/%d salt = %s, 期望 %s", g.token, g.i, got, g.salt)
		}
		if got := capPRNG(targetSeed, 4); got != g.target {
			t.Errorf("%s/%d target = %s, 期望 %s", g.token, g.i, got, g.target)
		}
		sum := sha256.Sum256([]byte(g.salt + strconv.Itoa(g.nonce)))
		if !strings.HasPrefix(hex.EncodeToString(sum[:]), g.target) {
			t.Errorf("%s/%d 黄金 nonce %d 应满足 target %s", g.token, g.i, g.nonce, g.target)
		}
	}
}

// capSolve 用与服务端相同的派生逻辑解一道挑战（模拟测试里的客户端）。
func capSolve(t *testing.T, token string, c, s, d int) []json.Number {
	t.Helper()
	base := capFNV1a(token)
	out := make([]json.Number, c)
	for i := 0; i < c; i++ {
		saltSeed := capFNV1aResume(base, strconv.Itoa(i+1))
		salt := capPRNG(saltSeed, s)
		target := capPRNG(capFNV1aResume(saltSeed, "d"), d)
		nonce := 0
		for {
			sum := sha256.Sum256([]byte(salt + strconv.Itoa(nonce)))
			if strings.HasPrefix(hex.EncodeToString(sum[:]), target) {
				break
			}
			nonce++
		}
		out[i] = json.Number(strconv.Itoa(nonce))
	}
	return out
}

// capTestShared 建一个可用内置 Cap 的 Shared（需要 web_secret）。
func capTestShared(t *testing.T) *core.Shared {
	t.Helper()
	b, _ := testutil.NewTestBot(t, 1)
	b.Cfg.PublicURL = "https://ad.example.com"
	if err := EnsureWebSecret(b.Shared); err != nil {
		t.Fatal(err)
	}
	return b.Shared
}

func TestCapChallengeRedeemVerify(t *testing.T) {
	sh := capTestShared(t)
	h := CapHandler(sh)

	// 1) 取挑战
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/cap/menshen/challenge", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("challenge 应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	var ch struct {
		Challenge struct{ C, S, D int } `json:"challenge"`
		Token     string                `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ch); err != nil || ch.Token == "" {
		t.Fatalf("challenge 响应无法解析：%s", w.Body.String())
	}
	if ch.Challenge.C != capChallengeCount || ch.Challenge.D != capChallengeDiff {
		t.Fatalf("挑战参数不对：%+v", ch.Challenge)
	}

	// 2) 解算并兑换
	sol := capSolve(t, ch.Token, ch.Challenge.C, ch.Challenge.S, ch.Challenge.D)
	body, _ := json.Marshal(map[string]any{"token": ch.Token, "solutions": sol})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/cap/menshen/redeem",
		strings.NewReader(string(body))))
	var rd struct {
		Success bool   `json:"success"`
		Token   string `json:"token"`
		Reason  string `json:"reason"`
	}
	json.Unmarshal(w.Body.Bytes(), &rd)
	if !rd.Success || rd.Token == "" {
		t.Fatalf("兑换应成功，得到 %s", w.Body.String())
	}

	// 3) siteverify 校验兑换令牌
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/cap/menshen/siteverify",
		strings.NewReader(`{"secret":"","response":"`+rd.Token+`"}`)))
	if !strings.Contains(w.Body.String(), `"success":true`) {
		t.Fatalf("siteverify 应通过，得到 %s", w.Body.String())
	}
	// 4) 同一个令牌不能再用（防重放）
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/cap/menshen/siteverify",
		strings.NewReader(`{"secret":"","response":"`+rd.Token+`"}`)))
	if strings.Contains(w.Body.String(), `"success":true`) {
		t.Error("兑换令牌重复使用应被拒绝")
	}
}

func TestCapRedeemBadSolutions(t *testing.T) {
	sh := capTestShared(t)
	ch, ok := capNewChallenge(sh)
	if !ok {
		t.Fatal("挑战生成失败")
	}
	token := ch["token"].(string)
	// 全 0 的解答几乎必然不满足 PoW。
	sol := make([]int, capChallengeCount)
	body, _ := json.Marshal(map[string]any{"token": token, "solutions": sol})
	w := httptest.NewRecorder()
	CapHandler(sh).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/cap/menshen/redeem",
		strings.NewReader(string(body))))
	if !strings.Contains(w.Body.String(), `"success":false`) {
		t.Errorf("错误解答应失败，得到 %s", w.Body.String())
	}
}

func TestCapVerifyTokenRejectsTampered(t *testing.T) {
	sh := capTestShared(t)
	if ok, _ := capVerifyToken(sh, "not-a-token"); ok {
		t.Error("非法令牌不应通过")
	}
	// 用错误的密钥签一个看起来合法的令牌。
	bad, _ := capJWTSign(capRedeemPayload{N: "x", Exp: 1 << 62}, []byte("other-secret-000000"))
	if ok, _ := capVerifyToken(sh, bad); ok {
		t.Error("签名不匹配的令牌不应通过")
	}
}

func TestCapDisabledWithoutWebSecret(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	// 没有 web_secret：挑战生成失败。
	if _, ok := capNewChallenge(b.Shared); ok {
		t.Error("缺网页密钥时不应能生成挑战")
	}
	w := httptest.NewRecorder()
	CapHandler(b.Shared).ServeHTTP(w,
		httptest.NewRequest(http.MethodPost, "/cap/menshen/challenge", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("缺密钥应 503，得到 %d", w.Code)
	}
}

// TestCaptchaProviderEnabled：cap 无需任何配置即可用；turnstile/hcaptcha
// 需要 site key + secret。
func TestCaptchaProviderEnabled(t *testing.T) {
	sh := capTestShared(t)
	if !(captchaProvider{name: "cap"}).enabled(sh) {
		t.Error("内置 cap 应当可用")
	}
	if (captchaProvider{name: "turnstile"}).enabled(sh) {
		t.Error("缺密钥的 turnstile 不应可用")
	}
	if !(captchaProvider{name: "hcaptcha", siteKey: "s", secret: "k"}).enabled(sh) {
		t.Error("配齐的 hcaptcha 应当可用")
	}
}
