package antiad

import (
	"fmt"
	"html"
	"log/slog"
	"strings"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// ---- 硬规则：冒用国家领导人（姓名） ----
//
// 判定模型把角色扮演当正常内容，但拿国家领导人的名义当门面本身就是风险。
// 这一条不走模型：只看账号资料（昵称、用户名、简介）里有没有领导人姓名，
// 命中直接封禁出群。
//
// 正文里提到姓名不算（管理员口径：只查资料，提到不管）：群里讨论新闻、
// 引用讲话是正常行为。要看正文就改 leaderGate / leaderGateWorker。
//
// 零额外开销：昵称与用户名随消息一起来；简介是判定本来就要取的
// （getChat，带一小时缓存）。不查头像、不调识图、不多发一次 TG 请求。

// leaderNames 是裸名即命中的一批：姓名本身罕见，出现就直指领导人。
// 表里一律写成归一化形态（无空白、小写）。
var leaderNames = []string{
	// 现任
	"习近平", "習近平", "习大大", "习主席", "习总书记", "xijinping",
	"王沪宁", "王滬寧", "赵乐际", "趙樂際", "蔡奇", "丁薛祥",
	// 历任最高领导人
	"胡锦涛", "胡錦濤", "江泽民", "江澤民", "邓小平", "鄧小平",
	"毛泽东", "毛澤東", "华国锋", "華國鋒", "胡耀邦", "赵紫阳", "趙紫陽",
	// 其他常被冒用的国家领导人及家属
	"温家宝", "溫家寶", "李克强", "李克強", "朱镕基", "朱鎔基",
	"李鹏", "李鵬", "乔石", "喬石", "刘少奇", "劉少奇", "周恩来", "周恩來",
	"习仲勋", "習仲勳", "彭丽媛", "彭麗媛",
}

// leaderTitled 是常见姓名：只有跟职务连用才算（李强可能是普通人，
// 李强总理不是）。比裸名匹配更严，但足以挡住冒用。
var leaderTitled = []string{
	"李强总理", "李强主席", "李强副总理", "李强书记",
	"李希书记", "李希纪委",
	"丁薛祥副总理", "蔡奇书记", "王沪宁主席", "赵乐际委员长",
}

// leaderNorm 把文本归一化：去掉空白、常见分割与装饰符号，统一小写，
// 使插入了分隔符或大小写混合的姓名也能命中。
func leaderNorm(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		switch r {
		case ' ', '\t', '\n', '\r', '\u3000', '\u00a0':
			continue
		}
		switch r {
		case '·', '•', '∙', '.', '-', '_', '|', '/', '\\', '、', '，', ',',
			'：', ':', '；', ';', '（', '(', '）', ')', '《', '》', '【', '】',
			'[', ']', '"', '\'', '“', '”', '‘', '’', '#', '@', '*', '^', '~', '+', '=':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// leaderHit 报告文本里出现了哪个领导人称呼（空串 = 没命中）。
func leaderHit(text string) string {
	norm := leaderNorm(text)
	if norm == "" {
		return ""
	}
	for _, n := range leaderNames {
		if strings.Contains(norm, n) {
			return n
		}
	}
	for _, n := range leaderTitled {
		if strings.Contains(norm, n) {
			return n
		}
	}
	return ""
}

// leaderProfileFields 检查账号资料的三个字段。资料里带着领导人姓名基本
// 就是冒用（真名用户不会把国家领导人的名字当昵称）。简介只在调用方本来就
// 要取它的时候传进来（判定 worker 与冷判定），不额外发请求。
func leaderProfileFields(first, last, username, bio string) (hit, where string) {
	if h := leaderHit(first + last); h != "" {
		return h, "昵称"
	}
	if h := leaderHit(username); h != "" {
		return h, "用户名"
	}
	if h := leaderHit(bio); h != "" {
		return h, "简介"
	}
	return "", ""
}

// leaderProfileHit 是 leaderProfileFields 的 tg 版。
func leaderProfileHit(u *tg.TGUser, bio string) (string, string) {
	if u == nil {
		return "", ""
	}
	return leaderProfileFields(u.FirstName, u.LastName, u.Username, bio)
}

// leaderVerdict 把命中包装成一条判定结论，供落流水与告警复用。
func leaderVerdict(hit, where string) adVerdict {
	return adVerdict{
		IsAd: true, Confidence: 1, Kind: "impersonate", Scope: "account",
		Severity: 3, Decider: "rule",
		Reason: fmt.Sprintf("冒用国家领导人：%s 出现在%s", hit, where),
	}
}

// leaderBan 直接封禁出群：删掉这条消息、banChatMember、落流水并告警。
// 演练群只落流水不动手（与其余处置同一口径）。
func leaderBan(b *core.Bot, conf store.BotChat, m *tg.Message, hit, where string) {
	v := leaderVerdict(hit, where)
	// /check <uid> 这类没有具体消息时不能删（message_id 为 0）。
	act := adAction{Delete: m.MessageID != 0, Ban: true, Alert: true, Name: "deleted_banned"}
	if m.MessageID == 0 {
		act.Name = "banned"
	}
	note := ApplyAction(b, m, act, conf.Dryrun)
	logID := logAd(b, m, v, logAction(act, conf.Dryrun), logNote(act, note, conf.Dryrun))
	if !conf.Dryrun {
		BumpAdHits(b, m.Chat.ID, m.From.ID, 1)
	}
	if act.Alert {
		sendAdAlert(b, conf, m, v, act, note, logID, conf.Dryrun)
	}
	slog.Info("反广告：冒用国家领导人，已封禁出群",
		"chat", m.Chat.ID, "uid", m.From.ID, "命中", hit, "位置", where)
}

// leaderGate 是消息路径上的前置判断：资料（昵称/用户名）里出现领导人姓名
// 就直接封禁，不送检、不花钱。返回 true 表示已处理完毕。
//
// 只看资料，不看正文（管理员口径：提到不管）。bio 为空时也只查昵称与
// 用户名 —— 简介在判定 worker 里才取，那条路上还有一次 leaderGateWorker 的复查。
func leaderGate(b *core.Bot, conf store.BotChat, m *tg.Message, bio string) bool {
	if m == nil || m.From == nil || m.From.ID == 0 {
		return false
	}
	if hit, where := leaderProfileHit(m.From, bio); hit != "" {
		leaderBan(b, conf, m, hit, where)
		return true
	}
	return false
}

// leaderGateWorker 是判定 worker 里的第二次前置判断：这里刚好拿到简介
// （判定本来就要取，没有额外开销），一并把简介过一遍规则。正文不看。
//
// 返回命中内容与位置（空串 = 未命中）；命中时封禁已在函数内执行完毕，
// 调用方只需收尾展示（/check 的多步响应要用它写清命中了什么）。
func leaderGateWorker(b *core.Bot, snap *store.Snapshot, conf store.BotChat,
	m *tg.Message, p senderProfile) (hit, where string) {

	// 用画像而不是消息里的 From：/check <uid> 的昵称与用户名是从
	// getChat 补进画像的（见 enrichSender）。
	if hit, where := leaderProfileFields(p.FirstName, p.LastName, p.Username, p.Bio); hit != "" {
		leaderBan(b, conf, m, hit, where)
		return hit, where
	}
	return "", ""
}

// leaderKindLabel 是这条硬规则的分类中文名（adKindLabels 与 Mini App 共用一份口径）。
const leaderKindLabel = "冒用领导人"

// leaderNoticeText 用于冷判定路径（没有具体消息可删）：把资料命中的情况
// 写成一条流水正文，管理员在记录与查看页上能对上。
func leaderNoticeText(u *tg.TGUser, bio, hit, where string) string {
	var sb strings.Builder
	sb.WriteString("［冒用国家领导人］\n")
	if u != nil {
		fmt.Fprintf(&sb, "昵称/用户名: %s (@%s)\n",
			html.EscapeString(strings.TrimSpace(u.FirstName+" "+u.LastName)),
			html.EscapeString(u.Username))
	}
	if strings.TrimSpace(bio) != "" {
		sb.WriteString("简介: " + html.EscapeString(core.TruncateRunes(bio, 300)) + "\n")
	}
	fmt.Fprintf(&sb, "命中: %s（%s）\n类型: impersonate", hit, where)
	return sb.String()
}
