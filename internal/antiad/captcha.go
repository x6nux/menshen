package antiad

import (
	"bytes"
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
// 四个提供方共用同一套「网页拿 token → 后端向提供方 siteverify 核对」的
// 流程，差别只在端点、字段名与响应形状。集中在这里，入群验证页与申诉页
// 不各写一份。
//
// 校验一律核对 hostname：令牌本该只由我们自己域名上的页面签出，hostname
// 不符说明令牌是在别处解出来的（把我们的 site key 嵌到自己页面上刷）。

// captchaProvider 是当前配置的验证码提供方。
type captchaProvider struct {
	name     string // turnstile / recaptcha / hcaptcha / cap
	siteKey  string
	secret   string
	endpoint string // 只有 cap 用
	minScore int    // 0-100，0 = 不检查；只有 recaptcha / hcaptcha 有分数
}

func captchaOf(sh *core.Shared) captchaProvider {
	c := sh.Cfg
	return captchaProvider{
		name: c.CaptchaProvider, siteKey: c.CaptchaSiteKey,
		secret: c.CaptchaSecret, endpoint: c.CaptchaEndpoint,
		minScore: c.CaptchaMinScore,
	}
}

// enabled 报告提供方配置是否齐全（与 Config.CaptchaEnabled 同一判据）。
func (p captchaProvider) enabled() bool {
	if p.name == "" || p.siteKey == "" || p.secret == "" {
		return false
	}
	if p.name == "cap" && p.endpoint == "" {
		return false
	}
	return true
}

// verify 校验令牌，返回 (是否通过, 失败原因)。原因只进日志，不返回给用户。
func (p captchaProvider) verify(sh *core.Shared, token, ip string) (bool, string) {
	switch p.name {
	case "turnstile":
		return verifyTurnstileToken(sh, p.secret, token, ip)
	case "recaptcha":
		return p.verifyHostScore(sh, recaptchaVerifyURL, url.Values{
			"secret": {p.secret}, "response": {token}, "remoteip": {ip},
		}, token)
	case "hcaptcha":
		return p.verifyHostScore(sh, hcaptchaVerifyURL, url.Values{
			"secret": {p.secret}, "response": {token}, "remoteip": {ip},
			"sitekey": {p.siteKey},
		}, token)
	case "cap":
		return p.verifyCap(token)
	}
	return false, "未配置验证码提供方"
}

// recaptchaVerifyURL / hcaptchaVerifyURL 是包级变量，测试时替换成 httptest。
var (
	recaptchaVerifyURL = "https://www.google.com/recaptcha/api/siteverify"
	hcaptchaVerifyURL  = "https://api.hcaptcha.com/siteverify"
)

// verifyHostScore 处理 reCAPTCHA 与 hCaptcha 共有的响应形状：
// success / hostname / score / error-codes。
func (p captchaProvider) verifyHostScore(sh *core.Shared, endpoint string,
	form url.Values, token string) (bool, string) {

	if token == "" {
		return false, "缺少令牌"
	}
	raw, why := postVerifyForm(endpoint, form)
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
	// score 只有 reCAPTCHA v3 / hCaptcha Enterprise 才返回；v2 与普通
	// hCaptcha 没有分数，此时不做分数判定（否则会把所有 v2 用户拦下）。
	if p.minScore > 0 && out.Score > 0 && out.Score*100 < float64(p.minScore) {
		return false, fmt.Sprintf("分数 %.2f 低于下限 %d", out.Score, p.minScore)
	}
	return true, ""
}

// verifyCap 向自建 Cap 实例校验令牌。
//
// Cap Standalone 的校验端点是 <endpoint>/<site_key>/siteverify，body 为
// JSON {"secret","response"}，成功返回 {"success":true}
// （见 https://github.com/tiagozip/cap 的 standalone 文档）。
func (p captchaProvider) verifyCap(token string) (bool, string) {
	if token == "" {
		return false, "缺少令牌"
	}
	u := strings.TrimRight(p.endpoint, "/") + "/" +
		url.PathEscape(p.siteKey) + "/siteverify"
	body, _ := json.Marshal(map[string]string{"secret": p.secret, "response": token})
	raw, why := postVerifyJSON(u, body)
	if why != "" {
		return false, why
	}
	var out struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, "siteverify 响应无法解析"
	}
	if !out.Success {
		return false, "success=false"
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

// postVerifyJSON 发一个 JSON 校验请求。
func postVerifyJSON(endpoint string, body []byte) ([]byte, string) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, "siteverify 请求失败: " + err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	return raw, ""
}
