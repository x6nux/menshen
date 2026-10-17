package antiad

import (
	"encoding/json"
	"fmt"
	"strings"

	"menshen/internal/tg"
)

// msgText 返回参与判定的正文：本人正文（为空时取图片/视频配文），再加上
// 消息附带的非文字载荷，每种一行、带方括号前缀。
//
// 只看 Text 会漏掉图片 + 配文这一整类；只看 Text/Caption 又会漏掉联系人
// 卡片、投票、文件名、文字背后的链接、内联按钮 —— 它们在无正文守门处被
// 直接放过，而卡片的名字与号码本身就是广告。
func msgText(m *tg.Message) string {
	var parts []string
	if m.Text != "" {
		parts = append(parts, m.Text)
	} else if m.Caption != "" {
		parts = append(parts, m.Caption)
	}
	if m.Vision != "" {
		parts = append(parts, m.Vision)
	}
	// 分两次遍历而不是 append 拼接两个切片：后者可能写进 Entities 的
	// 备用容量，与并发读同一条消息的调用方产生数据竞争。
	for _, es := range [][]tg.MessageEntity{m.Entities, m.CaptionEntities} {
		for _, e := range es {
			if e.Type == "text_link" && e.URL != "" {
				parts = append(parts, "［隐藏链接］"+e.URL)
			}
		}
	}
	parts = append(parts, payloadLines(&m.MsgPayload)...)
	parts = append(parts, fieldLines(m)...)
	if m.RichMessage != nil {
		parts = append(parts, richLines(m.RichMessage)...)
	}
	if m.ReplyMarkup != nil {
		for _, row := range m.ReplyMarkup.InlineKeyboard {
			for _, btn := range row {
				parts = append(parts, joinNonEmpty("［按钮］", btn.Text, btn.URL))
			}
		}
	}
	if m.ViaBot != nil && m.ViaBot.Username != "" {
		parts = append(parts, "［经由］@"+m.ViaBot.Username)
	}
	if m.GuestBot != nil {
		parts = append(parts, "［访客 bot］@"+m.GuestBot.Username)
	}
	if src := forwardSource(m.ForwardOrigin); src != "" {
		parts = append(parts, "［转发自］"+src)
	}
	return strings.Join(parts, "\n")
}

// payloadLines 把非文字载荷渲染成带类型前缀的文字，每种一行。
// 前缀让模型分得清哪段是卡片、哪段是文件名，而不是本人说的话。
func payloadLines(p *tg.MsgPayload) []string {
	var out []string
	add := func(prefix string, fields ...string) {
		if s := joinNonEmpty(prefix, fields...); s != prefix {
			out = append(out, s)
		}
	}
	if c := p.Contact; c != nil {
		add("［联系人卡片］", strings.TrimSpace(c.FirstName+" "+c.LastName), c.PhoneNumber)
	}
	if v := p.Poll; v != nil {
		f := []string{v.Question}
		for _, o := range v.Options {
			f = append(f, o.Text)
		}
		add("［投票］", f...)
	}
	if v := p.Venue; v != nil {
		add("［地点］", v.Title, v.Address)
	}
	if v := p.Game; v != nil {
		add("［游戏］", v.Title, v.Description)
	}
	if v := p.Invoice; v != nil {
		add("［账单］", v.Title, v.Description)
	}
	if v := p.Checklist; v != nil {
		f := []string{v.Title}
		for _, t := range v.Tasks {
			f = append(f, t.Text)
		}
		add("［清单］", f...)
	}
	if v := p.Audio; v != nil {
		add("［音频］", v.Title, v.Performer, v.FileName)
	}
	if v := p.Document; v != nil {
		add("［文件］", v.FileName)
	}
	// 视频与动图另有抽帧识图；文件名本身仍是攻击者可控的文字。
	for _, v := range []*tg.MovingMedia{p.Video, p.Animation} {
		if v != nil {
			add("［文件］", v.FileName)
		}
	}
	if v := p.Story; v != nil && v.Chat != nil {
		add("［故事］", chatLabel(v.Chat.Title, v.Chat.Username))
	}
	if v := p.LinkPreviewOptions; v != nil {
		add("［预览］", v.URL)
	}
	// 付费媒体把内容锁在星数后面，能读到的只有价格、项数与配文。
	if v := p.PaidMedia; v != nil {
		f := []string{fmt.Sprintf("%d 项，%d 星", len(v.PaidMedia), v.StarCount)}
		for _, it := range v.PaidMedia {
			f = append(f, it.Caption)
		}
		add("［付费媒体］", f...)
	}
	if v := p.Giveaway; v != nil {
		add("［抽奖］", v.PrizeDescription, fmt.Sprintf("%d 名", v.WinnerCount))
	}
	if v := p.GiveawayWinners; v != nil {
		add("［开奖］", v.PrizeDescription, fmt.Sprintf("%d 名", v.WinnerCount))
	}
	// 实况照片、视频消息、语音、定位、骰子只有媒体本身或数值，没有可判的
	// 文字：不进正文（进了的话每条语音都要花一次 AI），只在 hasMediaCarrier
	// 里算作媒体；实况照片的静态图另有识图路径（见 visualOf）。
	return out
}

