package tg

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// ---- Telegram 数据结构（只声明用得到的字段）----

type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
	// EditedMessage 是编辑过的消息。先发正常内容、判过后再编辑成广告
	// 是规避手法，必须重新送检；它同样要列进 allowed_updates 才收得到。
	EditedMessage *Message       `json:"edited_message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
	// ChatMember 只有在 allowed_updates 里显式声明才会推送。
	// 它是用户何时进群的唯一可靠来源——TG 没有反查接口。
	ChatMember *ChatMemberUpdated `json:"chat_member"`
	// MyChatMember 是 bot 自身的成员状态变化，用于发现
	// 被加进群但没给管理员权限这种静默失效的情况。
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
	// ForwardOrigin 判空标注这是转发来的，另取来源名进正文。
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

	// 引用/回复的三种形态。广告号会把正文压到极短、把载荷全放进引用块，
	// 不接这三个字段会难以识别这整类规避形态。
	//
	// ReplyToMessage —— 回复同一个群里的消息，TG 给出整条原消息。
	// ExternalReply  —— 回复其它聊天里的消息（频道引用属于这类），
	//                   TG 不给原消息本体，只给一份摘要。
	// Quote          —— 用户手动选中的那一段引文，是群里实际显示的内容。
	ReplyToMessage *Message           `json:"reply_to_message"`
	ExternalReply  *ExternalReplyInfo `json:"external_reply"`
	Quote          *TextQuote         `json:"quote"`

	// RichMessage 是富消息（Bot API 10.1）：正文按块组织，段落、列表、
	// 媒体、按钮都在块里，text 与 reply_markup 都是空的。
	//
	// 必须整类接住：这类消息在客户端照常渲染、也能转发，判定侧却拿不到
	// 一个字 —— 等于把一条有正文的消息当成空消息放行（渲染见 msgtext.go
	// 的 richLines）。
	RichMessage *RichMessage `json:"rich_message"`

	// 其余内容载荷。不声明这些字段时 encoding/json 会静默丢弃它们，消息
	// 就只剩转发来源这类元信息，在判定侧与「无正文」无法区分。
	Gift                *GiftInfo          `json:"gift"`
	GiftUpgradeSent     *GiftInfo          `json:"gift_upgrade_sent"`
	UniqueGift          *UniqueGiftInfo    `json:"unique_gift"`
	GiveawayCreated     *json.RawMessage   `json:"giveaway_created"`
	GiveawayCompleted   *GiveawayCompleted `json:"giveaway_completed"`
	UsersShared         *UsersShared       `json:"users_shared"`
	ChatShared          *ChatShared        `json:"chat_shared"`
	WebAppData          *WebAppData        `json:"web_app_data"`
	SuccessfulPayment   *SuccessfulPayment `json:"successful_payment"`
	RefundedPayment     *RefundedPayment   `json:"refunded_payment"`
	ProximityAlert      *ProximityAlert    `json:"proximity_alert_triggered"`
	ConnectedWebsite    string             `json:"connected_website"`
	PaidMessagePrice    *PaidMessagePrice  `json:"paid_message_price_changed"`
	PollOptionAdded     *PollOptionChange  `json:"poll_option_added"`
	PollOptionDeleted   *PollOptionChange  `json:"poll_option_deleted"`
	ChecklistTasksAdded *ChecklistTasks    `json:"checklist_tasks_added"`
	ChecklistTasksDone  *ChecklistTasks    `json:"checklist_tasks_done"`
	PassportData        *json.RawMessage   `json:"passport_data"`
	// PinnedMessage 是置顶事件自带的那条消息。判定不管置顶事件，但它的
	// 正文是有人在群里投放的内容，取一层正文进来。
	PinnedMessage *Message `json:"pinned_message"`
	// NewChatTitle 是群被改名后的标题。标题完全由管理员控制，被改成
	// 广告话术是既有形态，所以取进来判。
	NewChatTitle string `json:"new_chat_title"`
	MsgService
	// Raw 是 TG 推来的原始报文。载荷全不可识别时把它记进日志：新消息
	// 类型第一次出现时，需要的是原始字段名而不是猜测（见 antiad 的
	// noteUnknownPayload）。
	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON 解出消息并顺手留一份原始报文。
func (m *Message) UnmarshalJSON(b []byte) error {
	// 别名去方法集，否则 json 会回调本方法陷入递归。
	type plain Message
	if err := json.Unmarshal(b, (*plain)(m)); err != nil {
		return err
	}
	m.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// MsgService 是 service 消息的字段标记。它们只报事件（谁进谁出、群被改名、
// 通话开始、迁移到超级群……），本身不带可判定的载荷。
//
// 单列出来是为了让「载荷不可识别」的兜底能把它们摘出去：否则每一条改标题、
// 每一次通话开始都会被当成没解出来的新类型记进日志。
type MsgService struct {
	LeftChatMember               *json.RawMessage `json:"left_chat_member"`
	NewChatPhoto                 *json.RawMessage `json:"new_chat_photo"`
	DeleteChatPhoto              *json.RawMessage `json:"delete_chat_photo"`
	GroupChatCreated             *json.RawMessage `json:"group_chat_created"`
	SupergroupChatCreated        *json.RawMessage `json:"supergroup_chat_created"`
	ChannelChatCreated           *json.RawMessage `json:"channel_chat_created"`
	MigrateToChatID              *json.RawMessage `json:"migrate_to_chat_id"`
	MigrateFromChatID            *json.RawMessage `json:"migrate_from_chat_id"`
	AutoDeleteTimerChanged       *json.RawMessage `json:"message_auto_delete_timer_changed"`
	DirectMessagePriceChanged    *json.RawMessage `json:"direct_message_price_changed"`
	VideoChatStarted             *json.RawMessage `json:"video_chat_started"`
	VideoChatEnded               *json.RawMessage `json:"video_chat_ended"`
	VideoChatScheduled           *json.RawMessage `json:"video_chat_scheduled"`
	VideoChatParticipantsInvited *json.RawMessage `json:"video_chat_participants_invited"`
	ForumTopicCreated            *json.RawMessage `json:"forum_topic_created"`
	ForumTopicClosed             *json.RawMessage `json:"forum_topic_closed"`
	ForumTopicReopened           *json.RawMessage `json:"forum_topic_reopened"`
	ForumTopicEdited             *json.RawMessage `json:"forum_topic_edited"`
	GeneralForumTopicHidden      *json.RawMessage `json:"general_forum_topic_hidden"`
	GeneralForumTopicUnhidden    *json.RawMessage `json:"general_forum_topic_unhidden"`
	WriteAccessAllowed           *json.RawMessage `json:"write_access_allowed"`
	BoostAdded                   *json.RawMessage `json:"boost_added"`
	ChatBackgroundSet            *json.RawMessage `json:"chat_background_set"`
	ChatOwnerChanged             *json.RawMessage `json:"chat_owner_changed"`
	ChatOwnerLeft                *json.RawMessage `json:"chat_owner_left"`
	CommunityChatJoined          *json.RawMessage `json:"community_chat_joined"`
	ManagedBotCreated            *json.RawMessage `json:"managed_bot_created"`
	SuggestedPostApprovalFailed  *json.RawMessage `json:"suggested_post_approval_failed"`
	SuggestedPostApproved        *json.RawMessage `json:"suggested_post_approved"`
	SuggestedPostDeclined        *json.RawMessage `json:"suggested_post_declined"`
	SuggestedPostInfo            *json.RawMessage `json:"suggested_post_info"`
	SuggestedPostPaid            *json.RawMessage `json:"suggested_post_paid"`
	SuggestedPostRefunded        *json.RawMessage `json:"suggested_post_refunded"`
}

// ServiceSeen 报告这是不是一条 service 消息。
func (s MsgService) ServiceSeen() bool {
	for _, f := range []*json.RawMessage{
		s.LeftChatMember, s.NewChatPhoto, s.DeleteChatPhoto, s.GroupChatCreated,
		s.SupergroupChatCreated, s.ChannelChatCreated, s.MigrateToChatID,
		s.MigrateFromChatID, s.AutoDeleteTimerChanged, s.VideoChatStarted,
		s.VideoChatEnded, s.VideoChatScheduled, s.VideoChatParticipantsInvited,
		s.ForumTopicCreated, s.ForumTopicClosed, s.ForumTopicReopened,
		s.ForumTopicEdited, s.GeneralForumTopicHidden, s.GeneralForumTopicUnhidden,
		s.WriteAccessAllowed, s.BoostAdded, s.ChatBackgroundSet, s.ChatOwnerChanged,
		s.ChatOwnerLeft, s.CommunityChatJoined, s.ManagedBotCreated,
		s.SuggestedPostApprovalFailed, s.SuggestedPostApproved,
		s.SuggestedPostDeclined, s.SuggestedPostInfo, s.SuggestedPostPaid,
		s.SuggestedPostRefunded, s.DirectMessagePriceChanged,
	} {
		if f != nil {
			return true
		}
	}
	return false
}

// MessageEntity 是正文里的格式标注。text_link 的 URL 不在正文里，
// 是链接文字背后藏着的链接，只有从这里才拿得到。
type MessageEntity struct {
	Type string `json:"type"` // url / text_link / mention / ...
	URL  string `json:"url"`
}

// FileNamed 是只取文件名的媒体。文件名是攻击者可控的文字，
// 常被用于投放广告。
type FileNamed struct {
	FileName string `json:"file_name"`
}

// MsgPayload 是 text/caption 之外携带文字的消息类型。Message 与
// ExternalReplyInfo 共用：频道引用同样可能是一张联系人卡片或一个投票。
// 纯媒体（无配文的图片、贴纸、语音、定位、骰子）没有文字，不在此列。
type MsgPayload struct {
	// Contact 是分享联系人卡片。名字与号码本身就是载荷，
	// 是典型的引流形态。
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

	// 下面这些同样出现在 ExternalReplyInfo 里（引用其它聊天的消息时 TG
	// 只给这层摘要），所以与上面一样放在共用载荷里。
	//
	// 它们都没有 text/caption 之外的形态可用：付费媒体的文字在配文里、
	// 实况照片的静态图在 photo 里、抽奖的话术在附加奖品说明里 —— 不解析
	// 这些字段，这几类消息在判定侧就是空的。
	PaidMedia       *PaidMediaInfo   `json:"paid_media"`
	LivePhoto       *LivePhoto       `json:"live_photo"`
	VideoNote       *MediaMeta       `json:"video_note"`
	Voice           *MediaMeta       `json:"voice"`
	Location        *Location        `json:"location"`
	Dice            *Dice            `json:"dice"`
	Giveaway        *Giveaway        `json:"giveaway"`
	GiveawayWinners *GiveawayWinners `json:"giveaway_winners"`
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

// ExternalReplyInfo 是回复其它聊天里的消息时 TG 给的原消息摘要。
// Chat 为消息来源（频道引用时就是那个频道）。
type ExternalReplyInfo struct {
	MsgPayload
	Chat    *Chat  `json:"chat"`
	Text    string `json:"text"`
	Caption string `json:"caption"`
}

// ---- 富消息（Bot API 10.1）----

// RichMessage 是富消息的正文：一串块。
type RichMessage struct {
	Blocks []RichBlock `json:"blocks"`
}

// RichBlock 是富消息里的一个块。块类型有二十多种且会继续增加，所以用一个
// 共用结构承接：按 Type 取用到的字段，其余留空。未知 Type 不丢，由 richLines
// 以类型名留痕 —— 新块类型出现时先看得见，再决定怎么渲染。
type RichBlock struct {
	Type string `json:"type"`
	// 文本类块：paragraph / heading / pre / footer / blockquote /
	// expandable_blockquote / pullquote / thinking / mathematical_expression
	Text       *RichText `json:"text"`
	Expression string    `json:"expression"`
	Language   string    `json:"language"`
	// 容器类块：collage / slideshow / details / 引用块 / 列表项
	Blocks  []RichBlock       `json:"blocks"`
	Items   []RichBlockItem   `json:"items"`
	Cells   [][]RichBlockCell `json:"cells"`
	Summary *RichText         `json:"summary"`
	Caption *RichBlockCaption `json:"caption"`
	Credit  *RichText         `json:"credit"`
	// buttons 块。富消息的按钮属于内容，转发不会像 reply_markup 那样被
	// 丢掉，引流链接常常只挂在这里。
	Buttons []RichMessageButton `json:"buttons"`
	// 媒体块。只取文件名一类的文字，媒体本身不下载。
	Photo     []PhotoSize `json:"photo"`
	Animation *FileNamed  `json:"animation"`
	Audio     *FileNamed  `json:"audio"`
	Document  *FileNamed  `json:"document"`
	Video     *FileNamed  `json:"video"`
	VoiceNote *FileNamed  `json:"voice_note"`
	Location  *Location   `json:"location"`
}

// RichBlockItem 是列表项：标签加子块。
type RichBlockItem struct {
	Label  string      `json:"label"`
	Blocks []RichBlock `json:"blocks"`
}

// RichBlockCell 是表格单元格。
type RichBlockCell struct {
	Text *RichText `json:"text"`
}

// RichBlockCaption 是媒体块的配文与署名。
type RichBlockCaption struct {
	Text   *RichText `json:"text"`
	Credit *RichText `json:"credit"`
}

// RichMessageButton 是富消息自带的按钮。按钮的值与显示文字同等重要：
// 正文只有一句无害的话、链接只挂在按钮上，是这类广告的常见形态。
type RichMessageButton struct {
	Text         *RichText `json:"text"`
	URL          string    `json:"url"`
	CallbackData string    `json:"callback_data"`
	// CopyText 是「复制文本」按钮要复制的内容；WebApp 是 Web App 按钮，
	// 链接在它的 url 里。
	CopyText *struct {
		Text string `json:"text"`
	} `json:"copy_text"`
	WebApp *struct {
		URL string `json:"url"`
	} `json:"web_app"`
	SwitchInlineQuery            string `json:"switch_inline_query"`
	SwitchInlineQueryCurrentChat string `json:"switch_inline_query_current_chat"`
}

// RichText 是富消息里的富文本节点。同一个位置可能是纯字符串、节点数组，
// 或带类型与格式的对象；对象的 text 字段又能是前三种里的任意一种，所以
// 自己解这三种形状。
//
// 取值取全：文字本身与它挂着的链接、@用户名、电话、邮箱、卡号都要落进
// 正文 —— 引流载荷正是藏在这些格式化层里。
type RichText struct {
	Parts []string `json:"-"`
}

// UnmarshalJSON 把富文本摊平成一串片段：字符串与数组按顺序展开，对象按
// 「文字在前、载体在后」展开。
func (t *RichText) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	switch b[0] {
	case '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		t.add(s)
	case '[':
		var nodes []RichText
		if err := json.Unmarshal(b, &nodes); err != nil {
			return err
		}
		for _, n := range nodes {
			t.Parts = append(t.Parts, n.Parts...)
		}
	case '{':
		var node struct {
			Text            *RichText          `json:"text"`
			URL             string             `json:"url"`
			Username        string             `json:"username"`
			PhoneNumber     string             `json:"phone_number"`
			EmailAddress    string             `json:"email_address"`
			BankCardNumber  string             `json:"bank_card_number"`
			Hashtag         string             `json:"hashtag"`
			Cashtag         string             `json:"cashtag"`
			BotCommand      string             `json:"bot_command"`
			AnchorName      string             `json:"anchor_name"`
			AlternativeText string             `json:"alternative_text"`
			Button          *RichMessageButton `json:"button"`
		}
		if err := json.Unmarshal(b, &node); err != nil {
			return err
		}
		if node.Text != nil {
			t.Parts = append(t.Parts, node.Text.Parts...)
		}
		for _, s := range []string{node.URL, node.Username, node.PhoneNumber,
			node.EmailAddress, node.BankCardNumber, node.Hashtag, node.Cashtag,
			node.BotCommand, node.AnchorName, node.AlternativeText} {
			t.add(s)
		}
		if node.Button != nil {
			for _, s := range node.Button.Parts() {
				t.add(s)
			}
		}
	}
	return nil
}

// add 追加一个非空片段。载体（链接、@名、卡号）常常与文字重复，重复的
// 只留一份 —— 判定正文要的是可读，不是复读。
func (t *RichText) add(s string) {
	if s = strings.TrimSpace(s); s == "" {
		return
	}
	for _, p := range t.Parts {
		if p == s {
			return
		}
	}
	t.Parts = append(t.Parts, s)
}

// Line 把摊平的片段拼成一行，空节点返回空串。
func (t *RichText) Line() string {
	if t == nil {
		return ""
	}
	return strings.Join(t.Parts, " ")
}

// Parts 展开按钮的全部文字与载荷值。
func (b RichMessageButton) Parts() []string {
	var out []string
	if b.Text != nil {
		out = append(out, b.Text.Line())
	}
	out = append(out, b.URL, b.CallbackData, b.SwitchInlineQuery,
		b.SwitchInlineQueryCurrentChat)
	if b.CopyText != nil {
		out = append(out, b.CopyText.Text)
	}
	if b.WebApp != nil {
		out = append(out, b.WebApp.URL)
	}
	return out
}

// ---- 非文字载荷的取值结构 ----

// PaidMediaInfo 是付费媒体：价格写在星数里，媒体本体要付费才给看，
// 正文与图片字段都是空的。
type PaidMediaInfo struct {
	StarCount int             `json:"star_count"`
	PaidMedia []PaidMediaItem `json:"paid_media"`
}

// PaidMediaItem 是付费媒体里的一项。类型与配文是这里唯一能读到的文字。
type PaidMediaItem struct {
	Type    string      `json:"type"`
	Photo   []PhotoSize `json:"photo"`
	Video   *FileNamed  `json:"video"`
	Caption string      `json:"caption"`
}

// LivePhoto 是实况照片：静态图在 photo 里，与普通图片同样值得识图。
type LivePhoto struct {
	Photo  []PhotoSize `json:"photo"`
	Width  int         `json:"width"`
	Height int         `json:"height"`
}

// MediaMeta 是只有时长、尺寸这类元数据的媒体（语音、视频消息）。
// 它们没有文件名，能进正文的只有类型与时长。
type MediaMeta struct {
	Duration int `json:"duration"`
	Length   int `json:"length"`
}

// Location 是位置。经纬度本身不是文字，但坐标是载荷的一部分。
type Location struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// Dice 是骰子一类的随机表情，emoji 决定形态、value 是点数。
type Dice struct {
	Emoji string `json:"emoji"`
	Value int    `json:"value"`
}

// Giveaway 是抽奖：附加奖品说明是这里唯一由发起人写的文字。
type Giveaway struct {
	Chats            []*Chat `json:"chats"`
	PrizeDescription string  `json:"prize_description"`
	WinnerCount      int     `json:"winner_count"`
	PrizeStarCount   int     `json:"prize_star_count"`
}

// GiveawayWinners 是抽奖结果。
type GiveawayWinners struct {
	Chat             *Chat  `json:"chat"`
	PrizeDescription string `json:"prize_description"`
	WinnerCount      int    `json:"winner_count"`
}

// GiveawayCompleted 是抽奖结束事件。
type GiveawayCompleted struct {
	WinnerCount         int `json:"winner_count"`
	UnclaimedPrizeCount int `json:"unclaimed_prize_count"`
}

// GiftInfo 是礼物：礼物名与随礼物一起发的文字都是载荷。
type GiftInfo struct {
	Gift             *Gift  `json:"gift"`
	Text             string `json:"text"`
	UniqueGiftNumber int    `json:"unique_gift_number"`
}

// Gift 是礼物的静态信息。
type Gift struct {
	ID         string `json:"id"`
	StarCount  int    `json:"star_count"`
	TotalCount int    `json:"total_count"`
}

// UniqueGiftInfo 是独一无二的礼物（NFT 类）：名字完全由发送方决定。
type UniqueGiftInfo struct {
	Gift   *UniqueGift `json:"gift"`
	Text   string      `json:"text"`
	Origin string      `json:"origin"`
}

// UniqueGift 是独一无二礼物的名称。
type UniqueGift struct {
	GiftID   string `json:"gift_id"`
	BaseName string `json:"base_name"`
	Name     string `json:"name"`
	Number   int    `json:"number"`
}

// UsersShared 是分享用户：被分享者的名字与 @名。
type UsersShared struct {
	Users []struct {
		UserID    int64  `json:"user_id"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
		Username  string `json:"username"`
	} `json:"users"`
}

