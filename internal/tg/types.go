package tg

import "encoding/json"

// ---- Telegram 数据结构（只声明用得到的字段）----

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
	// ChatMember 只有在 allowed_updates 里显式声明才会推送。
	// 它是「用户何时进群」的唯一可靠来源——TG 没有反查接口。
	ChatMember *ChatMemberUpdated `json:"chat_member"`
	// MyChatMember 是 bot 自身的成员状态变化，用于发现
	// 「被加进群但没给管理员权限」这种静默失效的情况。
	MyChatMember *ChatMemberUpdated `json:"my_chat_member"`
}

type Message struct {
	MessageID int64   `json:"message_id"`
	Date      int64   `json:"date"`
	From      *TGUser `json:"from"`
	Chat      *Chat   `json:"chat"`
	Text      string  `json:"text"`
	// Caption 是图片/视频的配文。广告经常挂在图片下面，
	// 只看 Text 会把这一整类漏掉。
	Caption        string    `json:"caption"`
	NewChatMembers []*TGUser `json:"new_chat_members"`
	Entities       []struct {
		Type string `json:"type"` // url / text_link / mention / ...
	} `json:"entities"`
	CaptionEntities []struct {
		Type string `json:"type"`
	} `json:"caption_entities"`
	// ForwardOrigin 只判空，用于给 AI 标注「这是转发来的」
	ForwardOrigin json.RawMessage `json:"forward_origin"`

	// 引用/回复的三种形态。广告号会把正文压到一两个字符、把载荷全放进
	// 引用块（典型：引用一条频道广告，自己只回一个「u」），不接这三个
	// 字段就等于对这整类规避形态完全失明。
	//
	// ReplyToMessage —— 回复同一个群里的消息，TG 给出整条原消息。
	// ExternalReply  —— 回复其它聊天里的消息（频道引用属于这类），
	//                   TG 不给原消息本体，只给一份摘要。
	// Quote          —— 用户手动选中的那一段引文，是群里实际显示的内容。
	ReplyToMessage *Message           `json:"reply_to_message"`
	ExternalReply  *ExternalReplyInfo `json:"external_reply"`
	Quote          *TextQuote         `json:"quote"`
}

// TextQuote 是用户手动选中的引文片段。
type TextQuote struct {
	Text string `json:"text"`
}

// ExternalReplyInfo 是「回复其它聊天里的消息」时 TG 给的原消息摘要。
// Chat 为消息来源（频道引用时就是那个频道）。
type ExternalReplyInfo struct {
	Chat    *Chat  `json:"chat"`
	Text    string `json:"text"`
	Caption string `json:"caption"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *TGUser  `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type TGUser struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	IsPremium bool   `json:"is_premium"`
	IsBot     bool   `json:"is_bot"`
}

type Chat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"` // "private" | "group" | "supergroup" | "channel"
	Title string `json:"title"`
}

// ChatMemberUpdated 对应 TG 的 ChatMemberUpdated。
type ChatMemberUpdated struct {
	Chat          *Chat           `json:"chat"`
	From          *TGUser         `json:"from"`
	Date          int64           `json:"date"`
	OldChatMember *ChatMemberInfo `json:"old_chat_member"`
	NewChatMember *ChatMemberInfo `json:"new_chat_member"`
}

type ChatMemberInfo struct {
	User   *TGUser `json:"user"`
	Status string  `json:"status"`
}

type ChatMemberResp struct {
	OK     bool `json:"ok"`
	Result struct {
		Status string `json:"status"`
	} `json:"result"`
}

// ChatFullResp 是 getChat 的响应。只取 bio：广告号的强特征常常写在
// 个人简介里（联系方式、价目、引流话术），而这个字段只有 getChat 给。
type ChatFullResp struct {
	OK     bool `json:"ok"`
	Result struct {
		Bio string `json:"bio"`
	} `json:"result"`
}