// fieldLines 渲染只挂在 Message 上的载荷：礼物、分享、Web App 数据、支付、
// 投票选项与清单任务的增删、群改名、置顶消息。
//
// 它们不在 MsgPayload 里（ExternalReplyInfo 没有这些字段），但同样要进
// 同一段正文：正文、留底、告警、去重共用一个口径。
func fieldLines(m *tg.Message) []string {
	var out []string
	add := func(prefix string, fields ...string) {
		if s := joinNonEmpty(prefix, fields...); s != prefix {
			out = append(out, s)
		}
	}
	if v := m.Gift; v != nil {
		add("［礼物］", v.Label(), v.Text)
	}
	if v := m.GiftUpgradeSent; v != nil {
		add("［礼物升级］", v.Label(), v.Text)
	}
	if v := m.UniqueGift; v != nil {
		add("［独特礼物］", v.Label(), v.Text)
	}
	if m.GiveawayCreated != nil {
		out = append(out, "［抽奖开始］")
	}
	if v := m.GiveawayCompleted; v != nil {
		add("［抽奖结束］", fmt.Sprintf("%d 名中奖", v.WinnerCount))
	}
	if v := m.UsersShared; v != nil {
		f := []string{fmt.Sprintf("%d 人", len(v.Users))}
		for _, u := range v.Users {
			f = append(f, chatLabel(strings.TrimSpace(u.FirstName+" "+u.LastName), u.Username))
		}
		add("［分享用户］", f...)
	}
	if v := m.ChatShared; v != nil {
		add("［分享聊天］", chatLabel(v.Title, v.Username))
	}
	if v := m.WebAppData; v != nil {
		add("［网页数据］", v.ButtonText, v.Data)
	}
	if v := m.SuccessfulPayment; v != nil {
		add("［支付］", v.InvoicePayload,
			fmt.Sprintf("%d %s", v.TotalAmount, v.Currency), v.Order())
	}
	if v := m.RefundedPayment; v != nil {
		add("［退款］", v.InvoicePayload, fmt.Sprintf("%d %s", v.TotalAmount, v.Currency))
	}
	if v := m.ProximityAlert; v != nil {
		add("［雷达提醒］", displayUserName(v.Traveler), displayUserName(v.Watcher),
			fmt.Sprintf("%d 米", v.Distance))
	}
	if m.ConnectedWebsite != "" {
		add("［登录网站］", m.ConnectedWebsite)
	}
	if v := m.PaidMessagePrice; v != nil {
		add("［付费消息］", fmt.Sprintf("%d 星", v.StarCount))
	}
	if v := m.PollOptionAdded; v != nil {
		add("［投票新增选项］", v.Text())
	}
	if v := m.PollOptionDeleted; v != nil {
		add("［投票删除选项］", v.Text())
	}
	if v := m.ChecklistTasksAdded; v != nil {
		add("［清单新增任务］", v.Texts()...)
	}
	if v := m.ChecklistTasksDone; v != nil {
		add("［清单完成任务］", v.Texts()...)
	}
	if m.NewChatTitle != "" {
		add("［群改名］", m.NewChatTitle)
	}
	if v := m.PinnedMessage; v != nil {
		add("［置顶消息］", oneLine(v.Text), oneLine(v.Caption))
	}
	return out
}

