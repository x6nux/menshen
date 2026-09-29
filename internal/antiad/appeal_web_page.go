package antiad

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
)

// ---- 客户端特征、自动化信号、指纹 ----

// webSignals 是验证页客户端采集的特征。原始 JSON 存进 web_checks.signals。
type webSignals struct {
	UA       string `json:"ua"`
	Platform string `json:"platform"`
	Timezone string `json:"timezone"`
	// Languages 是 navigator.languages。
	Languages []string `json:"languages"`

	Webdriver bool `json:"webdriver"`
	// Automation 是命中的自动化框架全局量名；DocProps 是 document 上
	// $cdc_ / $wdc_ 前缀的属性名。客户端检测后上报，服务端只认名单内
	// 的硬信号，客户端多报的不算。
	Automation []string `json:"automation"`
	DocProps   []string `json:"doc_props"`

	Screen struct {
		W     int     `json:"w"`
		H     int     `json:"h"`
		Depth int     `json:"depth"`
		DPR   float64 `json:"dpr"`
	} `json:"screen"`
	HardwareConcurrency int     `json:"hardware_concurrency"`
	DeviceMemory        float64 `json:"device_memory"`
	MaxTouchPoints      int     `json:"max_touch_points"`

	CanvasHash    string   `json:"canvas_hash"`
	WebGLVendor   string   `json:"webgl_vendor"`
	WebGLRenderer string   `json:"webgl_renderer"`
	AudioHash     float64  `json:"audio_hash"`
	Fonts         []string `json:"fonts"`

	PluginsLen int `json:"plugins"`
	// NotificationPermission 与 PermissionQuery 用于交叉验证：
	// 前者是状态、后者是查询结果，自动化环境常常对不上。
	NotificationPermission string `json:"notification_permission"`
	PermissionQuery        string `json:"permission_query"`
}

// automationGlobals 是自动化框架注入的全局量（硬信号）。
var automationGlobals = []string{
	"callPhantom", "_phantom", "__nightmare", "domAutomation",
	"domAutomationController", "_selenium", "__webdriver_evaluate",
	"__selenium_unwrapped", "__fxdriver_unwrapped",
}

// evalSignals 把客户端特征分成硬信号（判失败）与软信号（只记录）。
//
// Turnstile 本身是主力，这里只是补充。客户端能伪造这些值，所以软信号
// 只供人参考，不参与判定。
func evalSignals(headerUA string, s webSignals) (hard, soft []string) {
	if s.Webdriver {
		hard = append(hard, "navigator.webdriver")
	}
	for _, ua := range []string{headerUA, s.UA} {
		if strings.Contains(ua, "HeadlessChrome") || strings.Contains(ua, "PhantomJS") {
			hard = append(hard, "UA:"+truncateLabel(ua, 60))
			break
		}
	}
	for _, name := range s.Automation {
		if slices.Contains(automationGlobals, name) {
			hard = append(hard, "自动化全局量:"+name)
		}
	}
	for _, name := range s.DocProps {
		if strings.HasPrefix(name, "$cdc_") || strings.HasPrefix(name, "$wdc_") {
			hard = append(hard, "document."+name)
		}
	}

	if headerUA != "" && s.UA != "" && headerUA != s.UA {
		soft = append(soft, "脚本 UA 与请求头不一致")
	}
	if len(s.Languages) == 0 {
		soft = append(soft, "navigator.languages 为空")
	}
	renderer := strings.ToLower(s.WebGLRenderer)
	if strings.Contains(renderer, "swiftshader") || strings.Contains(renderer, "llvmpipe") {
		soft = append(soft, "软件渲染:"+truncateLabel(s.WebGLRenderer, 60))
	}
	if s.Screen.W == 0 || s.Screen.H == 0 {
		soft = append(soft, "屏幕尺寸为 0")
	}
	if strings.Contains(s.UA, "Chrome/") && !strings.Contains(s.UA, "Mobile") && s.PluginsLen == 0 {
		soft = append(soft, "桌面 Chrome 但没有插件")
	}
	if s.NotificationPermission == "denied" && s.PermissionQuery == "prompt" {
		soft = append(soft, "通知权限状态与查询结果不一致")
	}
	return hard, soft
}

func truncateLabel(s string, n int) string { return core.TruncateRunes(s, n) }

