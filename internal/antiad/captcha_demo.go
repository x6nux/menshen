package antiad

import (
	"encoding/json"
	"log/slog"
	"net/http"
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
// turnstile / hcaptcha 用 captcha_demo_keys 里的凭据；cap 是进程内实现，
// 无需任何配置，恒定可用。
func captchaDemoProvider(sh *core.Shared, name string) (captchaProvider, bool) {
	if name == "cap" {
		if !capEnabled(sh) {
			return captchaProvider{}, false
		}
		return captchaProvider{name: "cap", siteKey: CapSiteKey}, true
	}
	c, ok := sh.Cfg.CaptchaDemoKeys[name]
	if !ok {
		return captchaProvider{}, false
	}
	return captchaProvider{name: name, siteKey: c.SiteKey, secret: c.Secret}, true
}

// handleCaptchaDemoPage 处理演示页的 GET ?json=1（返回各家 site key）与
// POST {provider,token,signals}（服务端校验）。
func handleCaptchaDemoPage(sh *core.Shared, w http.ResponseWriter, r *http.Request, _ webRoute) {
	if !sh.Cfg.CaptchaDemo {
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
