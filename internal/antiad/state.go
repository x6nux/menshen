package antiad

import (
	"strings"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// senderProfile 是喂给 AI 的发送者画像。
// 字段名即 JSON 键名，模型直接读它做判断，改名要同步改提示词。
type senderProfile struct {
	UserID    int64  `json:"user_id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	// Bio 是 TG 个人简介。广告号常把联系方式与价目写在这里，
	// 本条消息看起来再无害也该据此提高可疑度。
	Bio string `json:"bio"`
	// BioLinks 是昵称与简介里挂的公开频道/群组/bot 查出来的样子（见 enrichSender）。
	BioLinks []linkInfo `json:"bio_links,omitempty"`
	// IsChannel 表示以频道身份发言：first_name 是频道名，bio 是频道简介。
	IsChannel   bool  `json:"is_channel,omitempty"`
	IsPremium   bool  `json:"is_premium"`
	AgeHours    int64 `json:"age_hours"`
	AgeKnown    bool  `json:"age_known"`
	MsgsInGroup int64 `json:"msgs_in_group"`
	PriorAdHits int64 `json:"prior_ad_hits"`
	// Photos / PhotoKnown 只有前置号复核路径填充（见 prewarm.go）：
	// 头像张数与是否查到。指针是为了让 0 张也能进载荷（omitempty
	// 对非空指针不生效），同时未填充时字段整体缺席 —— PhotoKnown=false
	// 时照片数未知，任何一方都不得把它当成无头像。
	Photos     *int `json:"photos,omitempty"`
	PhotoKnown bool `json:"photo_known,omitempty"`
	// UsernameRand / NameRand 是本地算法对 username / 昵称（拉丁部分）
	// 的随机度评分（0~100，≥unameRandomScore 为无词形随机串），同样只有
	// 前置号复核路径填充。nil = 没有可评分的拉丁字母串（中文昵称、无
	// 用户名），不得当成 0 分解读；提示词按此口径解释给模型。
	UsernameRand *int `json:"username_rand,omitempty"`
	NameRand     *int `json:"name_rand,omitempty"`
	// ProfileOK 表示这份资料已被复判确认不构成广告（见 profile_ok.go），
	// 到 ProfileOKUntil 之前不得再凭资料判为广告；正文照常判断。
	ProfileOK      bool   `json:"profile_ok,omitempty"`
	ProfileOKUntil string `json:"profile_ok_until,omitempty"`
}

type adMessageInfo struct {
	Text        string `json:"text"`
	HasLink     bool   `json:"has_link"`
	HasMention  bool   `json:"has_mention"`
	HasMedia    bool   `json:"has_media"`
	IsForwarded bool   `json:"is_forwarded"`
	// IsEdited 表示这是发出后又编辑过的版本。
	IsEdited bool `json:"is_edited,omitempty"`
	Length   int  `json:"length"`
	// MentionedBots 是正文里 @ 到的 bot，见 mentionedBots。
	MentionedBots []string `json:"mentioned_bots,omitempty"`
}

type adChatInfo struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

// adQuotedInfo 是本人引用/回复的那段内容。
//
// 单列而不并进 message.text：归属必须分清。那段话是被引用者写的，
// 混进本人正文会让转述并批评广告的人也被判成广告。
//
// 只有外部聊天引用会进送检载荷（见 buildState）：群内引用是别人的话，
// 引用一条广告提醒管理员 / 批评 / 询问是举报行为，不算引用者的载荷。
// 留底与告警仍用 displayText 保留全部引用，那是审计口径。
type adQuotedInfo struct {
	Text string `json:"text"`
	From string `json:"from"`
	// IsExternal 表示引用的是其它聊天里的消息（频道引用属于这类）。
	// 这本身就是信号：把外部频道的推广内容搬进群，比回复群友更可疑。
	IsExternal bool `json:"is_external"`
	Length     int  `json:"length"`
}

// quotedInfo 取出被引用内容。没有引用则返回 nil ——
// 空的 quoted 块会给模型一个有引用但内容为空的假信号。
//
// 取值优先级：手动选中的引文 > 跨聊天引用摘要 > 同群原消息。
// 手动引文优先，是因为它才是群里实际显示、其他成员看得到的那一段。
func quotedInfo(m *tg.Message) *adQuotedInfo {
	q := &adQuotedInfo{}

	if m.ExternalReply != nil {
		q.IsExternal = true
		q.Text = m.ExternalReply.Text
		if q.Text == "" {
			q.Text = m.ExternalReply.Caption
		}
		// 频道引用同样可能是一张联系人卡片或一个投票。
		if extra := payloadLines(&m.ExternalReply.MsgPayload); len(extra) > 0 {
			q.Text = strings.TrimSpace(q.Text + "\n" + strings.Join(extra, "\n"))
		}
		if c := m.ExternalReply.Chat; c != nil {
			q.From = c.Title
		}
	} else if r := m.ReplyToMessage; r != nil {
		q.Text = msgText(r)
		q.From = senderName(r.From)
		if r.From == nil {
			q.From = ""
		}
	}

	if m.Quote != nil && m.Quote.Text != "" {
		q.Text = m.Quote.Text
	}
	if q.Text == "" {
		return nil
	}
	q.Length = len([]rune(q.Text))
	// 与本人正文同一套上限：引用内容同样完全由攻击者控制。
	q.Text = core.TruncateRunes(q.Text, adStateTextLimit)
	return q
}

// adState 是 systemone 的 state，也是大模型复判的输入。
// 两级判定共用同一份，保证它们看到的信息完全一致。
type adState struct {
	Chat          adChatInfo    `json:"chat"`
	Message       adMessageInfo `json:"message"`
	Sender        senderProfile `json:"sender"`
	RecentContext []core.CtxMsg `json:"recent_context"`
	// Quoted 是本人引用/回复的内容，无引用时为 nil。
	Quoted *adQuotedInfo `json:"quoted,omitempty"`
	// ReviewHistory 只在 /check 复查时非空：该用户在本群的全部留底，
	// 一次性交给模型。非空即代表这是对账号的整体复查而非对单条
	// 消息的判定，两级提示词都据此切换判断口径。
	ReviewHistory []core.CtxMsg `json:"review_history,omitempty"`
	// JoinCheck 为真表示这是**进群冷判定**：此人一条消息都还没发过，
	// message 整块是空的，全部证据在 sender 里。提示词据此切换口径，
	// 不加这个标记的话模型会把正文为空当成规避形态而误判。
	JoinCheck bool `json:"join_check,omitempty"`
	// PrewarmCheck 为真表示这是**前置号复核**：新成员的首条短消息，
	// 问的是账号层面是不是批量注册的广告前置号。提示词据此切换口径。
	PrewarmCheck        bool   `json:"prewarm_check,omitempty"`
	KnownAdPatterns     string `json:"known_ad_patterns"`
	KnownFalsePositives string `json:"known_false_positives"`
	// MatchedRules 是命中的非强制必封规则（强证据，不是判决）。enforce
	// 规则命中时已在同步段直接处置，不会走到送检；这里只放需要 AI 复核的。
	MatchedRules []MatchedRule `json:"matched_rules,omitempty"`
}

// ProfileEmpty 报告账号资料是否为空壳：简介、用户名都没有，名字也只有
// 空白。前置号识别的信号之一，单凭它不构成判断。
func (p senderProfile) ProfileEmpty() bool {
	return strings.TrimSpace(p.Bio) == "" &&
		strings.TrimSpace(p.Username) == "" &&
		strings.TrimSpace(p.FirstName+p.LastName) == ""
}

// hasMediaCarrier 报告这条消息是否真的携带媒体（图、贴纸、视频、文件、
// 语音、联系人卡片、投票等）。依据是媒体字段本身，不能以有无正文判断，
// 否则纯图、纯贴纸会被报成 has_media=false，与实际相反。
func hasMediaCarrier(m *tg.Message) bool {
	return len(m.Photo) > 0 || m.Sticker != nil || m.Video != nil ||
		m.Document != nil || m.Audio != nil || m.Animation != nil ||
		m.Contact != nil || m.Poll != nil || m.Venue != nil || m.Invoice != nil
}

// buildProfile 组装发送者画像。
func buildProfile(b *core.Bot, m *tg.Message, gm groupMember, now int64) senderProfile {
	p := senderProfile{PriorAdHits: gm.AdHits, MsgsInGroup: gm.MsgCount}
	if m.From != nil {
		p.UserID = m.From.ID
		p.Username = m.From.Username
		p.FirstName = m.From.FirstName
		p.LastName = m.From.LastName
		p.IsPremium = m.From.IsPremium
		p.IsChannel = m.From.ID < 0 // 见 senderOf
	}

	// joined_at 缺失时退回 first_seen，并如实告知模型年龄未知——
	// 否则它会把一个老成员当成刚进来的。
	base := gm.JoinedAt
	p.AgeKnown = base > 0
	if !p.AgeKnown {
		base = gm.FirstSeen
	}
	if base > 0 && now > base {
		p.AgeHours = (now - base) / 3600
	}
	return p
}

// buildState 组装送检载荷。recent_context 取自本人留底并排除当前这条，
// 所以当前消息先留底再调用它是安全的（HandleGroupMessage 正是如此）。
func buildState(b *core.Bot, snap *store.Snapshot, m *tg.Message, p senderProfile) adState {
	text := msgText(m)
	st := adState{
		Message: adMessageInfo{
			Text:        core.TruncateRunes(text, adStateTextLimit),
			HasMedia:    hasMediaCarrier(m),
			IsForwarded: len(m.ForwardOrigin) > 0,
			IsEdited:    m.EditDate != 0,
			// 长度报原文的，不报截断后的：长度本身是判定信号
			// （刷屏、超长广告文案），截断载荷不该把它一起抹掉。
			Length:        len([]rune(text)),
			MentionedBots: mentionedBots(text),
		},
		Sender: p,
	}
	// 群内引用不送检：引用群友的消息是别人的话，引用一条广告提醒管理员
	// 可能是举报行为，送检会让引用者看起来在说那段广告。外部聊天引用保留 ——
	// 那是正文为空、载荷全在引用里的主要规避形态（把频道广告搬进群），
	// 也是 quoted 字段存在的意义。
	if q := quotedInfo(m); q != nil && q.IsExternal {
		st.Quoted = q
	}
	if m.Chat != nil {
		st.Chat = adChatInfo{ID: m.Chat.ID, Title: m.Chat.Title}
		// 第一条消息没有此前可带：msgs_in_group 是含本条的计数，
		// 为 1 时直接跳过，省一次留底查询。
		if p.MsgsInGroup > 1 {
			st.RecentContext = recentOwn(b.Store, m,
				int(snap.BotSettingInt(b.BotID(), "antiad_ctx_msgs", 6)))
		}
	}

	// 显性特征由 TG 的 entities 直接给出，结构化标注比让模型
	// 自己从正文里数链接可靠得多。
	ents := m.Entities
	if len(ents) == 0 {
		ents = m.CaptionEntities
	}
	for _, e := range ents {
		switch e.Type {
		case "url", "text_link":
			st.Message.HasLink = true
		case "mention", "text_mention":
			st.Message.HasMention = true
		}
	}

	st.KnownAdPatterns, st.KnownFalsePositives = splitDigest(snap.Setting("antiad_digest"))
	// 启用的必封规则命中单独成字段：它比形态摘要强，提示词按强证据
	// 对待（见 matchedRulesClause），但仍由模型结合上下文复核后处置。
	//
	// 匹配用 judgingText（正文 + 外部引用）：正文为空、载荷全在外部引用
	// 里的规避形态同样要命中；群内引用是别人的话（引用广告提醒管理员），
	// 不算引用者发出的载荷。
	st.MatchedRules = MatchedRuleInfos(snap, judgingText(m))
	return st
}

// ctxTextLimit 是单条上下文的字符上限。上下文用来让模型看到此人近来
// 说过什么，不需要全文；不截断的话一条长消息就能把每次判定的 input
// token 占满。
const ctxTextLimit = 200

// recentOwn 取发送者本人在本群最近 n 条留底（不含当前这条），由旧到新。
//
// 只取本人的：别人的发言（尤其是带［引用］载荷的广告）混进来，模型
// 会把它当成本条消息引用的内容，导致误判。从留底取而不是另开
// 一个内存环：环按群共享、容量有限，活跃群里本人的上一条容易被挤出；
// 留底按人建了索引，重启也不丢。
//
// 已处罚过的消息不进来（markPunished）：它们已从群里删掉，若留在
// 上下文会被当成此人刚发的广告，在后续判定中反复把人推向封禁。
func recentOwn(s *store.Store, m *tg.Message, n int) []core.CtxMsg {
	if n <= 0 || m.From == nil {
		return nil
	}
	var out []core.CtxMsg
	// 多取一条：当前这条通常已经留底，过滤掉之后仍有 n 条。
	for _, h := range loadUserMessages(s, m.Chat.ID, m.From.ID, n+1, true) {
		if h.MessageID == m.MessageID {
			continue
		}
		// 留底存的是 displayText（含引用）。引用段是别人的话，但不能
		// 直接剥掉：剥掉会丢本人正文的对话语境；换成带归属的标记可同时
		// 避免两类误判（见 historyText）。
		t := historyText(h.Text)
		if strings.TrimSpace(t) == "" {
			continue
		}
		out = append(out, core.CtxMsg{Name: senderName(m.From),
			Text: core.TruncateRunes(t, ctxTextLimit), At: h.At})
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

const (
	DigestAdHeader = "【近期广告形态】"
	DigestFPHeader = "【易误杀的正常形态】"

	// 摘要样本的围栏。样本是攻击者完全控制的群消息原文，必须明确
	// 标成数据区，且 system 提示词里要声明围栏内不得当指令执行。
	digestFenceOpen  = "<<<"
	digestFenceClose = ">>>"
)

// adStateTextLimit 是送检正文的字符上限。这是整条链路上唯一由攻击者
// 自由控制长度的字段（上下文/日志/告警/样本/摘要都各有上限），而广告的
// 判定特征从来不在第 2000 个字之后。
const adStateTextLimit = 2000

// splitDigest 把形态摘要切成正例与反例两段。
// 标题缺失（管理员手工改乱了）时整段当作广告形态，
// 不因为格式问题丢掉全部学习成果。
func splitDigest(d string) (patterns, falsePositives string) {
	d = strings.TrimSpace(d)
	if d == "" {
		return "", ""
	}
	body := strings.TrimPrefix(d, DigestAdHeader)
	idx := strings.Index(body, DigestFPHeader)
	if idx < 0 {
		return strings.TrimSpace(body), ""
	}
	return strings.TrimSpace(body[:idx]),
		strings.TrimSpace(body[idx+len(DigestFPHeader):])
}

// isNewbie 判定是否按新人档处置。两个条件任一命中即为新人：
// 刚进群，或进群久了但几乎没发过言（潜伏号）。
//
// 两条界限都可以按 bot 覆盖：技术群和闲聊群对新人的合理定义不一样，
// 而这两个群往往归不同的人管。
func isNewbie(b *core.Bot, snap *store.Snapshot, p senderProfile) bool {
	id := b.BotID()
	// 年龄轴只在进群时间确实已知时参与判定。AgeKnown 为假时 AgeHours 退回
	// first_seen，而 first_seen 是 bot 第一次见到这个人发言——首次启用时
	// 全部老成员的 first_seen 都约等于此刻，采信它会把老成员一并判为新人，
	// 与老成员低风险的口径相反。这也与提示词里
	// age_known 为 false 时按普通成员对待、不要因此加重怀疑保持同向。
	if p.AgeKnown && p.AgeHours < snap.BotSettingInt(id, "antiad_new_hours", 72) {
		return true
	}
	// 发言数轴不受年龄是否已知影响：它本身就是资历的独立证据。
	return p.MsgsInGroup < snap.BotSettingInt(id, "antiad_new_msgs", 10)
}