// fingerprintOf 把特征规范化后算指纹。
//
// 屏幕宽高取 (min, max)：横竖屏得到同一个指纹；dpr 保留 2 位；字符串
// 统一小写；**不含完整 UA**（浏览器一升级就变）。不采信客户端算好的哈希。
func fingerprintOf(s webSignals) string {
	langs := make([]string, 0, len(s.Languages))
	for _, l := range s.Languages {
		langs = append(langs, strings.ToLower(l))
	}
	sort.Strings(langs)
	fonts := make([]string, 0, len(s.Fonts))
	for _, f := range s.Fonts {
		fonts = append(fonts, strings.ToLower(f))
	}
	sort.Strings(fonts)

	w, h := s.Screen.W, s.Screen.H
	if w > h {
		w, h = h, w
	}
	parts := []string{
		strings.ToLower(s.Platform),
		strings.Join(langs, ","),
		strings.ToLower(s.Timezone),
		fmt.Sprintf("%d,%d,%d", w, h, s.Screen.Depth),
		fmt.Sprintf("%.2f", s.Screen.DPR),
		fmt.Sprintf("%d", s.HardwareConcurrency),
		fmt.Sprintf("%.2f", s.DeviceMemory),
		fmt.Sprintf("%d", s.MaxTouchPoints),
		strings.ToLower(s.CanvasHash),
		strings.ToLower(s.WebGLVendor),
		strings.ToLower(s.WebGLRenderer),
		fmt.Sprintf("%.6f", s.AudioHash),
		strings.Join(fonts, ","),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])[:32]
}

// recordWebCheck 写一行网页验证记录。不论成败都写：失败的尝试对关联
// 账号同样有用。
func recordWebCheck(sh *core.Shared, ap appealRec, ip, ua, fp string,
	sig webSignals, flags []string, result string) {

	raw, _ := json.Marshal(sig)
	if _, err := sh.Store.Write.Exec(`INSERT INTO web_checks
		(appeal_id,bot_id,user_id,ip,ua,fp,signals,flags,result,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		ap.ID, ap.BotID, ap.UserID, ip, core.TruncateRunes(ua, 300), fp,
		string(raw), strings.Join(flags, "；"), result, time.Now().Unix()); err != nil {
		slog.Error("申诉：网页验证记录落库失败", "appeal", ap.ID, "err", err)
	}
}

// relatedAccounts 按指纹（强关联）与 30 天内 IP（弱关联）找其他账号。
//
// 只展示，从不据此自动处置：指纹可伪造，同型号 iPhone 的指纹也很接近；
// 运营商共享出口、公共 Wi-Fi 会让 IP 关联撞上无关的人。
func relatedAccounts(sh *core.Shared, fp, ip string, selfUID int64) (strong, weak []int64) {
	const limit = 10
	if fp != "" {
		rows, err := sh.Store.Read.Query(`SELECT DISTINCT user_id FROM web_checks
			WHERE fp=? AND user_id<>? LIMIT ?`, fp, selfUID, limit)
		if err == nil {
			for rows.Next() {
				var uid int64
				if rows.Scan(&uid) == nil {
					strong = append(strong, uid)
				}
			}
			rows.Close()
		}
	}
	if ip != "" {
		since := time.Now().Unix() - 30*86400
		rows, err := sh.Store.Read.Query(`SELECT DISTINCT user_id FROM web_checks
			WHERE ip=? AND created_at > ? AND user_id<>? LIMIT ?`,
			ip, since, selfUID, limit+len(strong))
		if err == nil {
			for rows.Next() {
				var uid int64
				if rows.Scan(&uid) == nil && !slices.Contains(strong, uid) {
					weak = append(weak, uid)
				}
			}
			rows.Close()
		}
		if len(weak) > limit {
			weak = weak[:limit]
		}
	}
	return strong, weak
}

// ---- Turnstile ----

// turnstileVerifyURL 是包级变量：测试时替换成 httptest 地址。
var turnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// verifyTurnstile 向 Cloudflare 校验令牌。
//
// 核对 success、hostname（等于 public_url 的主机名）、action 与 cdata，
// 任一不符即失败。令牌 5 分钟有效、只能校验一次，重放由 Cloudflare 拒绝。
func verifyTurnstile(sh *core.Shared, token, ip string, appealID int64) (bool, string) {
	if token == "" {
		return false, "缺少令牌"
	}
	host := ""
	if u, err := url.Parse(sh.Cfg.PublicURL); err == nil {
		host = u.Hostname()
	}
	form := url.Values{
		"secret":          {sh.Cfg.TurnstileSecret},
		"response":        {token},
		"remoteip":        {ip},
		"idempotency_key": {fmt.Sprintf("%d-%d", appealID, time.Now().UnixNano())},
	}
	// Transport 为 nil：读 HTTP_PROXY / HTTPS_PROXY 环境变量。
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.PostForm(turnstileVerifyURL, form)
	if err != nil {
		return false, "siteverify 请求失败: " + err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	var out struct {
		Success    bool     `json:"success"`
		Hostname   string   `json:"hostname"`
		Action     string   `json:"action"`
		CData      string   `json:"cdata"`
		ErrorCodes []string `json:"error-codes"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, "siteverify 响应无法解析"
	}
	switch {
	case !out.Success:
		return false, "success=false " + strings.Join(out.ErrorCodes, ",")
	case host != "" && out.Hostname != host:
		return false, "hostname 不符: " + out.Hostname
	case out.Action != "appeal":
		return false, "action 不符: " + out.Action
	case out.CData != fmt.Sprintf("%d", appealID):
		return false, "cdata 不符: " + out.CData
	}
	return true, ""
}

