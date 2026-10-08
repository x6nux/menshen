package antiad

import (
	"fmt"
	"html"
	"log/slog"
	"slices"
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// ---- 私聊里的治安命令：/ban 与 /gban ----
//
// 群内的 /ban 只作用于一个群、由群管理员使用；私聊这两个命令由服务
// 管理员（主/次）使用，作用面按身份放大：
//
//	/ban  —— 在你能管的所有群里对目标执行一次禁言或封禁（范围与 /uban
//	         对称）。纯治安动作：不删发言、不进样本池、不进名单。
//	/gban —— 把目标写入联合封禁名单（全局组 + 你的专属组），立即执行，
//	         之后进群/发言都会被自动处置。
//
// 两个命令都接受方式参数 mute / ban（禁言 / 封禁出群）：私聊里没有
// “本群配置”可依，方式必须由发起人指定。/ban 缺省封禁（与群内 /ban 同为
// 纯封禁）；/gban 缺省服从各群配置（与名单的既有语义一致）。

const banDMCmdUsage = "用法：<code>/ban &lt;user_id&gt; [mute|ban]</code> 或 " +
	"<code>/ban @用户名 [mute|ban]</code>（频道填 -100 开头的频道 ID）。\n" +
	"在你能管的所有群里把此人<b>禁言</b>（mute）或<b>封禁出群</b>（ban，默认）。\n" +
	"范围与 <code>/uban</code> 相同：主管理员=全部群，次管=自己名下 bot 的群。\n" +
	"只做一次治安动作：不删发言、不进联合封禁名单 —— 要全平台拉黑用 <code>/gban</code>。"

const gbanDMCmdUsage = "用法：<code>/gban &lt;user_id&gt; [mute|ban]</code> 或 " +
	"<code>/gban @用户名 [mute|ban]</code>。\n" +
	"把此人写入<b>联合封禁名单</b>（全局组 + 你的专属组）并立即执行：" +
	"他在任何群进群或发言都会被自动处置。\n" +
	"给了 mute/ban 就按该方式执行并记进名单；不给则服从各群自己的处罚配置。\n" +
	"解除用 <code>/uban</code> 或群内 <code>/ungban</code>。"

// splitPunishMode 从 /ban、/gban 的参数尾部取出处罚方式，返回方式与剩余
// 参数（目标 user_id / @用户名）。没写方式时返回空串，由调用方决定默认值。
func splitPunishMode(arg string) (mode, rest string) {
	f := strings.Fields(arg)
	if len(f) >= 2 {
		switch strings.ToLower(f[len(f)-1]) {
		case "mute", "禁言":
			return "mute", strings.Join(f[:len(f)-1], " ")
		case "ban", "封禁":
			return "ban", strings.Join(f[:len(f)-1], " ")
		}
	}
	return "", arg
}

// HandleBanCommandDM 处理管理员私聊里的 /ban：在能管的所有群里对目标执行
// 一次禁言或封禁。调用方已确认是服务管理员。
func HandleBanCommandDM(b *core.Bot, m *tg.Message, arg string) {
	actor := m.From.ID
	if !b.IsStaff(actor) {
		return
	}
	dm := m.Chat.ID
	mode, rest := splitPunishMode(arg)
	if mode == "" {
		mode = "ban"
	}
	uid, ok := resolveUIDArg(b, rest)
	if !ok {
		b.Send(dm, banDMCmdUsage, nil)
		return
	}
	if uid == actor || uid == b.BotID() {
		b.Send(dm, "不能对你自己或本 bot 执行此操作。", nil)
		return
	}
	if !b.AdLimits.Allow(fmt.Sprintf("ban:%d", actor),
		b.Cache.Snap().BotSettingInt(b.BotID(), "antiad_cmd_rpm", 3)) {
		b.Send(dm, "操作太频繁，请稍后再试。", nil)
		return
	}
	label := punishModeLabel(mode)
	progress := b.SendGetID(dm, "⏳ 正在你能管的所有群里对 "+userLink(uid)+
		" 执行"+label+"……", nil)
	reason := fmt.Sprintf("由 %s (%d) 私聊 /ban", senderName(m.From), actor)
	// 逐群一轮 TG 调用，而更新处理是串行的，必须让出去。
	go func() {
		r := punishEverywhere(b.Shared, actor, uid, mode, reason)
		b.EditOrSend(dm, progress, banScopeSummary(uid, mode, r), nil)
	}()
}

// punishScope 是私聊 /ban 的一次全量处置结果。群名均为可读显示名。
type punishScope struct {
	Done   []string // 已处置
	Moot   []string // 无需处置（人已不在群 / 已被封禁出群）
	Failed []string // 处置失败（多半是 bot 缺权限）
	Dryrun int      // 演练群：未执行
}

// punishEverywhere 在 actor 能管的所有群里对 uid 执行一次禁言或封禁，并逐群
// 落一条可撤销的处罚流水。范围与 UbanEverywhere 对称：主管理员=全部 bot 的群，
// 次管=自己名下 bot 的群。每个群只处置一次（同群多 bot 时挑第一个非演练的）。
func punishEverywhere(sh *core.Shared, actor, uid int64, mode, reason string) punishScope {
	snap := sh.Cache.Snap()
	main := sh.IsMain(actor)
	mine := func(botID int64) bool {
		rec := snap.Bots[botID]
		return rec != nil && !rec.IsMain && (main || rec.OwnerID == actor)
	}

	var out punishScope
	eachActiveChat(sh, mine, func(bots []*core.Bot, chatID int64) {
		for _, b := range bots {
			conf, ok := b.Cache.Snap().ChatConf(b.BotID(), chatID)
			if !ok || !conf.Enabled {
				continue
			}
			if conf.Dryrun {
				out.Dryrun++
				return
			}
			act, applied, moot := punishInChat(b, conf, uid, mode)
			name := chatDisplayName(conf)
			switch {
			case moot:
				out.Moot = append(out.Moot, name)
			case !applied:
				out.Failed = append(out.Failed, name)
			default:
				out.Done = append(out.Done, name)
				logManualPunish(b, chatID, uid, act, reason)
			}
			return
		}
	})
	slices.Sort(out.Done)
	slices.Sort(out.Moot)
	slices.Sort(out.Failed)
	slog.Info("私聊 /ban 完成", "by", actor, "uid", uid, "方式", mode,
		"处置", len(out.Done), "无需", len(out.Moot), "失败", len(out.Failed),
		"演练", out.Dryrun)
	return out
}

// punishInChat 在一个群里执行一次禁言或封禁，返回动作名、是否成功、是否
// 无需处置（人已不在群、或已被封禁出群）。
func punishInChat(b *core.Bot, conf store.BotChat, uid int64, mode string) (act string, ok, moot bool) {
	if mode == "mute" {
		minutes := b.Cache.Snap().BotSettingInt(b.BotID(), "antiad_mute_minutes", 1440)
		ok, desc := MuteSender(b, conf.ChatID, uid, time.Duration(minutes)*time.Minute)
		if ok {
			return "muted", true, false
		}
		if MuteMoot(b, conf.ChatID, uid, desc) != "" {
			return "muted", false, true
		}
		slog.Warn("私聊 /ban：禁言失败", "chat", conf.ChatID, "uid", uid, "tg", desc)
		return "muted", false, false
	}
	ok, desc := BanSender(b, conf.ChatID, uid)
	if ok {
		return "banned", true, false
	}
	slog.Warn("私聊 /ban：封禁失败", "chat", conf.ChatID, "uid", uid, "tg", desc)
	return "banned", false, false
}

// logManualPunish 落一条人工治安处置的流水，供 /uad、/uban 与面板据此解除。
// action 取 muted / banned（penaltyRowsActive 认这两个名字）。
func logManualPunish(b *core.Bot, chatID, uid int64, action, reason string) {
	logAd(b, &tg.Message{Chat: &tg.Chat{ID: chatID}, From: &tg.TGUser{ID: uid}},
		adVerdict{Decider: "manual-ban", Reason: reason}, action, "")
}

// HandleGbanCommandDM 处理管理员私聊里的 /gban：写入联合封禁名单并立即执行。
// 调用方已确认是服务管理员。
func HandleGbanCommandDM(b *core.Bot, m *tg.Message, arg string) {
	actor := m.From.ID
	if !b.IsStaff(actor) {
		return
	}
	dm := m.Chat.ID
	mode, rest := splitPunishMode(arg)
	uid, ok := resolveUIDArg(b, rest)
	if !ok {
		b.Send(dm, gbanDMCmdUsage, nil)
		return
	}
	if uid == actor || uid == b.BotID() {
		b.Send(dm, "不能对你自己或本 bot 执行此操作。", nil)
		return
	}
	if !b.AdLimits.Allow(fmt.Sprintf("gban:%d", actor),
		b.Cache.Snap().BotSettingInt(b.BotID(), "antiad_cmd_rpm", 3)) {
		b.Send(dm, "操作太频繁，请稍后再试。", nil)
		return
	}
	progress := b.SendGetID(dm, "⏳ 正在把 "+userLink(uid)+" 写入联合封禁名单……", nil)
	reason := fmt.Sprintf("由 %s (%d) 私聊 /gban", senderName(m.From), actor)
	go func() {
		var notes []string
		// 全局组对所有管理员开放（共同维护的名单），专属组记在发起人名下。
		if err := GbanAddMode(b.Shared, uid, mode, reason, 0, b.BotID()); err != nil {
			slog.Error("联合封禁：手动写入全局组失败", "uid", uid, "err", err)
			notes = append(notes, "写入全局组失败")
		}
		if err := GbanOwnAddBanMode(b.Shared, actor, uid, mode, reason, 0); err != nil {
			slog.Error("联合封禁：手动写入专属组失败", "uid", uid, "err", err)
			notes = append(notes, "写入专属组失败")
		}
		enforced := GbanEnabled(b.Shared)
		if enforced {
			EnforceGban(b.Shared, uid, reason)
			EnforceGbanOwn(b.Shared, actor, uid)
		}
		b.EditOrSend(dm, progress, gbanSummary(uid, mode, enforced, notes), nil)
	}()
}

// gbanSummary 渲染 /gban 的结果回执。
func gbanSummary(uid int64, mode string, enforced bool, notes []string) string {
	var sb strings.Builder
	sb.WriteString("🚫 <b>已写入联合封禁名单</b>：" + userLink(uid) + "\n")
	sb.WriteString("执行方式：" + punishModeDesc(mode) + "\n")
	if enforced {
		sb.WriteString("已在覆盖范围内立即执行（演练群不动手）。\n")
	} else {
		sb.WriteString("⚠️ 联合封禁总开关当前是<b>关</b>的，名单先记下，开启后自动执行。\n")
	}
	if len(notes) > 0 {
		sb.WriteString("⚠️ " + html.EscapeString(strings.Join(notes, "；")) + "\n")
	}
	sb.WriteString("\n解除：<code>/uban &lt;user_id&gt;</code> 全解，或群内 <code>/ungban</code>。")
	return sb.String()
}

// banScopeSummary 渲染 /ban 的结果回执。
func banScopeSummary(uid int64, mode string, r punishScope) string {
	var sb strings.Builder
	sb.WriteString(punishModeEmoji(mode) + " <b>已执行" + punishModeLabel(mode) +
		"</b>：" + userLink(uid) + "\n")
	fmt.Fprintf(&sb, "\n共处置 %d 个群。", len(r.Done))
	if len(r.Done) > 0 {
		sb.WriteString("\n✅ " + escapeNames(r.Done))
	}
	if len(r.Moot) > 0 {
		sb.WriteString("\n➖ 无需处置：" + escapeNames(r.Moot))
	}
	if len(r.Failed) > 0 {
		sb.WriteString("\n⚠️ 处置失败（多半是 bot 缺权限）：" + escapeNames(r.Failed))
	}
	if r.Dryrun > 0 {
		fmt.Fprintf(&sb, "\n🧪 %d 个演练群未执行。", r.Dryrun)
	}
	if len(r.Done)+len(r.Moot)+len(r.Failed) == 0 {
		sb.WriteString("\n没有可处置的群。")
	}
	sb.WriteString("\n\n解除用 <code>/uban &lt;user_id&gt;</code>。")
	return sb.String()
}

func escapeNames(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = html.EscapeString(n)
	}
	return strings.Join(out, "、")
}

func punishModeLabel(mode string) string {
	if mode == "mute" {
		return "禁言"
	}
	return "封禁出群"
}

func punishModeEmoji(mode string) string {
	if mode == "mute" {
		return "🔇"
	}
	return "🚫"
}

// punishModeDesc 描述 /gban 的执行方式：空串表示不指定、服从各群配置。
func punishModeDesc(mode string) string {
	switch mode {
	case "mute":
		return "禁言（覆盖各群配置）"
	case "ban":
		return "封禁出群（覆盖各群配置）"
	}
	return "服从各群自己的处罚配置"
}