// richLines 渲染富消息（Bot API 10.1）。
//
// 富消息的正文全在块里，text 与 reply_markup 都是空的：不渲染它，一条
// 有正文、有链接、有按钮的广告在判定侧就只是一条空消息。块逐层展开，
// 与群里看到的内容对齐。
func richLines(msg *tg.RichMessage) []string {
	var out []string
	for _, b := range msg.Blocks {
		out = append(out, blockLines(b, "")...)
	}
	return out
}

// blockLines 渲染一个富消息块。depth 是嵌套层数，只用来避免异常报文
// （互相引用的容器块）把渲染撑爆。
func blockLines(b tg.RichBlock, depth string) []string {
	if len(depth) > 4 {
		return nil
	}
	var out []string

	text := func(prefix string, nodes ...*tg.RichText) {
		f := make([]string, 0, len(nodes))
		for _, n := range nodes {
			f = append(f, n.Line())
		}
		if s := joinNonEmpty(prefix, f...); s != prefix {
			out = append(out, s)
		}
	}

	switch b.Type {
	case "paragraph", "heading", "pre", "footer", "blockquote",
		"expandable_blockquote", "pullquote", "thinking":
		text("［富消息］", b.Text, b.Credit)
	case "mathematical_expression":
		if s := strings.TrimSpace(b.Expression); s != "" {
			out = append(out, "［富消息］"+s)
		}
	case "buttons":
		for _, btn := range b.Buttons {
			if s := joinNonEmpty("［按钮］", btn.Parts()...); s != "［按钮］" {
				out = append(out, s)
			}
		}
	case "list":
		for _, it := range b.Items {
			if s := strings.TrimSpace(it.Label); s != "" {
				out = append(out, "［富消息］"+s)
			}
			for _, sub := range it.Blocks {
				out = append(out, blockLines(sub, depth+".")...)
			}
		}
	case "table":
		for _, row := range b.Cells {
			var f []string
			for _, cell := range row {
				f = append(f, cell.Text.Line())
			}
			if s := joinNonEmpty("［表格］", f...); s != "［表格］" {
				out = append(out, s)
			}
		}
	case "divider", "anchor":
		// 分隔线与锚点没有内容，只是个位置标记。
	case "photo", "animation", "audio", "document", "video", "voice_note":
		out = append(out, mediaBlockLine(b))
		if b.Caption != nil {
			text("［配文］", b.Caption.Text, b.Caption.Credit)
		}
	case "map":
		if b.Location != nil {
			out = append(out, fmt.Sprintf("［地图］%.4f,%.4f", b.Location.Latitude, b.Location.Longitude))
		}
	default:
		// 容器类块（collage / slideshow / details / 引用块的子块）与
		// 还没见过的新块类型：先展开子块，再把类型名留痕 —— 内容读不到
		// 也要看得出「这里有东西」，新块类型就是这样被发现的。
		for _, sub := range b.Blocks {
			out = append(out, blockLines(sub, depth+".")...)
		}
		if b.Summary != nil {
			text("［富消息］", b.Summary)
		}
		if b.Caption != nil {
			text("［配文］", b.Caption.Text, b.Caption.Credit)
		}
		if len(out) == 0 && b.Type != "" {
			out = append(out, "［富消息·未识别的块］"+b.Type)
		}
	}
	return out
}

