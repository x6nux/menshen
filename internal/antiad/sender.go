package antiad

import (
	"encoding/json"
	"html"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/tg"
)

// ---- 发言者画像的补全：简介里挂的频道/群组/bot ----

// linkInfo 是一个公开用户名查出来的样子。Kind 为 channel / supergroup /
// group / bot / user；查不到（私有、已删除、打错）时为 unknown。
type linkInfo struct {
	Handle string `json:"handle"`
	Kind   string `json:"kind"`
	Title  string `json:"title,omitempty"`
	About  string `json:"about,omitempty"`
	// AdKnown 表示这个账号在本服务里有广告案底：仍在联合封禁名单里，
	// 或被判过广告。名字分不出正常 bot 与广告 bot，这个字段能。
	AdKnown bool `json:"ad_known,omitempty"`
}

const (
	// profileLinkMax 是一个人最多解析的链接数。每个都是一次 getChat，
	// 资料里塞几十个用户名的号不值得全查。
	profileLinkMax = 5
	// linkTTL 是解析结果的缓存时长。频道简介很少改；缓存按用户名共享，
	// 群里十个人挂同一个频道也只查一次。
	linkTTL = 6 * time.Hour
)

var profileHandleRe = regexp.MustCompile(`(?:@|t\.me/)([A-Za-z][A-Za-z0-9_]{3,31})`)

// profileHandles 从昵称与简介里取出公开用户名（@xxx 与 t.me/xxx），去重保序。
// t.me/+xxx 与 t.me/joinchat 是私密邀请，查不出内容，交给提示词按强信号处理。
func profileHandles(p senderProfile) []string {
	text := p.FirstName + " " + p.LastName + " " + p.Bio
	var out []string
	seen := map[string]bool{}
	for _, m := range profileHandleRe.FindAllStringSubmatch(text, -1) {
		h, key := m[1], strings.ToLower(m[1])
		if key == "joinchat" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, h)
		if len(out) == profileLinkMax {
			break
		}
	}
	return out
}

// enrichSender 补上要发 TG API 才拿得到的画像：个人简介，以及简介与昵称里
// 挂的频道/群组/bot。自动判定、/check 复查与冷判定共用，都在判定 worker 里跑——
// 同步段每多一次 TG 往返，更新处理就多停一次。
//
// 简介里挂自己的频道/群组/bot 是正常的（UP 主、开发者都这样做），只给
// 用户名的话模型分不清挂自己的技术频道与挂刷单群，会误判。
func enrichSender(b *core.Bot, p *senderProfile) {
	applyChatInfo(b, p)
	p.BioLinks = resolveProfileLinks(b, *p)
	// 简介与链接解析完才有完整资料，指纹也在这之后算。
	markProfileOK(b, p)
}

// applyChatInfo 补个人简介与昵称/用户名（getChat，带缓存）。
func applyChatInfo(b *core.Bot, p *senderProfile) {
	info := userInfo(b, p.UserID)
	p.Bio = info.bio
	// /check <uid> 没有消息可依托，昵称与用户名是空的：用 getChat 的结果补上。
	// 硬规则按资料判人、模型看资料也更准（补的是空位，不覆盖消息里已有的）。
	if p.FirstName == "" {
		p.FirstName = info.firstName
	}
	if p.LastName == "" {
		p.LastName = info.lastName
	}
	if p.Username == "" {
		p.Username = info.username
	}
}

// enrichSenderFresh 是 /check 复查专用的画像补全：绕过全部缓存。
//
// 复查是人对机器结论的仲裁，要看到的是此刻的真实资料：简介缓存（1 小时）
// 清掉重查，资料里挂的频道/群组/bot 缓存（6 小时）逐个清掉重解析；资料
// 放行标记 profile_ok 不套用 —— 复查审的正是资料本身，若旧放行仍挡在这里，
// 复查就永远维持原判。
func enrichSenderFresh(b *core.Bot, p *senderProfile) {
	ForgetUserInfo(b, p.UserID)
	applyChatInfo(b, p)
	// 链接解析要等简介补全（@xxx 藏在简介里），所以清缓存放在这之后。
	for _, h := range profileHandles(*p) {
		cachesOf(b.Shared).link.Delete(strings.ToLower(h))
	}
	p.BioLinks = resolveProfileLinks(b, *p)
	p.ProfileOK, p.ProfileOKUntil = false, ""
}