// ChatShared 是分享聊天：标题与 @名。
type ChatShared struct {
	ChatID   int64  `json:"chat_id"`
	Title    string `json:"title"`
	Username string `json:"username"`
}

// WebAppData 是 Web App 回传的数据。两个字段都由客户端决定内容，
// 可以放任意文字，是现成的载荷通道。
type WebAppData struct {
	Data       string `json:"data"`
	ButtonText string `json:"button_text"`
}

// SuccessfulPayment 是支付成功。发票载荷与订单信息由发送方填写。
type SuccessfulPayment struct {
	Currency       string `json:"currency"`
	TotalAmount    int    `json:"total_amount"`
	InvoicePayload string `json:"invoice_payload"`
	OrderInfo      *struct {
		Name            string `json:"name"`
		PhoneNumber     string `json:"phone_number"`
		Email           string `json:"email"`
		ShippingAddress *struct {
			City    string `json:"city"`
			Address string `json:"address"`
		} `json:"shipping_address"`
	} `json:"order_info"`
}

// RefundedPayment 是退款。
type RefundedPayment struct {
	Currency       string `json:"currency"`
	TotalAmount    int    `json:"total_amount"`
	InvoicePayload string `json:"invoice_payload"`
}

// ProximityAlert 是「某人靠近某人」的提醒。
type ProximityAlert struct {
	Traveler *TGUser `json:"traveler"`
	Watcher  *TGUser `json:"watcher"`
	Distance int     `json:"distance"`
}

