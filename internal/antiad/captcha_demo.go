package antiad

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"menshen/internal/core"
)

// ---- 人机验证演示页（_w/demo）----
//
// 一次展示并校验四家验证方式，用来在真机上逐个试通。只在 captcha_demo=1
// 时可用，生产保持关闭。页面只含各家的 **site key**（公开信息），secret
// 永不出服务端；校验走与入群验证同一条 captchaProvider.verify，所以这里
// 通过即代表入群验证那条路也通。

// demoProviderOrder 是演示顺序。
var demoProviderOrder = []string{"turnstile", "hcaptcha", "cap"}

// captchaDemoProvider 按名字取演示提供方。
//
// turnstile / hcaptcha 用 captcha_demo_keys 设置里的凭据；cap 是进程内
// 实现，无需任何配置，恒定可用。
func captchaDemoProvider(sh *core.Shared, name string) (captchaProvider, bool) {
	if name == "cap" {
		if !capEnabled(sh) {
			return captchaProvider{}, false
		}
		return captchaProvider{name: "cap", siteKey: CapSiteKey}, true
	}
	keys, err := parseCaptchaDemoKeys(sh.Cache.Snap().Setting("captcha_demo_keys"))
	if err != nil {
		return captchaProvider{}, false
	}
	c, ok := keys[name]
	if !ok {
		return captchaProvider{}, false
	}
	return captchaProvider{name: name, siteKey: c.SiteKey, secret: c.Secret}, true
}

