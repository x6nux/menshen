package antiad

import (
	"fmt"
	"html"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// ---- 清理判定上下文（/clear）----
//
// 群内 /clear 只管发命令的那个群：群被广告刷过一轮之后，那批留底已经失去
// 参考价值，继续当证据只会让后续判定沿着旧语境走。
//
// 私聊 /clear 是同一动作放大到多个群：主/次管理员先看一份可清理的群清单，
// 点按钮挑目标群或全部群。范围与 /ban、/uban 对称 —— 主管理员 = 全部 bot
// 的群，次管 = 自己名下 bot 的群。

const clearUsage = "用法：群内直接发 <code>/clear</code>。\n" +
	"把本群全部成员的判定史截到此刻：此前的留底不再进后续判定" +
	"（最近上下文与 /check 复查），历史命中数归零。\n" +
	"不解除任何限制，也不删留底与流水 —— 需要时仍可在面板上复查。"

const clearDMCmdUsage = "用法：<code>/clear</code> 列出你能清理的群，" +
	"点按钮选目标群或全部群；也可直接发 <code>/clear &lt;chat_id&gt;</code> " +
	"或 <code>/clear all</code>。\n" +
	"清理 = 把目标群全部成员的判定史截到此刻：此前的留底不再进最近上下文与 " +
	"<code>/check</code> 复查，历史命中数归零；不解除任何限制，也不删留底与流水。\n" +
	"范围与 <code>/ban</code>、<code>/uban</code> 相同：主管理员=全部群，" +
	"次管=自己名下 bot 的群。"

// HandleClearCommand 处理群内 /clear。
//
// 权限与 /uad、/ungban 同一道门：它改变的是全群成员的判定输入，
// 只有群管理员及以上能用，非授权者静默忽略。
func HandleClearCommand(b *core.Bot, conf store.BotChat, m *tg.Message) {
	if !canMarkAd(b, conf.ChatID, m.From.ID) {
		return
	}
	// 命令本身不该留在群里（与 /ban、/uad 一致）。删不掉也不影响后续。
	b.TG.Call("deleteMessage", map[string]any{
		"chat_id": conf.ChatID, "message_id": m.MessageID,
	})

	snap := b.Cache.Snap()
	ttl := time.Duration(snap.BotSettingInt(b.BotID(), "antiad_alert_ttl", 300)) * time.Second
	reply := func(text string) { groupNotice(b, conf.ChatID, text, nil, ttl) }

	if !b.AdLimits.Allow(fmt.Sprintf("clear:%d", m.From.ID),
		snap.BotSettingInt(b.BotID(), "antiad_cmd_rpm", 3)) {
		reply("操作太频繁，请稍后再试。")
		return
	}

	n, err := ResetChatHistory(b, conf.ChatID)
	if err != nil {
		reply("⚠️ 清理失败：" + htmlEscapeShort(err.Error()))
		return
	}
	reply(fmt.Sprintf("✅ 已清理本群 %d 名成员的判定上下文："+
		"此前留底不再进入后续判定，历史命中数归零。\n"+
		"留底与流水保留，需要时可在面板复查。", n))
	slog.Info("反广告：管理员清理本群判定上下文", "chat", conf.ChatID,
		"by", m.From.ID, "成员数", n)
}

// ---- 私聊清理：挑目标群 ----

// clearCbPrefix 是私聊清理按钮的回调前缀，目标群 0 表示这批全部。
const clearCbPrefix = "a:cl:"

// clearMaxButtons 是清单里最多列出的群数。按钮可选群数远小于 Telegram 的
// 上限，但清单本身就是给人看的：群太多时点选不如直接发 /clear <chat_id>。
const clearMaxButtons = 24

// clearTarget 是一个可清理的目标群。
type clearTarget struct {
	ChatID int64
	Name   string
}

// clearTargets 列出 actor 能清理的群，按群名排序（同名按 chat_id）。
//
// 走的是判定链路的同一份配置：只有启用中的群才在判定，也才谈得上「清理
// 上下文」。范围与 /ban、/uban 对称（见 eachActiveChat 的 include）。
func clearTargets(sh *core.Shared, actor int64) []clearTarget {
	snap := sh.Cache.Snap()
	main := sh.IsMain(actor)
	mine := func(botID int64) bool {
		rec := snap.Bots[botID]
		return rec != nil && !rec.IsMain && (main || rec.OwnerID == actor)
	}
	var out []clearTarget
	eachActiveChat(sh, mine, func(bots []*core.Bot, chatID int64) {
		conf, _ := snap.ChatConf(bots[0].BotID(), chatID)
		out = append(out, clearTarget{ChatID: chatID, Name: chatDisplayName(conf)})
	})
	slices.SortFunc(out, func(x, y clearTarget) int {
		if x.Name != y.Name {
			return strings.Compare(x.Name, y.Name)
		}
		return int(x.ChatID - y.ChatID)
	})
	return out
}

// findClearTarget 在清单里按 chat_id 找目标。
func findClearTarget(targets []clearTarget, chatID int64) (clearTarget, bool) {
	for _, t := range targets {
		if t.ChatID == chatID {
			return t, true
		}
	}
	return clearTarget{}, false
}

// clearCallbackData 拼私聊清理按钮的回调数据。
func clearCallbackData(chatID int64) string {
	return clearCbPrefix + strconv.FormatInt(chatID, 10)
}

// parseClearCallback 还原回调里的目标群；0 表示全部群。
func parseClearCallback(data string) (int64, bool) {
	rest, ok := strings.CutPrefix(data, clearCbPrefix)
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// HandleClearCommandDM 处理管理员私聊里的 /clear。调用方已确认是服务管理员。
func HandleClearCommandDM(b *core.Bot, m *tg.Message, arg string) {
	actor := m.From.ID
	if !b.IsStaff(actor) {
		return
	}
	dm := m.Chat.ID
	if !b.AdLimits.Allow(fmt.Sprintf("clear:%d", actor),
		b.Cache.Snap().BotSettingInt(b.BotID(), "antiad_cmd_rpm", 3)) {
		b.Send(dm, "操作太频繁，请稍后再试。", nil)
		return
	}

	targets := clearTargets(b.Shared, actor)
	if len(targets) == 0 {
		b.Send(dm, "没有你能清理的群。", nil)
		return
	}

	switch arg = strings.TrimSpace(arg); {
	case arg == "":
		b.Send(dm, clearPickText(targets), clearPickKB(targets))
	case arg == "all" || arg == "全部" || arg == "全部群":
		members, chats := clearMany(b, targets)
		b.Send(dm, clearResultText("", true, targets, members, chats), nil)
	default:
		chatID, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			b.Send(dm, clearDMCmdUsage, nil)
			return
		}
		t, ok := findClearTarget(targets, chatID)
		if !ok {
			b.Send(dm, "这个群不在你能管的范围内。发 <code>/clear</code> 看可选的群。", nil)
			return
		}
		members, chats := clearMany(b, []clearTarget{t})
		b.Send(dm, clearResultText(t.Name, false, []clearTarget{t}, members, chats), nil)
	}
}

// HandleClearCallback 处理私聊清理清单上的按钮。
func HandleClearCallback(b *core.Bot, q *tg.CallbackQuery) {
	actor := q.From.ID
	if !b.IsStaff(actor) {
		b.AnswerCallback(q.ID, "")
		return
	}
	chatID, ok := parseClearCallback(q.Data)
	if !ok {
		b.AnswerCallback(q.ID, "")
		return
	}
	if !b.AdLimits.Allow(fmt.Sprintf("clear:%d", actor),
		b.Cache.Snap().BotSettingInt(b.BotID(), "antiad_cmd_rpm", 3)) {
		b.AnswerCallback(q.ID, "操作太频繁，请稍后再试")
		return
	}

	// 清单是点选那一刻生成的，权限边界以执行这一下的范围为准重算：
	// 群可能已被移除或换了归属。
	targets := clearTargets(b.Shared, actor)
	if len(targets) == 0 {
		b.AnswerCallback(q.ID, "没有你能清理的群")
		return
	}
	label := "全部群"
	picked := targets
	if chatID != 0 {
		t, found := findClearTarget(targets, chatID)
		if !found {
			b.AnswerCallback(q.ID, "这个群不在你能管的范围内")
			return
		}
		picked, label = []clearTarget{t}, t.Name
	}

	b.AnswerCallback(q.ID, "")
	members, chats := clearMany(b, picked)
	b.EditOrSend(q.Message.Chat.ID, q.Message.MessageID,
		clearResultText(label, chatID == 0, picked, members, chats), nil)
	slog.Info("反广告：私聊清理判定上下文", "by", actor, "目标", label,
		"群数", chats, "成员数", members)
}

// clearMany 逐群截断判定史，返回被截断的成员行数与成功的群数。
//
// 逐群一条 UPDATE 而不是一次 IN 查询：写入连接只有一条，这里是管理员手工
// 触发、群数在几十这个量级，分成几条短语句反而不会长时间占住写连接。
func clearMany(b *core.Bot, targets []clearTarget) (members, chats int64) {
	for _, t := range targets {
		n, err := ResetChatHistory(b, t.ChatID)
		if err != nil {
			slog.Error("反广告：清理群判定上下文失败",
				"chat", t.ChatID, "err", err)
			continue
		}
		members += n
		chats++
	}
	return members, chats
}

// clearPickText 渲染私聊里的目标清单。
func clearPickText(targets []clearTarget) string {
	var sb strings.Builder
	sb.WriteString("🧹 <b>清理判定上下文</b>\n\n")
	sb.WriteString("把目标群全部成员的判定史截到此刻：此前的留底不再进最近上下文与 " +
		"<code>/check</code> 复查，历史命中数归零。\n")
	sb.WriteString("<i>不解除任何限制，也不删留底与流水。</i>\n\n")
	fmt.Fprintf(&sb, "可清理 %d 个群，点按钮选目标：\n", len(targets))
	if len(targets) > clearMaxButtons {
		fmt.Fprintf(&sb, "\n群较多，清单只列前 %d 个；"+
			"其余请直接用 <code>/clear &lt;chat_id&gt;</code>。", clearMaxButtons)
	}
	return sb.String()
}

// clearPickKB 生成目标清单的按钮：一群一行，末尾追加全部群。
func clearPickKB(targets []clearTarget) map[string]any {
	rows := make([][2]string, 0, len(targets)+1)
	for i, t := range targets {
		if i >= clearMaxButtons {
			break
		}
		rows = append(rows, [2]string{core.TruncateRunes(t.Name, 30),
			clearCallbackData(t.ChatID)})
	}
	return tg.InlineKB(rows, [][2]string{
		{"🧹 全部群（" + strconv.Itoa(len(targets)) + " 个）", clearCallbackData(0)},
	})
}

// clearResultText 渲染清理结果：label 是单个目标群的显示名（all 为真时
// 忽略它），members 是这次被截断的画像行数。群名由群主自定，必须转义。
func clearResultText(label string, all bool, targets []clearTarget,
	members, chats int64) string {

	var sb strings.Builder
	switch {
	case all:
		fmt.Fprintf(&sb, "🧹 <b>已清理全部 %d 个群的判定上下文</b>\n\n", chats)
	case chats == 0:
		fmt.Fprintf(&sb, "⚠️ <b>清理失败</b>：%s\n\n", html.EscapeString(label))
		return sb.String()
	default:
		fmt.Fprintf(&sb, "🧹 <b>已清理判定上下文</b>：%s\n\n", html.EscapeString(label))
	}
	fmt.Fprintf(&sb, "成员：%d 名（判定史截到此刻，历史命中数归零）\n", members)
	sb.WriteString("此前的留底不再进最近上下文与 <code>/check</code> 复查。" +
		"不解除任何限制，留底与流水也照旧保留。")
	if all && len(targets) > 0 {
		names := make([]string, 0, len(targets))
		for _, t := range targets {
			names = append(names, t.Name)
		}
		if len(names) > clearMaxButtons {
			names = append(names[:clearMaxButtons],
				fmt.Sprintf("等 %d 个", len(targets)))
		}
		sb.WriteString("\n\n覆盖：" + escapeNames(names))
	}
	return sb.String()
}
