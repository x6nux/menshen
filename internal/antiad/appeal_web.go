package antiad

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"menshen/internal/core"
)

// ---- 网页基础设施：签名密钥、链接签名、路由 ----
//
// 网页只在 webhook 模式（public_url 非空）下存在。链接形如
// <public_url>/_w/<用途>/<id>/<签名>；main 把路径中带 "_w" 段的请求
// 交给 WebHandler，其余照旧进 webhook。

// EnsureWebSecret 确保 settings.web_secret 存在（webhook 模式启动时调用）。
//
// 不进 settingDefaults：空值的含义是「还没生成」，给默认值会让所有部署
// 共用同一个密钥。在启动阶段生成而不是首次使用时：并发下两次生成会互相
// 覆盖，先签出去的链接随之失效。
func EnsureWebSecret(sh *core.Shared) error {
	if sh.Cache.Snap().Setting("web_secret") != "" {
		return nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	if err := sh.PutSetting("web_secret", hex.EncodeToString(buf)); err != nil {
		return err
	}
	slog.Info("已生成网页签名密钥")
	return nil
}

// webSig 是网页链接的签名：hex(HMAC-SHA256(secret, msg))[:32]。
// 用途前缀写进 msg（ap: / apv: / v: / vp:），一处的签名拿不到另一处用。
//
// 密钥缺失时返回空串：绝不能退化成「用空密钥签名」——那样任何人都能
// 按公开的格式算出合法签名，顺序枚举 id 读取申诉理由与原文。
func webSig(sh *core.Shared, msg string) string {
	secret := sh.Cache.Snap().Setting("web_secret")
	if secret == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

// webSigOK 校验签名，常数时间比较。密钥缺失或签名为空一律不通过。
func webSigOK(sh *core.Shared, msg, sig string) bool {
	if sig == "" {
		return false
	}
	want := webSig(sh, msg)
	if want == "" {
		return false
	}
	return hmac.Equal([]byte(want), []byte(sig))
}

func appealSig(sh *core.Shared, id, uid int64) string {
	return webSig(sh, fmt.Sprintf("ap:%d:%d", id, uid))
}

func appealViewSig(sh *core.Shared, id int64) string {
	return webSig(sh, fmt.Sprintf("apv:%d", id))
}

func logViewSig(sh *core.Shared, id int64) string {
	return webSig(sh, fmt.Sprintf("v:%d", id))
}

func logViewPostSig(sh *core.Shared, id, exp int64) string {
	return webSig(sh, fmt.Sprintf("vp:%d:%d", id, exp))
}

// webURL 拼出对外链接；网页不可用（没配 public_url 或缺签名密钥）时
// 返回空串，调用方按「网页不可用」处理。缺密钥时还发链接的话，页面上
// 的签名校验形同虚设。
func webURL(sh *core.Shared, path string) string {
	if !WebAvailable(sh) {
		return ""
	}
	base := strings.TrimRight(sh.Cfg.PublicURL, "/")
	return base + "/_w/" + path
}

// AppealURL 是申诉人自己的验证页链接。
func AppealURL(sh *core.Shared, appealID, uid int64) string {
	return webURL(sh, fmt.Sprintf("ap/%d/%s", appealID, appealSig(sh, appealID, uid)))
}

// AppealDetailURL 是管理员看的申诉详情页链接。
func AppealDetailURL(sh *core.Shared, appealID int64) string {
	return webURL(sh, fmt.Sprintf("apv/%d/%s", appealID, appealViewSig(sh, appealID)))
}

// LogViewURL 是原文查看页链接。
func LogViewURL(sh *core.Shared, logID int64) string {
	return webURL(sh, fmt.Sprintf("v/%d/%s", logID, logViewSig(sh, logID)))
}

// WebAvailable 报告网页子系统是否可用（webhook 模式且有签名密钥）。
func WebAvailable(sh *core.Shared) bool {
	return sh.Cfg.PublicURL != "" && sh.Cache.Snap().Setting("web_secret") != ""
}

// ---- 路由 ----

type webRoute struct {
	kind string // ap / apv / v
	id   int64
	sig  string
}

// parseWebRoute 从请求路径里解析网页路由。
//
// 与 TokenFromPath 同一思路：不依赖前缀，路径中任一段为 "_w" 即命中，
// 反代再套几层子路径都认得出来。形状：.../_w/<kind>/<id>/<sig>
func parseWebRoute(p string) (webRoute, bool) {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	for i, s := range segs {
		if s != "_w" {
			continue
		}
		if i+3 >= len(segs) {
			return webRoute{}, false
		}
		id, err := strconv.ParseInt(segs[i+2], 10, 64)
		if err != nil || id <= 0 {
			return webRoute{}, false
		}
		return webRoute{kind: segs[i+1], id: id, sig: segs[i+3]}, true
	}
	return webRoute{}, false
}

// IsWebPagePath 报告路径是否指向公开网页的页面路由（ap / apv / v）。
//
// 只做形状判断、不验签：SPA 外壳本身不含任何数据，签名校验发生在
// ?json=1 的数据接口里。main 在把 _w 请求交给 WebHandler 之前用它
// 决定是否先发壳（见 panel.PublicShellHandler）。
func IsWebPagePath(p string) bool {
	rt, ok := parseWebRoute(p)
	if !ok {
		return false
	}
	switch rt.kind {
	case "ap", "apv", "v":
		return true
	}
	return false
}

// WebHandler 处理 _w 下的网页请求。
//
// 签名不对、记录不存在一律 404，不区分两者 —— 免得被人按编号扫出
// 哪些申诉单存在。各页面的实现见 appeal_web.go / adview.go 的对应阶段。
func WebHandler(sh *core.Shared) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, ok := parseWebRoute(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch route.kind {
		case "ap":
			handleAppealPage(sh, w, r, route)
		case "apv":
			handleAppealDetailPage(sh, w, r, route)
		case "v":
			handleLogViewPage(sh, w, r, route)
		default:
			http.NotFound(w, r)
		}
	})
}

