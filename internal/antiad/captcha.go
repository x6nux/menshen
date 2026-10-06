package antiad

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"menshen/internal/core"
)

// ---- 入群人机验证：验证码提供方的统一入口 ----
//
// 三个提供方共用同一套「网页拿 token → 后端核对」的流程，差别只在端点与
// 字段名。集中在这里，入群验证页、演示页与申诉页不各写一份。
//
//   - turnstile → Cloudflare siteverify
//   - hcaptcha  → hCaptcha siteverify
//   - cap       → 进程内的 Go 实现（capserver.go），不发外部请求
//
// 外部两家一律核对 hostname：令牌本该只由我们自己域名上的页面签出，不符
// 说明令牌是在别处解出来的（把我们的 site key 嵌到自己页面上刷）。

// captchaProvider 是当前配置的验证码提供方。
type captchaProvider struct {
	name     string // turnstile / hcaptcha / cap
	siteKey  string
	secret   string
	minScore int // 0-100，0 = 不检查；只有 hCaptcha Enterprise 有分数
}

func captchaOf(sh *core.Shared) captchaProvider {
	c := sh.Cfg
	key := c.CaptchaSiteKey
	if c.CaptchaProvider == "cap" && key == "" {
		key = CapSiteKey
	}
	return captchaProvider{
		name: c.CaptchaProvider, siteKey: key,
		secret: c.CaptchaSecret, minScore: c.CaptchaMinScore,
	}
}

// enabled 报告提供方是否可用。
//
// cap 是进程内的，只要有网页签名密钥即可用，无需任何配置；外部两家要
// site key + secret。
func (p captchaProvider) enabled(sh *core.Shared) bool {
	switch p.name {
	case "cap":
		return capEnabled(sh)
	case "turnstile", "hcaptcha":
		return p.siteKey != "" && p.secret != ""
	}
	return false
}

// verify 校验令牌，返回 (是否通过, 失败原因)。原因只进日志/演示页。
func (p captchaProvider) verify(sh *core.Shared, token, ip string) (bool, string) {
	switch p.name {
	case "turnstile":
		return verifyTurnstileToken(sh, p.secret, token, ip)
	case "hcaptcha":
		return p.verifyHCaptcha(sh, token, ip)
	case "cap":
		return capVerifyToken(sh, token)
	}
	return false, "未配置验证码提供方"
}

// hcaptchaVerifyURL 是包级变量，测试时替换成 httptest。
var hcaptchaVerifyURL = "https://api.hcaptcha.com/siteverify"

// verifyHCaptcha 校验 hCaptcha 令牌：success / hostname（/ score，若返回）。
func (p captchaProvider) verifyHCaptcha(sh *core.Shared, token, ip string) (bool, string) {
	if token == "" {
		return false, "缺少令牌"
	}
	raw, why := postVerifyForm(hcaptchaVerifyURL, url.Values{
		"secret": {p.secret}, "response": {token}, "remoteip": {ip},
		"sitekey": {p.siteKey},
	})
	if why != "" {
		return false, why
	}
	var out struct {
		Success    bool     `json:"success"`
		Hostname   string   `json:"hostname"`
		Score      float64  `json:"score"`
		ErrorCodes []string `json:"error-codes"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, "siteverify 响应无法解析"
	}
	if !out.Success {
		return false, "success=false " + strings.Join(out.ErrorCodes, ",")
	}
	if host := publicHost(sh); host != "" && out.Hostname != "" && out.Hostname != host {
		return false, "hostname 不符: " + out.Hostname
	}
	// 分数只有 hCaptcha Enterprise 才返回；普通 hCaptcha 没有分数，跳过。
	if p.minScore > 0 && out.Score > 0 && out.Score*100 < float64(p.minScore) {
		return false, fmt.Sprintf("分数 %.2f 低于下限 %d", out.Score, p.minScore)
	}
	return true, ""
}

// postVerifyForm 发一个表单校验请求，返回响应体；失败时第二项为原因。
func postVerifyForm(endpoint string, form url.Values) ([]byte, string) {
	// Transport 为 nil：读 HTTP_PROXY / HTTPS_PROXY 环境变量。
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.PostForm(endpoint, form)
	if err != nil {
		return nil, "siteverify 请求失败: " + err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	return raw, ""
}
