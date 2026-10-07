package antiad

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"menshen/internal/core"
)

// ---- Cap 协议的内置 Go 服务端 ----
//
// Cap（https://github.com/tiagozip/cap）默认是一台要单独跑的 Bun 服务
// （Challenge/Redis）。这里只用 Go 实现 widget 需要的那套 SHA-256 PoW
// 协议（capjs-core 的 format 1），于是 Cap 无需任何外部实例、也无需配置，
// 随进程即用。密钥从 settings.web_secret 派生，不给部署加新配置项。
//
// 线格式（与官方 widget 对齐）：
//
//	POST <base>challenge   → {challenge:{c,s,d}, token, expires}
//	POST <base>redeem      {token, solutions} → {success, token, expires}
//	POST <base>siteverify  {secret, response} → {success}
//
// <base> = <public_url>/cap/<sitekey>/。token 是 HS256 签名的 JWT，salt 与
// target 由 token 经 FNV-1a + xorshift 派生（见 capPRNG）；widget 端独立
// 算同样的值，所以两边必须完全一致 —— 有测试用官方算法钉住。

const (
	capChallengeCount = 50
	capChallengeSize  = 32
	capChallengeDiff  = 4
	capChallengeTTL   = 10 * time.Minute
	capRedeemTTL      = 20 * time.Minute

	// 与 capjs-core 的上限一致：token 里的参数超出范围一律判无效。
	capMaxCount = 1000
	capMaxSize  = 256
	capMaxDiff  = 16
)

// CapSiteKey 是内置 Cap 服务的站点标识（出现在 widget 端点与页面数据里）。
const CapSiteKey = "menshen"

// CapBaseURL 是内置 Cap 服务对外的基址（widget 的 data-cap-api-endpoint）。
func CapBaseURL(sh *core.Shared) string {
	if !WebAvailable(sh) {
		return ""
	}
	return strings.TrimRight(sh.Cfg.PublicURL, "/") + "/cap/" + CapSiteKey + "/"
}

// capSecret 派生内置 Cap 的签名密钥：不引入新配置，随部署的 web_secret 唯一。
func capSecret(sh *core.Shared) []byte {
	ws := sh.Cache.Snap().Setting("web_secret")
	if ws == "" {
		return nil
	}
	mac := hmac.New(sha256.New, []byte(ws))
	mac.Write([]byte("cap:v1"))
	return mac.Sum(nil)
}

// capEnabled 报告内置 Cap 是否可用（需要网页签名密钥）。
func capEnabled(sh *core.Shared) bool { return capSecret(sh) != nil }

// ---- JWT（HS256，base64url 无填充）与 Cap 的 PRNG ----

func capB64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

var capJWTHeader = capB64([]byte(`{"alg":"HS256","typ":"JWT"}`))

func capJWTSign(payload any, secret []byte) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	si := capJWTHeader + "." + capB64(body)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(si))
	return si + "." + capB64(mac.Sum(nil)), nil
}

// capJWTVerify 校验签名并把载荷解进 out，返回签名原文。
func capJWTVerify(token string, secret []byte, out any) ([]byte, bool) {
	if strings.Count(token, ".") != 2 {
		return nil, false
	}
	last := strings.LastIndexByte(token, '.')
	first := strings.IndexByte(token, '.')
	si := token[:last]
	sig, err := base64.RawURLEncoding.DecodeString(token[last+1:])
	if err != nil {
		return nil, false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(si))
	if subtle.ConstantTimeCompare(mac.Sum(nil), sig) != 1 {
		return nil, false
	}
	body, err := base64.RawURLEncoding.DecodeString(token[first+1 : last])
	if err != nil {
		return nil, false
	}
	if err := json.Unmarshal(body, out); err != nil {
		return nil, false
	}
	return sig, true
}

// capFNV1a 是 JS 版 fnv1a 的逐位复刻。
//
// 不能换成 hash/fnv：JS 的位运算走 int32、累加走 double，与 Go 的 uint32
// 逐位不同。这里按 JS 的语义算，widget 才会算出同样的 salt / target。
func capFNV1a(s string) uint32 { return capFNV1aResume(2166136261, s) }