// PaidMessagePrice 是付费消息的价格变更。
type PaidMessagePrice struct {
	StarCount int `json:"paid_message_star_count"`
}

// PollOptionChange 是投票选项的新增与删除。
type PollOptionChange struct {
	Option *struct {
		Text string `json:"text"`
	} `json:"option"`
}

// ChecklistTasks 是清单任务的新增与完成。
type ChecklistTasks struct {
	Tasks []struct {
		Text string `json:"text"`
	} `json:"tasks"`
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
	// 判断 bot 是否仍在群里时，restricted 必须看它 —— 只看 status
	// 会把已被踢走但留下一条受限记录当成还在群里。
	IsMember bool `json:"is_member"`
	// CanSendMessages 也只在 restricted 里出现，用指针区分没有这个字段
	// 与确实为 false（见 antiad.canSpeak）：把受限状态当成被禁言，
	// 会对着能发言的人反复禁言。
	CanSendMessages *bool `json:"can_send_messages"`
}

type ChatMemberResp struct {
	OK     bool `json:"ok"`
	Result struct {
		Status string `json:"status"`
		// IsMember 只在 restricted 状态里出现：被限制但仍在群里。
		// 只看 status 会把已被踢走但留下一条受限记录当成还在群里，
		// 禁言失败后的二次判断会因此漏掉真正该跳过的场景。
		IsMember *bool `json:"is_member"`
		// CanSendMessages 只在 restricted 状态里出现，所以用指针区分
		// 字段不存在与确实为 false：普通成员的响应里没有它，
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

// ---- 载荷取值的取值方法 ----
//
// 判定的正文按「前缀 + 字段」拼行，取值统一放在这里：结构体与它的取值
// 方式在一个文件里，新增字段时不会只补解码、漏了渲染。

// Label 取礼物名与星数。
func (g *GiftInfo) Label() string {
	if g == nil || g.Gift == nil {
		return ""
	}
	return joinFields(g.Gift.ID, starText(g.Gift.StarCount))
}

// Label 取独一无二礼物的名称，名字完全由发送方决定。
func (g *UniqueGiftInfo) Label() string {
	if g == nil || g.Gift == nil {
		return ""
	}
	// name 与 base_name 通常相同，取一个有值的即可。
	name := g.Gift.Name
	if name == "" {
		name = g.Gift.BaseName
	}
	return joinFields(name, g.Gift.GiftID)
}

// Order 取订单信息里的文字：收件人、电话、邮箱、地址都由付款方填写。
func (p *SuccessfulPayment) Order() string {
	if p == nil || p.OrderInfo == nil {
		return ""
	}
	o := p.OrderInfo
	fields := []string{o.Name, o.PhoneNumber, o.Email}
	if o.ShippingAddress != nil {
		fields = append(fields, o.ShippingAddress.City, o.ShippingAddress.Address)
	}
	return joinFields(fields...)
}

// Text 取投票选项的文字。
func (p *PollOptionChange) Text() string {
	if p == nil || p.Option == nil {
		return ""
	}
	return p.Option.Text
}

// Texts 取清单任务的文字。
func (c *ChecklistTasks) Texts() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.Tasks))
	for _, t := range c.Tasks {
		out = append(out, t.Text)
	}
	return out
}

// starText 把星数写成可读的金额；0 星当没有处理。
func starText(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n) + " 星"
}

// joinFields 用空格连接非空字段，全空时返回空串。
func joinFields(fields ...string) string {
	var out []string
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return strings.Join(out, " ")
}