// appealPageInfo 渲染验证页上的「账号信息」「为什么被限制」「你在群里的发言」
// 与「AI 复核结论」。
//
// 这个页面是用户唯一能看到自己处罚依据的地方：只写「完成验证拿解禁码」
// 的话，他既不知道为什么被罚、也不知道该改什么，只能盲点一遍。内容全部
// 来自本单与本人流水（签名 URL，只给本人看），逐项转义。
func appealPageInfo(sh *core.Shared, ap appealRec) (account, limits, msgs, ai string) {
	loc := sh.Cache.Snap().Location()
	ts := func(unix int64) string {
		if unix <= 0 {
			return ""
		}
		return time.Unix(unix, 0).In(loc).Format("2006-01-02 15:04")
	}

	// 账号信息：判定当时的昵称与用户名。广告号被处置后常改名，所以取
	// 流水里记下的那一份，而不是现场查（现场也多半查不到：没私聊过）。
	var name string
	sh.Store.Read.QueryRow(`SELECT user_name FROM antiad_log
		WHERE bot_id=? AND user_id=? ORDER BY id DESC LIMIT 1`,
		ap.BotID, ap.UserID).Scan(&name)
	var ab strings.Builder
	fmt.Fprintf(&ab, "<p>用户 ID：<code>%d</code>", ap.UserID)
	if strings.TrimSpace(name) != "" {
		ab.WriteString("<br>判定时的昵称：" + html.EscapeString(name))
	}
	ab.WriteString("</p>")

	var lb strings.Builder
	for _, p := range effectivePenalties(sh, ap.BotID, ap.UserID) {
		lb.WriteString("<li>")
		switch p.Type {
		case "join_profile":
			fmt.Fprintf(&lb, "进群资料审核限制 · 群 <code>%d</code>", p.ChatID)
		case "message":
			fmt.Fprintf(&lb, "消息判定处置 · 群 <code>%d</code>", p.ChatID)
		case "gban":
			lb.WriteString("联合封禁 · 全平台")
		case "gban_own":
			lb.WriteString("联合封禁 · 本 bot 名下群组")
		}
		if t := ts(p.At); t != "" {
			lb.WriteString(" · " + t)
		}
		if r := strings.TrimSpace(p.Reason); r != "" {
			lb.WriteString(`<div class="sub">理由：` +
				html.EscapeString(core.TruncateRunes(r, 400)) + `</div>`)
		}
		if t := strings.TrimSpace(p.Text); t != "" {
			lb.WriteString(`<div class="sub">原消息：` +
				html.EscapeString(core.TruncateRunes(t, 400)) + `</div>`)
		}
		lb.WriteString("</li>")
	}
	if lb.Len() == 0 {
		lb.WriteString("<li>本 bot 名下已查不到仍在生效的限制，可能已经解除。</li>")
	}

	// 发言记录：给本人看他在这些群里的留底。这是「为什么被罚」最直接的
	// 证据，用户往往看到自己的原话就明白了。
	msgs = appealPageMsgs(sh, ap, loc)

	var aiB strings.Builder
	switch ap.AIResult {
	case "uphold":
		aiB.WriteString("<p>AI 复核后<b>维持原判</b>")
	case "overturn":
		aiB.WriteString("<p>AI 复核后撤销原判")
	case "error":
		aiB.WriteString("<p>AI 复核时出错（未自动解除）")
	case "skipped":
		aiB.WriteString("<p>AI 复核已跳过")
	default:
		aiB.WriteString("<p>还没有 AI 复核结论")
	}
	if ap.AIConf > 0 {
		fmt.Fprintf(&aiB, "（置信度 %.0f%%", ap.AIConf*100)
		if ap.AIModel != "" {
			aiB.WriteString(" · " + html.EscapeString(ap.AIModel))
		}
		aiB.WriteString("）")
	} else if ap.AIModel != "" {
		aiB.WriteString("（" + html.EscapeString(ap.AIModel) + "）")
	}
	aiB.WriteString("</p>")
	if r := strings.TrimSpace(ap.AIReason); r != "" {
		aiB.WriteString(`<div class="sub">` +
			html.EscapeString(core.TruncateRunes(r, 400)) + `</div>`)
	}
	if s := strings.TrimSpace(ap.Statement); s != "" {
		aiB.WriteString(`<div class="sub">你的申诉理由：` +
			html.EscapeString(core.TruncateRunes(s, 200)) + `</div>`)
	}
	ai = aiB.String()
	account = ab.String()
	limits = lb.String()
	return
}