func capFNV1aResume(state uint32, s string) uint32 {
	h := state
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		hi := int32(h)
		// JS：(hash<<1)+(hash<<4)+(hash<<7)+(hash<<8)+(hash<<24) 以 double 累加。
		sum := int64(hi<<1) + int64(hi<<4) + int64(hi<<7) + int64(hi<<8) + int64(hi<<24)
		h = uint32(int64(hi) + sum)
	}
	return h
}

// capPRNG 复刻 JS 的 prngFromHash：xorshift32，每轮产出 8 位十六进制。
func capPRNG(initial uint32, length int) string {
	state := initial
	var b strings.Builder
	for b.Len() < length {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		fmt.Fprintf(&b, "%08x", state)
	}
	return b.String()[:length]
}

// ---- 挑战与兑换 ----

type capChallengePayload struct {
	N   string `json:"n"`
	C   int    `json:"c"`
	S   int    `json:"s"`
	D   int    `json:"d"`
	Exp int64  `json:"exp"`
	Iat int64  `json:"iat"`
}

// capRedeemPayload 是兑换令牌的载荷。它与挑战不同：挑战证明算力，
// 兑换令牌是已通过的凭据，给入群验证那边核验。
type capRedeemPayload struct {
	N   string `json:"n"`
	Exp int64  `json:"exp"`
	Sk  string `json:"sk"`
}

func capRandHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf)
}

// capNewChallenge 生成一次挑战。
func capNewChallenge(sh *core.Shared) (map[string]any, bool) {
	secret := capSecret(sh)
	if secret == nil {
		return nil, false
	}
	now := time.Now().UnixMilli()
	p := capChallengePayload{
		N: capRandHex(25), C: capChallengeCount, S: capChallengeSize,
		D: capChallengeDiff, Exp: now + capChallengeTTL.Milliseconds(), Iat: now,
	}
	token, err := capJWTSign(p, secret)
	if err != nil {
		return nil, false
	}
	return map[string]any{
		"challenge": map[string]any{"c": p.C, "s": p.S, "d": p.D},
		"token":     token,
		"expires":   p.Exp,
	}, true
}

// capRedeemBody 是 widget 提交的解答。solutions 是数字数组（format 1），
// 用 json.Number 保留下标量原文，才能和 JS 的字符串拼接逐字节对上。
type capRedeemBody struct {
	Token     string        `json:"token"`
	Solutions []json.Number `json:"solutions"`
}

// capValidate 校验一次 PoW 解答，成功返回兑换令牌。
func capValidate(sh *core.Shared, siteKey string, body capRedeemBody) (map[string]any, bool) {
	secret := capSecret(sh)
	if secret == nil {
		return map[string]any{"success": false, "reason": "cap_disabled"}, false
	}
	var p capChallengePayload
	sig, ok := capJWTVerify(body.Token, secret, &p)
	if !ok {
		return map[string]any{"success": false, "reason": "invalid_token"}, false
	}
	if p.Exp < time.Now().UnixMilli() {
		return map[string]any{"success": false, "reason": "expired"}, false
	}
	if p.C < 1 || p.C > capMaxCount || p.S < 1 || p.S > capMaxSize ||
		p.D < 1 || p.D > capMaxDiff {
		return map[string]any{"success": false, "reason": "invalid_token"}, false
	}
	if len(body.Solutions) != p.C {
		return map[string]any{"success": false, "reason": "invalid_solutions"}, false
	}
	base := capFNV1a(body.Token)
	for i := 0; i < p.C; i++ {
		idx := fmt.Sprintf("%d", i+1)
		saltSeed := capFNV1aResume(base, idx)
		targetSeed := capFNV1aResume(saltSeed, "d")
		salt := capPRNG(saltSeed, p.S)
		target := capPRNG(targetSeed, p.D)
		sum := sha256.Sum256([]byte(salt + body.Solutions[i].String()))
		if !strings.HasPrefix(hex.EncodeToString(sum[:]), target) {
			return map[string]any{"success": false, "reason": "invalid_solution"}, false
		}
	}
	// 防重放：同一个挑战只兑换一次。
	if !capConsumeNonce(sh, hex.EncodeToString(sig), capChallengeTTL) {
		return map[string]any{"success": false, "reason": "already_redeemed"}, false
	}
	now := time.Now().UnixMilli()
	rt, err := capJWTSign(capRedeemPayload{
		N: capRandHex(16), Exp: now + capRedeemTTL.Milliseconds(), Sk: siteKey,
	}, secret)
	if err != nil {
		return map[string]any{"success": false, "reason": "internal"}, false
	}
	return map[string]any{"success": true, "token": rt, "expires": now + capRedeemTTL.Milliseconds()}, true
}

