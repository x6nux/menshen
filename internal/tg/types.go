package tg

import "encoding/json"

// ---- Telegram 数据结构（只声明用得到的字段）----

type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
	// EditedMessage 是编辑过的消息。「先发正常内容、判过后再编辑成广告」
	// 是规避手法，必须重新送检；它同样要列进 allowed_updates 才收得到。
	EditedMessage *Message       `json:"edited_message"`
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
	// EditDate 非 0 表示这是编辑后的版本（edited_message）。
	EditDate int64 `json:"edit_date"`
	// SenderChat 是以频道/群身份发言时的真正发言者。此时 From 是占位的
	// Channel_Bot（所有频道共用一个），见 antiad 的 senderOf。
	SenderChat *Chat `json:"sender_chat"`
	// MediaGroupID 是相册 ID：配文只挂在其中一张上，判成广告时整组删。
	MediaGroupID string      `json:"media_group_id"`
	Photo        []PhotoSize `json:"photo"`
	Sticker      *Sticker    `json:"sticker"`
	// Vision 是识图模型对本条图片/贴纸的描述，由判定 worker 填入，不来自 TG。
	Vision string `json:"-"`
	Text   string `json:"text"`
	// Caption 是图片/视频的配文。广告经常挂在图片下面，
	// 只看 Text 会把这一整类漏掉。
	Caption string `json:"caption"`
	// 联系人卡片、投票、文件名等非文本载荷，见 MsgPayload。
	MsgPayload
	NewChatMembers  []*TGUser       `json:"new_chat_members"`
	Entities        []MessageEntity `json:"entities"`
	CaptionEntities []MessageEntity `json:"caption_entities"`
	// ForwardOrigin 判空标注「这是转发来的」，另取来源名进正文。
	ForwardOrigin json.RawMessage `json:"forward_origin"`
	// ReplyMarkup 是消息自带的内联按钮。经由 inline bot 发出的广告
	// 常把引流链接挂在按钮上，正文只有一句无害的话。
	ReplyMarkup *struct {
		InlineKeyboard [][]struct {
			Text string `json:"text"`
			URL  string `json:"url"`
			// CallbackData 是回调按钮的数据。入站消息里很少见，但面板
			// 重绘卡片时要把它原样带回去（/log 卡片原地处置）。
			CallbackData string `json:"callback_data"`
		} `json:"inline_keyboard"`
	} `json:"reply_markup"`
	ViaBot *TGUser `json:"via_bot"`
	// GuestBotCallerUser 是召唤访客 bot（Bot API 10.0 guest mode）代发这条的人。
	// 被 @ 的 bot 不必是群成员就能往群里发一条，所以按召唤者论处。
	GuestBotCallerUser *TGUser `json:"guest_bot_caller_user"`
	// GuestBot 是被召唤的那个 bot，入口把 From 换成召唤者时存在这里，不来自 TG。
	GuestBot *TGUser `json:"-"`

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

// MessageEntity 是正文里的格式标注。text_link 的 URL 不在正文里，
// 是「点这里」背后藏着的链接，只有从这里才拿得到。
type MessageEntity struct {
	Type string `json:"type"` // url / text_link / mention / ...
	URL  string `json:"url"`
}

// FileNamed 是只取文件名的媒体。文件名是攻击者可控的文字，
// 「日入5000教程@xx.pdf」这类很常见。
type FileNamed struct {
	FileName string `json:"file_name"`
}