// appealPageMsgLimit 是验证页上展示的发言条数与单条字数上限。
// 「所有发言」按留底展示，但页面不能被一个刷了几千条的号撑爆。
const (
	appealPageMsgLimit  = 100
	appealPageMsgLength = 300
)

// appealPageMsgs 渲染此人在本 bot 名下各群的发言留底（最近的在前）。
// 群名取配置里的标题，取不到就只写 chat_id。
func appealPageMsgs(sh *core.Shared, ap appealRec, loc *time.Location) string {
	snap := sh.Cache.Snap()
	chats := snap.ChatsOf(ap.BotID)
	if len(chats) == 0 {
		// 群配置已被移除，但留底还在：退回到处罚流水里出现过的群。
		seen := map[int64]bool{}
		for _, p := range effectivePenalties(sh, ap.BotID, ap.UserID) {
			if p.ChatID != 0 && !seen[p.ChatID] {
				seen[p.ChatID] = true
				chats = append(chats, store.BotChat{BotID: ap.BotID, ChatID: p.ChatID})
			}
		}
	}
	var sb strings.Builder
	total := 0
	for _, c := range chats {
		if total >= appealPageMsgLimit {
			break
		}
		rows, err := sh.Store.Read.Query(`SELECT text,at FROM group_messages
			WHERE chat_id=? AND user_id=? AND text != '' ORDER BY at DESC LIMIT ?`,
			c.ChatID, ap.UserID, appealPageMsgLimit-total)
		if err != nil {
			continue
		}
		var items []string
		for rows.Next() {
			var text string
			var at int64
			if rows.Scan(&text, &at) != nil {
				continue
			}
			items = append(items, `<li><span class="ts">`+
				time.Unix(at, 0).In(loc).Format("01-02 15:04")+`</span> `+
				html.EscapeString(core.TruncateRunes(text, appealPageMsgLength))+`</li>`)
		}
		rows.Close()
		if len(items) == 0 {
			continue
		}
		total += len(items)
		title := strconv.FormatInt(c.ChatID, 10)
		if strings.TrimSpace(c.Title) != "" {
			title = html.EscapeString(c.Title)
		}
		fmt.Fprintf(&sb, `<div class="sub">群 %s</div><ul class="plain">%s</ul>`,
			title, strings.Join(items, ""))
	}
	if sb.Len() == 0 {
		return "<p>没有查到你的发言留底（可能已过保留期，或你还没在本 bot 的群里发过言）。</p>"
	}
	return sb.String()
}

// newNonce 生成 CSP 脚本 nonce。
func newNonce() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "nonce"
	}
	return hex.EncodeToString(buf)
}