// ---- 页面处理器 ----

// handleAppealPage 处理验证页的数据接口 GET ?json=1 与提交 POST。
//
// 页面外壳由 React SPA 托管（main.go 在路由层先发壳）；这里只出数据。
func handleAppealPage(sh *core.Shared, w http.ResponseWriter, r *http.Request, rt webRoute) {
	ap, ok := loadAppealByID(sh.Store, rt.id)
	if !ok || !webSigOK(sh, fmt.Sprintf("ap:%d:%d", ap.ID, ap.UserID), rt.sig) {
		// 签名错与记录不存在不区分，免得被按编号扫
		writeWebJSON(w, http.StatusNotFound, map[string]any{"error": "链接无效或已被替换。"})
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Query().Get("json") == "1":
		if !appealWebUsable(ap, time.Now().Unix()) {
			writeWebJSON(w, http.StatusGone, map[string]any{
				"error": "链接已失效，请回到 bot 重新申诉。"})
			return
		}
		writeWebJSON(w, http.StatusOK, appealPageDataOf(sh, ap))
	case r.Method == http.MethodPost:
		appealPagePost(sh, w, r, ap)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// appealWebWindow 是网页验证链接的有效窗口。链接签名不过期，过期由
// web_since 判定；用户在 bot 里再点一次 /start 会自动续一个窗口。
const appealWebWindow = 24 * time.Hour

// appealWebUsable 报告申诉单是否还在可验证窗口内（web 状态、窗口内）。
func appealWebUsable(ap appealRec, now int64) bool {
	return ap.Status == "web" && ap.WebSince != 0 &&
		now-ap.WebSince < int64(appealWebWindow/time.Second)
}

// appealPagePost 处理验证提交。
func appealPagePost(sh *core.Shared, w http.ResponseWriter, r *http.Request, ap appealRec) {
	now := time.Now().Unix()
	if !appealWebUsable(ap, now) {
		writeWebJSON(w, http.StatusGone, map[string]any{
			"ok": false, "msg": "链接已失效，请回到 bot 重新申诉。"})
		return
	}

	ip := clientIP(sh, r)
	// 限频：每张申诉单每分钟 5 次、每个 IP 每分钟 20 次。
	if !sh.AdLimits.Allow(fmt.Sprintf("web:ap:%d", ap.ID), 5) ||
		!sh.AdLimits.Allow("web:ip:"+ip, 20) {
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

	ua := r.Header.Get("User-Agent")
	hard, soft := evalSignals(ua, body.Signals)

	result := "pass"
	msg := ""
	code := ""

	if len(hard) > 0 {
		result = "bot"
		msg = "验证未通过，请换用常规浏览器重试。"
	} else if ok, why := verifyTurnstile(sh, body.Token, ip, ap.ID); !ok {
		result = "turnstile"
		msg = "验证未通过，请换用常规浏览器重试。"
		slog.Info("申诉：Turnstile 未通过", "appeal", ap.ID, "原因", why)
	}

	fp := fingerprintOf(body.Signals)
	recordWebCheck(sh, ap, ip, ua, fp, body.Signals, append(hard, soft...), result)

	if result == "pass" {
		c, ok := issueUnlockCode(sh, ap.ID)
		if !ok {
			writeWebJSON(w, http.StatusInternalServerError, map[string]any{
				"ok": false, "msg": "签发解禁码失败，请稍后重试。"})
			return
		}
		code = c
		ap, _ = loadAppealByID(sh.Store, ap.ID)
		notifyCodeIssued(sh, ap, ip, fp, soft)
		writeWebJSON(w, http.StatusOK, map[string]any{
			"ok": true, "code": code,
			"msg": "验证通过。把解禁码发给群管理员即可解除限制。"})
		return
	}

	// 失败：累加次数，满 5 次结案。
	updateAppeal(sh, ap.ID, `web_attempts = web_attempts + 1`)
	ap, _ = loadAppealByID(sh.Store, ap.ID)
	if ap.WebAttempts >= 5 {
		updateAppeal(sh, ap.ID, `status='rejected'`)
		ap, _ = loadAppealByID(sh.Store, ap.ID)
		notifyAppealRejected(sh, ap, hard, soft, ip, fp)
	}
	writeWebJSON(w, http.StatusOK, map[string]any{"ok": false, "msg": msg})
}

// notifyCodeIssued 通知申诉人并给管理员推完整卡片。
func notifyCodeIssued(sh *core.Shared, ap appealRec, ip, fp string, soft []string) {
	strong, weak := relatedAccounts(sh, fp, ip, ap.UserID)
	if b := botFor(sh, ap.BotID); b != nil {
		sendUnlockCode(b, ap.UserID, ap)
		pushAppealCard(b, ap, effectivePenalties(b.Shared, b.BotID(), ap.UserID), appealCardFull,
			apHasGban(sh, ap.UserID), appealCardExtra{Soft: soft, Strong: strong, Weak: weak})
	}
	slog.Info("申诉：网页验证通过，已签发解禁码", "appeal", ap.ID, "uid", ap.UserID)
}

// notifyAppealRejected 通知申诉人并推失败卡片。
func notifyAppealRejected(sh *core.Shared, ap appealRec, hard, soft []string, ip, fp string) {
	strong, weak := relatedAccounts(sh, fp, ip, ap.UserID)
	if b := botFor(sh, ap.BotID); b != nil {
		b.Send(ap.UserID, "你的申诉未通过网页验证，请联系群管理员处理。", nil)
		pushAppealCard(b, ap, effectivePenalties(b.Shared, b.BotID(), ap.UserID), appealCardFailed,
			apHasGban(sh, ap.UserID), appealCardExtra{
				Hard: hard, Soft: soft, Strong: strong, Weak: weak})
	}
	slog.Info("申诉：网页验证失败满 5 次，已结案", "appeal", ap.ID, "uid", ap.UserID)
}

// botFor 找到管理某张申诉单的 bot 实例；实例不在运行时返回 nil。
func botFor(sh *core.Shared, botID int64) *core.Bot {
	if sh.Reg == nil {
		return nil
	}
	b, _ := sh.Reg.LookupID(botID)
	return b
}

// apHasGban 报告此人当前是否仍在联合封禁名单里。
func apHasGban(sh *core.Shared, uid int64) bool {
	_, ok := sh.Cache.Snap().Gban[uid]
	return ok
}

// ---- 请求头与响应 ----

// writeWebJSON 是网页接口的 JSON 响应：no-store + noindex，与页面同口径。
func writeWebJSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// ---- 客户端 IP ----

// clientIP 取客户端真实 IP。
//
// 配置了 client_ip_header 就取它（逗号列表取第一个），解析失败退回对端
// 地址。前提是 listen_addr 只绑回环、流量全部经过反代——这个头才可信。
func clientIP(sh *core.Shared, r *http.Request) string {
	if h := sh.Cfg.ClientIPHeader; h != "" {
		v := r.Header.Get(h)
		if i := strings.IndexByte(v, ','); i >= 0 {
			v = v[:i]
		}
		if v = strings.TrimSpace(v); net.ParseIP(v) != nil {
			return v
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