// handleCaptchaDemoPage 处理演示页的 GET ?json=1（返回各家 site key）与
// POST {provider,token,signals}（服务端校验）。
func handleCaptchaDemoPage(sh *core.Shared, w http.ResponseWriter, r *http.Request, _ webRoute) {
	if !CaptchaDemoOn(sh) {
		writeWebJSON(w, http.StatusNotFound, map[string]any{"error": "链接无效或已被替换。"})
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Query().Get("json") == "1":
		writeWebJSON(w, http.StatusOK, captchaDemoData(sh))
	case r.Method == http.MethodPost:
		captchaDemoPost(sh, w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// captchaDemoData 列出已配置的提供方。未配 key 的不出现 —— 页面不必为
// 一个必然失败的方式留位置。
func captchaDemoData(sh *core.Shared) map[string]any {
	out := []map[string]any{}
	for _, name := range demoProviderOrder {
		p, ok := captchaDemoProvider(sh, name)
		if !ok {
			continue
		}
		item := map[string]any{"provider": name, "sitekey": p.siteKey}
		if name == "cap" {
			// 内置 Cap 的 widget 端点在进程内，不是外部实例。
			item["endpoint"] = CapBaseURL(sh)
		}
		out = append(out, item)
	}
	return map[string]any{"providers": out}
}

// captchaDemoPost 校验一次提交。返回通过与否和原因 —— 演示页要的就是
// 能看到失败原因，便于判断是密钥、域名白名单还是组件的问题。
func captchaDemoPost(sh *core.Shared, w http.ResponseWriter, r *http.Request) {
	ip := clientIP(sh, r)
	if !sh.AdLimits.Allow("web:demo:"+ip, 30) {
		writeWebJSON(w, http.StatusTooManyRequests, map[string]any{
			"ok": false, "msg": "请求太频繁，请稍后再试。"})
		return
	}
	var body struct {
		Provider string     `json:"provider"`
		Token    string     `json:"token"`
		Signals  webSignals `json:"signals"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).
		Decode(&body); err != nil {
		writeWebJSON(w, http.StatusBadRequest, map[string]any{
			"ok": false, "msg": "请求体无法解析。"})
		return
	}
	p, ok := captchaDemoProvider(sh, body.Provider)
	if !ok {
		writeWebJSON(w, http.StatusBadRequest, map[string]any{
			"ok": false, "msg": "未知或未配置的验证方式。"})
		return
	}
	if len(body.Token) == 0 {
		writeWebJSON(w, http.StatusOK, map[string]any{
			"ok": false, "msg": "组件没有返回令牌，可能没加载出来。"})
		return
	}

	ua := r.Header.Get("User-Agent")
	hard, soft := evalSignals(ua, body.Signals)
	if len(hard) > 0 {
		writeWebJSON(w, http.StatusOK, map[string]any{
			"ok": false, "msg": "命中自动化特征：" + strings.Join(hard, "；")})
		return
	}
	if ok, why := p.verify(sh, body.Token, ip); !ok {
		slog.Info("人机验证演示：未通过", "提供方", p.name, "原因", why)
		writeWebJSON(w, http.StatusOK, map[string]any{
			"ok": false, "msg": p.name + " 未通过：" + why})
		return
	}
	msg := p.name + " 验证通过"
	if len(soft) > 0 {
		msg += "（软信号：" + strings.Join(soft, "；") + "）"
	}
	slog.Info("人机验证演示：通过", "提供方", p.name)
	writeWebJSON(w, http.StatusOK, map[string]any{"ok": true, "msg": msg})
}

// ---- 申诉验证演示页（_w/apdemo）----
//
// 与人机验证演示页（_w/demo）同一套测试台：校验走与申诉页完全相同的
// verifyTurnstile（config 的 Turnstile 密钥、action / cdata / hostname
// 全部照查），所以这里通过即代表申诉验证那条路也通。申诉页本身只回
// 一句`验证未通过`，排查密钥、域名白名单或 cdata 绑定问题时，具体
// 原因全靠这页显示。
//
// cdata 取路由里的 <id>（任意正整数即可），不落库、不影响任何真实申诉单。

// handleAppealDemoPage 处理 GET ?json=1（sitekey 与 cdata）与 POST 校验。
func handleAppealDemoPage(sh *core.Shared, w http.ResponseWriter, r *http.Request, rt webRoute) {
	if !CaptchaDemoOn(sh) {
		writeWebJSON(w, http.StatusNotFound, map[string]any{"error": "链接无效或已被替换。"})
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Query().Get("json") == "1":
		writeWebJSON(w, http.StatusOK, map[string]any{
			"sitekey": sh.Cfg.TurnstileSiteKey,
			"cdata":   strconv.FormatInt(rt.id, 10),
		})
	case r.Method == http.MethodPost:
		appealDemoPost(sh, w, r, rt.id)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// appealDemoPost 校验一次提交，与人机验证演示页一样把失败原因显示出来。
func appealDemoPost(sh *core.Shared, w http.ResponseWriter, r *http.Request, appealID int64) {
	if sh.Cfg.TurnstileSiteKey == "" || sh.Cfg.TurnstileSecret == "" {
		writeWebJSON(w, http.StatusOK, map[string]any{
			"ok": false,
			"msg": "服务端未配置申诉验证的 Turnstile 密钥" +
				"（config.yaml 的 turnstile_site_key / turnstile_secret）。",
		})
		return
	}
	ip := clientIP(sh, r)
	if !sh.AdLimits.Allow("web:apdemo:"+ip, 30) {
		writeWebJSON(w, http.StatusTooManyRequests, map[string]any{
			"ok": false, "msg": "请求太频繁，请稍后再试。"})
		return
	}
	var body struct {
		Token   string     `json:"token"`
		Signals webSignals `json:"signals"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).
		Decode(&body); err != nil {
		writeWebJSON(w, http.StatusBadRequest, map[string]any{
			"ok": false, "msg": "请求体无法解析。"})
		return
	}
	if body.Token == "" {
		writeWebJSON(w, http.StatusOK, map[string]any{
			"ok": false, "msg": "组件没有返回令牌，可能没加载出来。"})
		return
	}

	ua := r.Header.Get("User-Agent")
	hard, soft := evalSignals(ua, body.Signals)
	if len(hard) > 0 {
		writeWebJSON(w, http.StatusOK, map[string]any{
			"ok": false, "msg": "命中自动化特征：" + strings.Join(hard, "；")})
		return
	}
	if ok, why := verifyTurnstile(sh, body.Token, ip, appealID); !ok {
		slog.Info("申诉验证演示：未通过", "原因", why)
		writeWebJSON(w, http.StatusOK, map[string]any{
			"ok": false, "msg": "Turnstile 未通过：" + why})
		return
	}
	msg := "申诉验证路径通过（action / cdata / hostname 全部相符）"
	if len(soft) > 0 {
		msg += "（软信号：" + strings.Join(soft, "；") + "）"
	}
	slog.Info("申诉验证演示：通过")
	writeWebJSON(w, http.StatusOK, map[string]any{"ok": true, "msg": msg})
}
