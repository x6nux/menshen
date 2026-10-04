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
// 只看 Text 会把「图片 + 配文」这一整类漏光；只看 Text/Caption 又会把
// 联系人卡片、投票、文件名、藏在「点这里」背后的链接、内联按钮漏光——
// 它们在「无正文」守门处被直接放过，而卡片的名字与号码本身就是广告。
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
	// 备用容量，与并发读同一条消息的人互相踩。
	for _, es := range [][]tg.MessageEntity{m.Entities, m.CaptionEntities} {
		for _, e := range es {
			if e.Type == "text_link" && e.URL != "" {
				parts = append(parts, "［隐藏链接］"+e.URL)
			}
		}
	}
	parts = append(parts, payloadLines(&m.MsgPayload)...)
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
	for _, f := range []*tg.FileNamed{p.Document, p.Video, p.Animation} {
		if f != nil {
			add("［文件］", f.FileName)
		}
	}
	if v := p.Story; v != nil && v.Chat != nil {
		add("［故事］", chatLabel(v.Chat.Title, v.Chat.Username))
	}
	if v := p.LinkPreviewOptions; v != nil {
		add("［预览］", v.URL)
	}
	return out
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
// 来源名本身就是信号（「日赚频道」），只标 is_forwarded 模型看不到它。
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

// displayText 是「这条消息在群里实际可见的全部文字」：本人正文加上
// 被引用的内容。
//
// 判空、去重、上下文、落库、告警五处共用它，因为引用规避会把本人正文
// 压到空或一两个字符：
//   - 只看本人正文判空 → 整条漏判
//   - 只拿本人正文做去重 → 所有空正文消息哈希成同一个 key，
//     第一条之后全被当成重复吞掉
//   - 只拿本人正文落库/告警 → 管理员和形态总结看到的都是空白
//
// 送检载荷不用它：那里 message.text 与 quoted 必须分开，保住归属。
func displayText(m *tg.Message) string {
	t := msgText(m)
	q := quotedInfo(m)
	if q == nil {
		return t
	}
	if t == "" {
		return "［引用］" + q.Text
	}
	return t + "\n［引用］" + q.Text
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

// displayUserName 把 TG 资料拼成「名 姓 (@username)」。
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
