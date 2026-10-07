package antiad

import (
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// RestrictionStatusText 渲染某人当前生效的限制，供 /check 的结果附注：
// 写清限制类型、判定来源与所在群；联合封禁额外给出前往对应 bot 解除的
// 链接 —— 全局组指向主 bot，专属组指向条目归属人的 bot（链接打开的是
// 那个 bot 里这个人的资料卡，卡片上有解除联合封禁入口）。
//
// 返回的按钮行已包好 URLBtn，调用方用 tg.KBAppend 追加即可。
// 文本为空表示当前没有任何生效中的限制。
func RestrictionStatusText(sh *core.Shared, botID, uid int64) (string, [][2]string) {
	pens := ActivePenalties(sh, botID, uid)
	if len(pens) == 0 {
		return "", nil
	}
	snap := sh.Cache.Snap()
	loc := snap.Location()
	var sb strings.Builder
	var links [][2]string
	sb.WriteString("⚠️ <b>当前生效的限制</b>\n")
	for _, p := range pens {
		when := time.Unix(p.At, 0).In(loc).Format("01-02 15:04")
		reason := html.EscapeString(core.TruncateRunes(strings.TrimSpace(p.Reason), 120))
		switch p.Type {
		case "join_profile":
			fmt.Fprintf(&sb, "• 进群限制（按个人简介判）｜ %s ｜ 群 %s\n  <i>%s</i>\n",
				when, chatRef(snap, p.ChatID), reason)
		case "prewarm":
			fmt.Fprintf(&sb, "• 前置号识别（批量注册特征）｜ %s ｜ 群 %s\n  <i>%s</i>\n",
				when, chatRef(snap, p.ChatID), reason)
		case "message":
			fmt.Fprintf(&sb, "• %s ｜ %s ｜ 群 %s\n  <i>%s</i>\n",
				messagePenaltyName(p.Action), when, chatRef(snap, p.ChatID), reason)
		case "gban":
			fmt.Fprintf(&sb, "• <b>全局联合封禁</b>｜ %s\n  <i>%s</i>\n", when, reason)
			if u := mainBotUsername(snap); u != "" {
				links = append(links, [2]string{"🔓 前往主 bot 解除",
					tg.URLBtn(userDeepLink(u, uid))})
			}
		case "gban_own":
			fmt.Fprintf(&sb, "• <b>专属联合封禁</b>（管理员 %d 的组）｜ %s\n  <i>%s</i>\n",
				p.Owner, when, reason)
			if u := ownerBotUsername(snap, p.Owner, botID); u != "" {
				links = append(links, [2]string{"🔓 前往 @" + u + " 解除",
					tg.URLBtn(userDeepLink(u, uid))})
			}
		}
	}
	return strings.TrimRight(sb.String(), "\n"), links
}

// userDeepLink 打开某人的资料卡（卡片上有解除联合封禁按钮）。
func userDeepLink(botUsername string, uid int64) string {
	return "https://t.me/" + botUsername + "?start=" + UserPayload(uid)
}

// messagePenaltyName 把消息级处罚的动作翻成禁言或封禁出群。
func messagePenaltyName(action string) string {
	a := strings.TrimPrefix(action, "dryrun:")
	if strings.Contains(a, "banned") {
		return "封禁出群"
	}
	return "禁言"
}

// chatRef 渲染群引用：全平台找得到标题就带上，否则只有 id。
func chatRef(snap *store.Snapshot, chatID int64) string {
	for _, rec := range snap.Bots {
		if c, ok := snap.ChatConf(rec.BotID, chatID); ok && c.Title != "" {
			return strconv.FormatInt(chatID, 10) + "（" + html.EscapeString(c.Title) + "）"
		}
	}
	return strconv.FormatInt(chatID, 10)
}

// mainBotUsername 取主 bot 的用户名；没配主 bot（或还没拿到用户名）时
// 返回空，调用方就不给链接。
func mainBotUsername(snap *store.Snapshot) string {
	best := botWithUsername(snap, func(r *store.BotRec) bool { return r.IsMain })
	return best
}

// ownerBotUsername 取对应的机器人用户名：优先当前群所在的这个 bot
// （专属组条目本就属于它的归属人），否则取该归属人名下号最小的一个。
func ownerBotUsername(snap *store.Snapshot, ownerID, preferBotID int64) string {
	if rec := snap.Bots[preferBotID]; rec != nil && rec.OwnerID == ownerID &&
		rec.Username != "" {
		return rec.Username
	}
	return botWithUsername(snap, func(r *store.BotRec) bool { return r.OwnerID == ownerID })
}

// botWithUsername 返回满足条件的 bot 里 bot_id 最小的那个的用户名
// （map 遍历无序，取最小保证同样的状态渲染出同样的链接）。
func botWithUsername(snap *store.Snapshot, ok func(*store.BotRec) bool) string {
	var best *store.BotRec
	for _, rec := range snap.Bots {
		if !ok(rec) || rec.Username == "" {
			continue
		}
		if best == nil || rec.BotID < best.BotID {
			best = rec
		}
	}
	if best == nil {
		return ""
	}
	return best.Username
}