// capVerifyToken 校验 widget 拿回的兑换令牌（已通过 + 未用过）。
func capVerifyToken(sh *core.Shared, token string) (bool, string) {
	secret := capSecret(sh)
	if secret == nil {
		return false, "内置 Cap 未启用（缺网页签名密钥）"
	}
	var p capRedeemPayload
	sig, ok := capJWTVerify(token, secret, &p)
	if !ok {
		return false, "令牌无效"
	}
	if p.Exp < time.Now().UnixMilli() {
		return false, "令牌已过期"
	}
	if !capConsumeNonce(sh, hex.EncodeToString(sig), capRedeemTTL) {
		return false, "令牌已被使用"
	}
	return true, ""
}

// capConsumeNonce 用掉一次令牌签名；重复返回 false。
func capConsumeNonce(sh *core.Shared, sigHex string, ttl time.Duration) bool {
	return cachesOf(sh).capNonces.SetNX(sigHex, struct{}{}, ttl)
}

// ---- HTTP ----

// CapHandler 处理内置 Cap 服务的请求，路径形如 /cap/<sitekey>/<action>。
func CapHandler(sh *core.Shared) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		segs := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/cap"), "/"), "/")
		if len(segs) == 0 || segs[0] == "" {
			http.NotFound(w, r)
			return
		}
		action := "challenge"
		if len(segs) > 1 && segs[1] != "" {
			action = segs[1]
		}
		switch action {
		case "challenge":
			capChallengeHTTP(sh, w, r)
		case "redeem":
			capRedeemHTTP(sh, w, r)
		case "siteverify":
			capSiteverifyHTTP(sh, w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

func capWriteJSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func capChallengeHTTP(sh *core.Shared, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ch, ok := capNewChallenge(sh)
	if !ok {
		capWriteJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "cap unavailable"})
		return
	}
	capWriteJSON(w, http.StatusOK, ch)
}

func capRedeemHTTP(sh *core.Shared, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body capRedeemBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).
		Decode(&body); err != nil {
		capWriteJSON(w, http.StatusBadRequest, map[string]any{"success": false, "reason": "invalid_body"})
		return
	}
	res, ok := capValidate(sh, CapSiteKey, body)
	if !ok {
		slog.Info("内置 Cap：兑换未通过", "原因", res["reason"])
	}
	capWriteJSON(w, http.StatusOK, res)
}

func capSiteverifyHTTP(sh *core.Shared, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Secret   string `json:"secret"`
		Response string `json:"response"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).
		Decode(&body); err != nil {
		capWriteJSON(w, http.StatusBadRequest, map[string]any{"success": false})
		return
	}
	// secret 留空即按内置密钥校验；它是给外部调用方复用的兼容字段，
	// 与 Cap Standalone 的 per-key secret 不同（这里没有独立密钥）。
	ok, why := capVerifyToken(sh, body.Response)
	if !ok {
		capWriteJSON(w, http.StatusOK, map[string]any{"success": false, "error": why})
		return
	}
	capWriteJSON(w, http.StatusOK, map[string]any{"success": true})
}