// mediaBlockLine 渲染富消息里的媒体块。只取文件名一类的文字，媒体本身
// 不下载：文件名是攻击者可控的文字，图片则至少留下规格，让「这里有媒体」
// 这件事不丢。
func mediaBlockLine(b tg.RichBlock) string {
	kind := map[string]string{
		"photo": "图片", "animation": "动图", "audio": "音频",
		"document": "文件", "video": "视频", "voice_note": "语音",
	}[b.Type]
	f := []string{kind}
	var named *tg.FileNamed
	switch b.Type {
	case "animation":
		named = b.Animation
	case "audio":
		named = b.Audio
	case "document":
		named = b.Document
	case "video":
		named = b.Video
	case "voice_note":
		named = b.VoiceNote
	}
	if named != nil {
		f = append(f, named.FileName)
	}
	if len(b.Photo) > 0 {
		f = append(f, fmt.Sprintf("%d 张", len(b.Photo)))
	}
	return joinNonEmpty("［富消息·媒体］", f...)
}

// hasPayload 报告这条消息有没有可识别的载荷。
//
// 为假说明 TG 推来的是一个我们没有解的消息类型：客户端看得到正文，
// 判定侧只剩来源标注。调用方据此留下原始报文（见 noteUnknownPayload）——
// 这类缺口只有看得见才会被补上。
//
// 判据要宽：有媒体就算有载荷。否则「转发的无配文视频」这种正常消息
// 会被反复当成新类型留档。
func hasPayload(m *tg.Message) bool {
	if m.Text != "" || m.Caption != "" || m.Vision != "" {
		return true
	}
	for _, es := range [][]tg.MessageEntity{m.Entities, m.CaptionEntities} {
		for _, e := range es {
			if e.Type == "text_link" && e.URL != "" {
				return true
			}
		}
	}
	if m.RichMessage != nil || m.ReplyMarkup != nil {
		return true
	}
	// service 消息本来就不带载荷，不算「没解出来」。
	if m.NewChatMembers != nil || m.ServiceSeen() || hasMediaCarrier(m) {
		return true
	}
	if _, seeable := visualOf(m); seeable {
		return true
	}
	// 引用段不算载荷：回复消息本身必然带内容，只看到引用说明带的那份
	// 内容没解出来，正是要留档的形态。
	//
	// 便宜的判据都试过才渲染：普通消息在第一个 return 就出了这个函数，
	// 剩下这些只有不携带文字、也不是媒体的消息才会走到。
	return len(payloadLines(&m.MsgPayload)) > 0 || len(fieldLines(m)) > 0
}

// joinNonEmpty 以空格拼接非空字段并加上前缀；全空时只返回前缀。
func joinNonEmpty(prefix string, fields ...string) string {
	var f []string
	for _, s := range fields {
		if s = strings.TrimSpace(s); s != "" {
			f = append(f, s)
		}
	}
	return prefix + strings.Join(f, " ")
}

func chatLabel(title, username string) string {
	if username != "" {
		return strings.TrimSpace(title + " @" + username)
	}
	return title
}