// resolveProfileLinks 逐个查清资料里挂的频道/群组/bot 是什么。
func resolveProfileLinks(b *core.Bot, p senderProfile) []linkInfo {
	var out []linkInfo
	for _, h := range profileHandles(p) {
		out = append(out, resolveLink(b, h))
	}
	return out
}

// resolveLink 查一个公开用户名是什么，结果按小写用户名缓存。
func resolveLink(b *core.Bot, handle string) linkInfo {
	key := strings.ToLower(handle)
	if info, ok := cachesOf(b.Shared).link.Get(key); ok {
		return info
	}
	info := linkInfo{Handle: handle, Kind: "unknown"}
	raw, err := b.TG.Call("getChat", map[string]any{"chat_id": "@" + handle})
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			ID          int64  `json:"id"`
			Type        string `json:"type"`
			Title       string `json:"title"`
			FirstName   string `json:"first_name"`
			LastName    string `json:"last_name"`
			Description string `json:"description"`
			Bio         string `json:"bio"`
		} `json:"result"`
	}
	if err == nil && json.Unmarshal(raw, &resp) == nil && resp.OK {
		r := resp.Result
		info.Kind, info.Title, info.About = r.Type, r.Title, r.Description
		if r.Type == "private" {
			// 用户与 bot 的 getChat 都是 private。TG 规定 bot 用户名必须以 bot 结尾。
			info.Kind = "user"
			if strings.HasSuffix(key, "bot") {
				info.Kind = "bot"
			}
			info.Title = strings.TrimSpace(r.FirstName + " " + r.LastName)
			info.About = r.Bio
		}
		// bot 的简介只有公开预览页给得到（getChat 对别的 bot 只返回名字与
		// 用户名），而该 bot 有没有广告案底只有本服务自己的账本知道。两者
		// 一起补上，模型才能分清挂自己的正常 bot 与引流到广告 bot；只给名字
		// 时容易误判为广告号。
		if info.Kind == "bot" {
			info.About = fetchBotAbout(handle)
			info.AdKnown = accountAdKnown(b.Shared, r.ID)
		}
		// 频道简介同样是攻击者可控的文字，与正文同一套上限思路。
		info.Title = core.TruncateRunes(info.Title, 100)
		info.About = core.TruncateRunes(info.About, 300)
	}
	// 查不到也缓存：私有群、已删号每条消息都重查一遍只是白白撞速率限制。
	cachesOf(b.Shared).link.Set(key, info, linkTTL)
	return info
}

// accountAdKnown 报告这个账号在本服务里有没有广告案底：仍在联合封禁名单
// 里，或被判过广告。判断把人引导到某个 bot 是否构成引流广告靠它，仅凭
// 名字无法区分（部分名字只是取名风格）。
func accountAdKnown(sh *core.Shared, uid int64) bool {
	if uid == 0 {
		return false
	}
	snap := sh.Cache.Snap()
	if _, ok := snap.Gban[uid]; ok {
		return true
	}
	for _, bans := range snap.GbanOwnBans {
		if _, ok := bans[uid]; ok {
			return true
		}
	}
	var n int
	if err := sh.Store.Read.QueryRow(`SELECT COUNT(*) FROM antiad_log
		WHERE user_id=? AND verdict='ad'`, uid).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// botAboutURL 是 bot 公开预览页的前缀。测试时替换成 httptest 地址。
var botAboutURL = "https://t.me/"

// ogDescriptionRe 从预览页里取简介。
var ogDescriptionRe = regexp.MustCompile(`<meta property="og:description" content="([^"]*)"`)

// botAboutClient 取 bot 简介用的 HTTP 客户端。Transport 为 nil：读
// HTTP_PROXY / HTTPS_PROXY 环境变量，与 Turnstile 校验同一个思路。
var botAboutClient = &http.Client{Timeout: 5 * time.Second}

// fetchBotAbout 取一个 bot 的公开简介（它的 /setdescription 文本）。
//
// 只有公开预览页给得到这个字段：getChat 对别的 bot 只返回名字与用户名；
// bot 的简介与用户 bio 不同，是设置给自己聊天页面的说明。取不到（网络
// 不可用、未设置、页面结构变化）返回空串：提示词中没设置简介与正常简介
// 同样不加重怀疑。
func fetchBotAbout(handle string) string {
	req, err := http.NewRequest(http.MethodGet, botAboutURL+handle, nil)
	if err != nil {
		return ""
	}
	// 不带浏览器 UA 时预览页会返回无 meta 的精简版。
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; menshen-antiad/1.0)")
	resp, err := botAboutClient.Do(req)
	if err != nil {
		slog.Debug("反广告：读取 bot 简介失败", "handle", handle, "err", err)
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return ""
	}
	m := ogDescriptionRe.FindSubmatch(raw)
	if m == nil {
		return ""
	}
	desc := strings.TrimSpace(html.UnescapeString(string(m[1])))
	// 没设置简介时预览页给的是默认文案（"You can contact @xxx right away."），
	// 那不是简介，不应解读为该 bot 只提供联系方式。
	if strings.HasPrefix(desc, "You can contact @") &&
		strings.HasSuffix(desc, " right away.") {
		return ""
	}
	return core.TruncateRunes(desc, 300)
}

