package antiad

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"menshen/internal/core"
)

// ---- 入群人机验证：验证码提供方的统一入口 ----
//
// 三个提供方共用同一套“网页拿 token → 后端核对”的流程，差别只在端点与
// 字段名。集中在这里，入群验证页、演示页与申诉页不各写一份。
//
//   - turnstile → Cloudflare siteverify
//   - hcaptcha  → hCaptcha siteverify
//   - cap       → 进程内的 Go 实现（capserver.go），不发外部请求
//
// 外部两家一律核对 hostname：令牌本该只由我们自己域名上的页面签出，不符
// 说明令牌是在别处解出来的（把我们的 site key 嵌到自己页面上刷）。

// captchaProvider 是当前配置的验证码提供方。全部从 settings 表现读
// （面板上改完即生效），不走 config.yaml。
type captchaProvider struct {
	name     string // turnstile / hcaptcha / cap；空 = 未启用
	siteKey  string
	secret   string
	minScore int // 0-100，0 = 不检查；只有 hCaptcha Enterprise 有分数
}

func captchaOf(sh *core.Shared) captchaProvider {
	snap := sh.Cache.Snap()
	key := snap.Setting("captcha_site_key")
	if snap.Setting("captcha_provider") == "cap" && key == "" {
		key = CapSiteKey
	}
	return captchaProvider{
		name:     snap.Setting("captcha_provider"),
		siteKey:  key,
		secret:   snap.Setting("captcha_secret"),
		minScore: int(snap.SettingInt("captcha_min_score", 0)),
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

// captchaHostOK 报告 siteverify 返回的 hostname 是否可接受。
//
// hCaptcha 文档明确：hostname 由用户浏览器派生，“不得用于任何鉴权”，
// 高峰期还可能直接返回 "not-provided"；真正绑定域名的是 sitekey 的
// Domain allowlist（不匹配时 siteverify 自己 success=false）。
// 它对子域页面返回的是可注册域（本站主机名的父域），严格相等会把
// 正常用户拦下。
//
// 规则：相等、是本站主机名的父域（本站主机名以 .它 结尾）、或对端没给
// 有效值，都算通过。反向不成立：令牌在别人域上解出时，返回值不可能是
// 本站主机名的父域。
func captchaHostOK(returned, host string) bool {
	if host == "" || returned == "" || strings.EqualFold(returned, "not-provided") {
		return true
	}
	if strings.EqualFold(returned, host) {
		return true
	}
	return strings.HasSuffix(strings.ToLower(host), "."+strings.ToLower(returned))
}

// verifyHCaptcha 校验 hCaptcha 令牌：success（/ score，若返回）；
// hostname 按 captchaHostOK 宽松核对。
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
	if !captchaHostOK(out.Hostname, publicHost(sh)) {
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

// ---- 面板写入用的校验（TG 面板与 Mini App 共用，见 panel/ops_settings.go）----

// captchaProviders 是入群验证支持的提供方白名单（cap 为内置实现）。
var captchaProviders = map[string]bool{
	"turnstile": true, "hcaptcha": true, "cap": true,
}

// CaptchaProviderValid 校验提供方名；空串合法（= 入群验证关闭）。
func CaptchaProviderValid(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return true
	}
	return captchaProviders[name]
}

// CaptchaCred 是一组测试台验证码凭据。
type CaptchaCred struct{ SiteKey, Secret string }

// parseCaptchaDemoKeys 解析 captcha_demo_keys 设置值：
//
//	provider=site_key,secret;provider=...
//
// cap 是内置实现（无需密钥），写了也忽略。
func parseCaptchaDemoKeys(v string) (map[string]CaptchaCred, error) {
	out := map[string]CaptchaCred{}
	v = strings.TrimSpace(v)
	if v == "" {
		return out, nil
	}
	for _, part := range strings.Split(v, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, rest, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("片段缺少 '='：%q", part)
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "cap" {
			continue
		}
		if name != "turnstile" && name != "hcaptcha" {
			return nil, fmt.Errorf("未知提供方 %q（可用 turnstile / hcaptcha；cap 内置无需配置）", name)
		}
		f := strings.Split(rest, ",")
		if len(f) != 2 {
			return nil, fmt.Errorf("%s 需要 site_key,secret 两段：%q", name, part)
		}
		cred := CaptchaCred{SiteKey: strings.TrimSpace(f[0]), Secret: strings.TrimSpace(f[1])}
		if cred.SiteKey == "" || cred.Secret == "" {
			return nil, fmt.Errorf("%s 缺少 site key 或 secret", name)
		}
		out[name] = cred
	}
	return out, nil
}

// ValidateCaptchaDemoKeys 校验 captcha_demo_keys 设置值（面板写入时用）。
func ValidateCaptchaDemoKeys(v string) error {
	_, err := parseCaptchaDemoKeys(v)
	return err
}

// CaptchaDemoOn 报告人机验证测试台是否开启（settings.captcha_demo）。
func CaptchaDemoOn(sh *core.Shared) bool {
	return sh.Cache.Snap().SettingInt("captcha_demo", 0) == 1
}

// CaptchaProviderName 是当前入群验证提供方名（空 = 关闭）。
func CaptchaProviderName(sh *core.Shared) string {
	return sh.Cache.Snap().Setting("captcha_provider")
}

// CaptchaDemoProviderNames 返回测试台配置的外部提供方名（CSP 组装用）。
// 设置值无法解析时返回 nil：坏值不该把整页 CSP 撑坏。
func CaptchaDemoProviderNames(sh *core.Shared) []string {
	keys, err := parseCaptchaDemoKeys(sh.Cache.Snap().Setting("captcha_demo_keys"))
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(keys))
	for name := range keys {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