// MsgPayload 是 text/caption 之外携带文字的消息类型。Message 与
// ExternalReplyInfo 共用：频道引用同样可能是一张联系人卡片或一个投票。
// 纯媒体（无配文的图片、贴纸、语音、定位、骰子）没有文字，不在此列。
type MsgPayload struct {
	// Contact 是「分享联系人」卡片。名字与号码本身就是载荷
	// （「XX交流群 + 电话」是典型引流形态）。
	Contact *Contact `json:"contact"`
	Poll    *struct {
		Question string `json:"question"`
		Options  []struct {
			Text string `json:"text"`
		} `json:"options"`
	} `json:"poll"`
	Venue *struct {
		Title   string `json:"title"`
		Address string `json:"address"`
	} `json:"venue"`
	Game *struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	} `json:"game"`
	Invoice *struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	} `json:"invoice"`
	Checklist *struct {
		Title string `json:"title"`
		Tasks []struct {
			Text string `json:"text"`
		} `json:"tasks"`
	} `json:"checklist"`
	Audio *struct {
		Title     string `json:"title"`
		Performer string `json:"performer"`
		FileName  string `json:"file_name"`
	} `json:"audio"`
	Document  *FileNamed `json:"document"`
	Video     *FileNamed `json:"video"`
	Animation *FileNamed `json:"animation"`
	Story     *struct {
		Chat *Chat `json:"chat"`
	} `json:"story"`
	LinkPreviewOptions *struct {
		URL string `json:"url"`
	} `json:"link_preview_options"`
}

// Contact 是联系人卡片。
type Contact struct {
	PhoneNumber string `json:"phone_number"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	UserID      int64  `json:"user_id"`
}

// PhotoSize 是图片的一档尺寸。TG 的 photo 数组按从小到大排。
type PhotoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
}

// Sticker 是贴纸。动态（TGS）与视频（WEBM）贴纸没法当图片看，只能看缩略图。
type Sticker struct {
	FileID       string     `json:"file_id"`
	FileUniqueID string     `json:"file_unique_id"`
	IsAnimated   bool       `json:"is_animated"`
	IsVideo      bool       `json:"is_video"`
	Thumbnail    *PhotoSize `json:"thumbnail"`
	Emoji        string     `json:"emoji"`
	SetName      string     `json:"set_name"`
}

// TextQuote 是用户手动选中的引文片段。
type TextQuote struct {
	Text string `json:"text"`
}

// ExternalReplyInfo 是「回复其它聊天里的消息」时 TG 给的原消息摘要。
// Chat 为消息来源（频道引用时就是那个频道）。
type ExternalReplyInfo struct {
	MsgPayload
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
	// Username 是公开频道/群的 @ 名，转发来源与故事来源要用它。
	Username string `json:"username"`
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
	// IsMember 只在 restricted 状态里出现：被限制但仍在群里。
	// 判断「bot 还在不在群里」时，restricted 必须看它 —— 只看 status
	// 会把「已被踢走但留下一条受限记录」当成还在群里。
	IsMember bool `json:"is_member"`
}

type ChatMemberResp struct {
	OK     bool `json:"ok"`
	Result struct {
		Status string `json:"status"`
		// CanSendMessages 只在 restricted 状态里出现，所以用指针区分
		// 「字段不存在」与「确实为 false」：普通成员的响应里没有它，
		// 当成 false 会把所有正常成员都看成被禁言。
		CanSendMessages *bool `json:"can_send_messages"`
		// UntilDate 是限时限制的到期时间（0 = 永久）。
		UntilDate int64 `json:"until_date"`
	} `json:"result"`
}

// ChatFullResp 是 getChat 的响应。广告号的强特征常常写在个人简介里
// （联系方式、价目、引流话术），而这个字段只有 getChat 给。
// 频道身份发言时取 Description（频道简介）；群的 LinkedChatID 是关联频道。
type ChatFullResp struct {
	OK     bool `json:"ok"`
	Result struct {
		Bio          string `json:"bio"`
		Description  string `json:"description"`
		LinkedChatID int64  `json:"linked_chat_id"`
		// 昵称与用户名：/check <uid> 这种没有消息可依托的场景要按资料判人，
		// 只能从 getChat 补（见 antiad.userInfo）。
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
		Username  string `json:"username"`
		Title     string `json:"title"`
	} `json:"result"`
}