// forwardSource 取转发来源的名字。从引流频道整条转发进群是常见形态，
// 来源名本身就是信号，只标 is_forwarded 模型看不到它。
func forwardSource(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var o struct {
		SenderUser     *tg.TGUser `json:"sender_user"`
		SenderUserName string     `json:"sender_user_name"`
		SenderChat     *tg.Chat   `json:"sender_chat"`
		Chat           *tg.Chat   `json:"chat"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return ""
	}
	switch {
	case o.SenderUser != nil:
		return chatLabel(strings.TrimSpace(o.SenderUser.FirstName+" "+o.SenderUser.LastName),
			o.SenderUser.Username)
	case o.SenderUserName != "":
		return o.SenderUserName
	case o.Chat != nil:
		return chatLabel(o.Chat.Title, o.Chat.Username)
	case o.SenderChat != nil:
		return chatLabel(o.SenderChat.Title, o.SenderChat.Username)
	}
	return ""
}

// quoteMark 是拼接被引用内容的标记，displayText / judgingText / 留底回填
// （stripQuotedTail）共用同一个字面量。
const quoteMark = "［引用］"

// displayText 是这条消息在群里实际可见的全部文字：本人正文加上被引用的
// 内容（引用本群频道／群身份发的帖子时只有本人正文，见 quotedInfo）。
//
// 判空、去重、上下文、落库、告警五处共用它，因为引用规避会把本人正文
// 压到空或一两个字符：
//   - 只看本人正文判空 → 整条漏判
//   - 只拿本人正文做去重 → 所有空正文消息哈希成同一个 key，第一条之后
//     全被当成重复跳过
//   - 只拿本人正文落库/告警 → 管理员和形态总结看到的都是空白
//
// 送检载荷不用它：那里 message.text 与 quoted 必须分开，保住归属；
// 规则匹配用 judgingText（群内引用不参与）。
func displayText(m *tg.Message) string {
	return joinQuote(msgText(m), quotedInfo(m))
}

// judgingText 是真正参与判定与规则匹配的文本：本人正文 + 外部聊天引用。
//
// 群内引用是别人的话：引用一条广告来提醒管理员 / 批评 / 询问属于举报与
// 讨论，把那段广告算到引用者头上会把他判成发广告的人。外部引用（频道、
// 其它聊天）保留 —— 那是正文为空、载荷全在引用里的主要规避形态。留底、
// 去重、告警仍走 displayText（审计口径，引用照实保留；本群频道／群身份
// 发的帖子除外，那段连留底也不进，见 quotedInfo）。
func judgingText(m *tg.Message) string {
	q := quotedInfo(m)
	if q == nil || !q.IsExternal {
		return msgText(m)
	}
	return joinQuote(msgText(m), q)
}

// joinQuote 把正文与引用拼成可见文本；正文为空时直接给引用段。
func joinQuote(t string, q *adQuotedInfo) string {
	if q == nil {
		return t
	}
	if t == "" {
		return quoteMark + q.Text
	}
	return t + "\n" + quoteMark + q.Text
}

// historyQuoteMark 是历史条目里引用段的标记：与 quoteMark 同指一段内容，
// 但写明归属 —— 引用段是被引用者的原话，不是发言者本人说的。
const historyQuoteMark = "［引用·别人的话］"

// historyText 把留底文本转成喂给模型的历史条目（recent_context 与 /check
// 的 review_history 都从这里走）。
//
// 引用段不能直接剥掉，也不能原样保留：
//   - 剥掉：本人正文失去对话语境，整体复查时可能被误读。
//   - 原样保留：引用广告提醒管理员的人，历史里那段广告看起来像他在发。
//
// 带归属的标记让模型既能用引用段理解本人在回应什么，又不会把别人的话
// 记在发言者头上。
func historyText(text string) string {
	// 引用段由 displayText 接在正文后面（换行 + 标记），或整条都是引用；
	// 只改第一处标记 —— 引用内容里再出现“［引用］”字样是原文，不动。
	if strings.HasPrefix(text, quoteMark) {
		return historyQuoteMark + text[len(quoteMark):]
	}
	if i := strings.Index(text, "\n"+quoteMark); i >= 0 {
		return text[:i] + "\n" + historyQuoteMark +
			text[i+len("\n"+quoteMark):]
	}
	return text
}

// stripQuotedTail 去掉留底文本里的引用段，返回本人正文。
//
// 留底存的是 displayText（正文 + 引用），审计口径要本人正文时用它。
// 喂给模型的历史条目不走这里 —— 单纯剥掉会丢掉本人正文的对话语境，
// 那走 historyText（引用保留、换带归属的标记）。
func stripQuotedTail(text string) string {
	// 引用段由 displayText 接在正文后面（换行 + 标记），或整条都是引用。
	if i := strings.Index(text, "\n"+quoteMark); i >= 0 {
		return strings.TrimRight(text[:i], "\n")
	}
	if strings.HasPrefix(text, quoteMark) {
		return ""
	}
	return text
}

// senderName 取一个供上下文展示的名字，优先 username。
func senderName(u *tg.TGUser) string {
	if u == nil {
		return "?"
	}
	if u.Username != "" {
		return "@" + u.Username
	}
	if u.FirstName != "" {
		return u.FirstName
	}
	return fmt.Sprint(u.ID)
}

// displayUserName 把 TG 资料拼成“名 姓 (@username)”的形式。
func displayUserName(u *tg.TGUser) string {
	if u == nil {
		return ""
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if u.Username != "" {
		if name != "" {
			return name + " (@" + u.Username + ")"
		}
		return "@" + u.Username
	}
	return name
}
