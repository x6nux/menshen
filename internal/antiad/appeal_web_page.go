package antiad

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

// turnstileResult 是 siteverify 的解析结果。入群验证只用 Success 与
// Hostname；申诉页还要核对 Action 与 CData（把令牌绑到那一张申诉单上）。
type turnstileResult struct {
	Success    bool     `json:"success"`
	Hostname   string   `json:"hostname"`
	Action     string   `json:"action"`
	CData      string   `json:"cdata"`
	ErrorCodes []string `json:"error-codes"`
}

// turnstileSiteverify 向 Cloudflare 校验令牌，返回解析结果。
//
// 令牌 5 分钟有效、只能校验一次，重放由 Cloudflare 拒绝。idem 是幂等键：
// 同一次校验重发时 Cloudflare 返回同一结果，避免网络重试造成第二次校验
// 同一个令牌。错误只表示请求/解析失败，success=false 不是 error。
func turnstileSiteverify(secret, token, ip, idem string) (turnstileResult, error) {
	var out turnstileResult
	if token == "" {
		return out, fmt.Errorf("缺少令牌")
	}
	form := url.Values{
		"secret":          {secret},
		"response":        {token},
		"remoteip":        {ip},
		"idempotency_key": {idem},
	}
	// Transport 为 nil：读 HTTP_PROXY / HTTPS_PROXY 环境变量。
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.PostForm(turnstileVerifyURL, form)
	if err != nil {
		return out, fmt.Errorf("siteverify 请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("siteverify 响应无法解析")
	}
	return out, nil
}

// publicHost 是 public_url 的主机名（用于核对验证码返回的 hostname）。
func publicHost(sh *core.Shared) string {
	if u, err := url.Parse(sh.Cfg.PublicURL); err == nil {
		return u.Hostname()
	}
	return ""
}

// verifyTurnstile 校验申诉页的 Turnstile 令牌。
//
// 核对 success、hostname（等于 public_url 的主机名）、action 与 cdata，
// 任一不符即失败。用的是申诉专用的 TurnstileSecret。
func verifyTurnstile(sh *core.Shared, token, ip string, appealID int64) (bool, string) {
	out, err := turnstileSiteverify(sh.Cfg.TurnstileSecret, token, ip,
		fmt.Sprintf("%d-%d", appealID, time.Now().UnixNano()))
	if err != nil {
		return false, err.Error()
	}
	host := publicHost(sh)
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

// verifyTurnstileToken 校验入群验证页的 Turnstile 令牌：只核对 success 与
// hostname。入群验证不绑定单个对象，没有 action / cdata 可比，用的是入群
// 验证自己的 CaptchaSecret（与申诉的 TurnstileSecret 是两套配置）。
func verifyTurnstileToken(sh *core.Shared, secret, token, ip string) (bool, string) {
	out, err := turnstileSiteverify(secret, token, ip,
		fmt.Sprintf("jv-%d", time.Now().UnixNano()))
	if err != nil {
		return false, err.Error()
	}
	if !out.Success {
		return false, "success=false " + strings.Join(out.ErrorCodes, ",")
	}
	if host := publicHost(sh); host != "" && out.Hostname != host {
		return false, "hostname 不符: " + out.Hostname
	}
	return true, ""
}

// appealPageDataOf 组装验证页 SPA 所需的结构化数据。
//
// 验证页是用户唯一能看到自己处罚依据的地方：只写完成验证拿解禁码不够，
// 还需给出被限原因与可修改内容。内容全部来自本单与本人流水（签名 URL，
// 只给本人看）。
func appealPageDataOf(sh *core.Shared, ap appealRec) map[string]any {
	loc := sh.Cache.Snap().Location()

	// 账号信息：判定当时的昵称与用户名。广告号被处置后常改名，所以取
	// 流水里记下的那一份，而不是现场查（现场也多半查不到：没私聊过）。
	var name string
	sh.Store.Read.QueryRow(`SELECT user_name FROM antiad_log
		WHERE bot_id=? AND user_id=? ORDER BY id DESC LIMIT 1`,
		ap.BotID, ap.UserID).Scan(&name)

	limits := []map[string]any{}
	for _, p := range effectivePenalties(sh, ap.BotID, ap.UserID) {
		limits = append(limits, map[string]any{
			"type": p.Type, "chat_id": p.ChatID,
			"time":   pageTS(loc, p.At),
			"reason": core.TruncateRunes(strings.TrimSpace(p.Reason), 400),
			"text":   core.TruncateRunes(strings.TrimSpace(p.Text), 400),
		})
	}

	return map[string]any{
		"appeal_id": ap.ID,
		"uid":       ap.UserID,
		"sitekey":   sh.Cfg.TurnstileSiteKey,
		"cdata":     strconv.FormatInt(ap.ID, 10),
		"days":      sh.Cache.Snap().SettingInt("log_retention_days", 30),
		"account":   map[string]any{"uid": ap.UserID, "name": name},
		"limits":    limits,
		"messages":  appealPageMessages(sh, ap, loc),
		"ai":        appealPageAI(ap),
	}
}

// pageTS 是网页上统一的时间格式；0 值返回空串。
func pageTS(loc *time.Location, unix int64) string {
	if unix <= 0 {
		return ""
	}
	return time.Unix(unix, 0).In(loc).Format("2006-01-02 15:04")
}

// appealPageAI 把 AI 复核结论整理成结论标签、置信度、模型、理由与申诉人
// 自己的理由。
func appealPageAI(ap appealRec) map[string]any {
	label := "还没有 AI 复核结论"
	switch ap.AIResult {
	case "uphold":
		label = "AI 复核后维持原判"
	case "overturn":
		label = "AI 复核后撤销原判"
	case "error":
		label = "AI 复核时出错（未自动解除）"
	case "skipped":
		label = "AI 复核已跳过"
	}
	return map[string]any{
		"result": ap.AIResult, "label": label,
		"conf": ap.AIConf, "model": ap.AIModel,
		"reason":    core.TruncateRunes(strings.TrimSpace(ap.AIReason), 400),
		"statement": core.TruncateRunes(strings.TrimSpace(ap.Statement), 200),
	}
}

// appealPageMsgLimit 是验证页上展示的发言条数与单条字数上限。
// 所有发言按留底展示，但页面不能被刷了几千条的号撑爆。
const (
	appealPageMsgLimit  = 100
	appealPageMsgLength = 300
)

// appealPageMessages 返回此人在本 bot 名下各群的发言留底（最近的在前）。
// 群名取配置里的标题，取不到就只带 chat_id。
func appealPageMessages(sh *core.Shared, ap appealRec, loc *time.Location) []map[string]any {
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
	out := []map[string]any{}
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
		title := strconv.FormatInt(c.ChatID, 10)
		if strings.TrimSpace(c.Title) != "" {
			title = c.Title
		}
		for rows.Next() {
			var text string
			var at int64
			if rows.Scan(&text, &at) != nil {
				continue
			}
			out = append(out, map[string]any{
				"chat_id": c.ChatID, "title": title,
				"text": core.TruncateRunes(text, appealPageMsgLength),
				"at":   at, "time": time.Unix(at, 0).In(loc).Format("01-02 15:04"),
			})
			total++
		}
		rows.Close()
	}
	return out
}