var mentionRe = regexp.MustCompile(`@([A-Za-z][A-Za-z0-9_]{3,31})`)

// mentionedBots 取出正文里 @ 到的 bot 用户名。TG 规定 bot 用户名必须以 bot 结尾。
// 正文只有一串 @xxxbot 是召唤访客 bot 代发广告的典型手法，单列出来模型才看得清。
func mentionedBots(text string) []string {
	var out []string
	for _, mm := range mentionRe.FindAllStringSubmatch(text, -1) {
		if strings.HasSuffix(strings.ToLower(mm[1]), "bot") {
			out = append(out, mm[1])
		}
	}
	return out
}

// adKindLabels 是广告分类的中文名。分类值来自模型输出，面板上直接显示
// `scam`、`porn_bait` 这类原始值不易理解。
var adKindLabels = map[string]string{
	"crypto":     "加密货币",
	"porn":       "色情招揽",
	"porn_bait":  "色情内容",
	"gambling":   "博彩",
	"scam":       "诈骗",
	"promo":      "推广引流",
	"spam_flood": "刷屏",
	"manual":     "人工标记",
	// 硬规则命中（冒用国家领导人，见 leader.go）
	"impersonate": leaderKindLabel,
}

// adKindLabel 返回分类的中文名；模型给了表外的值就原样显示，不吞掉信息。
func adKindLabel(kind string) string {
	if kind == "" || kind == "none" {
		return "未分类"
	}
	if l, ok := adKindLabels[kind]; ok {
		return l
	}
	return kind
}

// ---- 实际发言者 ----

// TG 填在 from 里的几个固定账号。
const (
	// tgServiceUID 是关联频道自动转发进讨论群时的发送者（is_bot=false）。
	tgServiceUID = 777000
	// groupAnonymousBotID 是匿名管理员发言时的占位账号，此时 sender_chat 为本群。
	groupAnonymousBotID = 1087968824
)

// senderOf 取消息的实际发言者。
//
// 以频道身份发言时 TG 在 from 里填的是占位的 Channel_Bot（is_bot=true，
// 所有频道共用一个），真正的发言者在 sender_chat——不换的话 bot 一律豁免
// 会把它放过，禁言、白名单也会落到那个公共账号上。换成的频道 ID 为负
// （-100…），下游据此区分频道与真人。
//
// 匿名管理员（sender_chat 是本群）与关联频道自动转发保持原样，由 adExempt 豁免。
// 访客 bot 代发的换成召唤者：留底、判定、处置都记在召唤者名下。
func senderOf(m *tg.Message) *tg.TGUser {
	if m.GuestBotCallerUser != nil {
		return m.GuestBotCallerUser
	}
	c := m.SenderChat
	if c == nil || (m.Chat != nil && c.ID == m.Chat.ID) ||
		(m.From != nil && m.From.ID == tgServiceUID) {
		return m.From
	}
	return &tg.TGUser{ID: c.ID, FirstName: c.Title, Username: c.Username}
}

// asSender 返回一份把 From 换成实际发言者的副本（不改原消息：同一条 update
// 可能还被别处读着）。访客 bot 代发的另把 bot 记进 GuestBot，正文里要标出来。
func asSender(m *tg.Message) *tg.Message {
	u := senderOf(m)
	if u == m.From {
		return m
	}
	cp := *m
	if m.GuestBotCallerUser != nil && m.From != nil && m.From.IsBot {
		cp.GuestBot = m.From
	}
	cp.From = u
	return &cp
}
