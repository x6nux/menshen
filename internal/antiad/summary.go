package antiad

import (
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/tg"
)

// ---- 管理员私聊：定时汇总 ----
//
// 逐条私聊在刷屏高峰里会刷爆私聊，还会撞上 TG 的发送频率限制，把删除、
// 禁言这些要紧的调用一起拖慢。所以命中只落流水，由 FlushAdSummary 每分钟
// 看一次、距上次发出满 antiad_alert_every 分钟才汇成一条发出去。
//
// 按 bot 汇总、按 bot 存游标（bot_settings.antiad_alert_last_id）：次级管理员
// 只该看到自己 bot 的命中，而重启不丢、不重发。

const (
	// adSummaryList 是汇总里逐条列出的命中上限，其余只给条数。
	adSummaryList = 20
	// adSummaryButtons 是带「进记录卡片」按钮的条数，多了键盘会比正文还长。
	adSummaryButtons = 8
)

// FlushAdSummary 把这个 bot 新的判定汇总成一条私聊发给它的告警对象。
// 平静时期的第一条命中一分钟内就能看到，刷屏高峰里也只是每隔几分钟一条。
func FlushAdSummary(b *core.Bot, now time.Time) {
	snap := b.Cache.Snap()
	id := b.BotID()
	every := time.Duration(snap.BotSettingInt(id, "antiad_alert_every", 5)) * time.Minute
	if last := b.SummaryAt.Load(); last != 0 && now.Sub(time.Unix(last, 0)) < every {
		return
	}
	var maxID int64
	if err := b.Store.Read.QueryRow(`SELECT COALESCE(MAX(id),0) FROM antiad_log
		WHERE bot_id=?`, id).Scan(&maxID); err != nil {
		slog.Error("反广告：读取流水游标失败", "err", err)
		return
	}
	cursor := snap.BotSettingInt(id, "antiad_alert_last_id", -1)
	if cursor >= maxID {
		return
	}
	// 游标为 -1 是升级后第一次运行：只记下位置，不把过去的命中翻出来发。
	// 私聊关着时游标照走，再打开不补发。
	notify := cursor >= 0 && snap.BotSettingInt(id, "antiad_dm_admins", 1) == 1
	if notify {
		text, kb, ok := renderAdSummary(b, cursor, maxID)
		if ok {
			for _, to := range b.AlertTargets() {
				b.Send(to, text, kb)
			}
			b.SummaryAt.Store(now.Unix())
		}
	}
	// 游标在渲染与发送之后再推进：先推进的话，渲染出错或发送失败的那批
	// 命中会永久从汇总里消失，只剩面板里能查回来。
	if err := b.PutBotSetting(id, "antiad_alert_last_id", strconv.FormatInt(maxID, 10)); err != nil {
		slog.Error("反广告：保存汇总游标失败", "err", err)
	}
}

// summaryHit 是汇总里列出的一条命中。
type summaryHit struct {
	ID, ChatID, UserID    int64
	Confidence            float64
	Verdict, Kind, Action string
	Reason                string
}

// renderAdSummary 渲染 (from, to] 区间的汇总。没有命中、复判为正常与判定失败时
// ok 为假。不带原文与昵称：要看的点进记录卡片。
//
// 「复判为正常」是初判先删了、复判又说不是广告的（见 judgeAndAct）：
// 误删了一条正常消息，管理员得知道。
func renderAdSummary(b *core.Bot, from, to int64) (string, map[string]any, bool) {
	id := b.BotID()
	var hits, cleared, errs, skipped int64
	if err := b.Store.Read.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN verdict='ad' AND action NOT IN ('none','join_checked','prewarm_checked') THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN verdict='clean' AND action NOT IN ('none','join_checked','prewarm_checked') THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN verdict='error' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN verdict='skipped' THEN 1 ELSE 0 END),0)
		FROM antiad_log WHERE bot_id=? AND id > ? AND id <= ?`, id, from, to).
		Scan(&hits, &cleared, &errs, &skipped); err != nil {
		slog.Error("反广告：统计汇总失败", "err", err)
		return "", nil, false
	}
	if hits == 0 && cleared == 0 && errs == 0 {
		return "", nil, false
	}

	var list []summaryHit
	rows, err := b.Store.Read.Query(`SELECT id,chat_id,user_id,verdict,confidence,ad_kind,action,reason
		FROM antiad_log WHERE bot_id=? AND id > ? AND id <= ? AND action NOT IN ('none','join_checked','prewarm_checked')
		ORDER BY chat_id, id LIMIT ?`, id, from, to, adSummaryList)
	if err != nil {
		slog.Error("反广告：读取汇总命中失败", "err", err)
		return "", nil, false
	}
	for rows.Next() {
		var h summaryHit
		if rows.Scan(&h.ID, &h.ChatID, &h.UserID, &h.Verdict, &h.Confidence, &h.Kind,
			&h.Action, &h.Reason) == nil {
			list = append(list, h)
		}
	}
	rows.Close()

	snap := b.Cache.Snap()
	var sb strings.Builder
	sb.WriteString("📊 <b>反广告汇总</b>\n")
	fmt.Fprintf(&sb, "命中 %d · 判定失败 %d · 未送检 %d", hits, errs, skipped)
	if cleared > 0 {
		fmt.Fprintf(&sb, " · 复判为正常 %d", cleared)
	}
	sb.WriteString("\n")
	var lastChat int64
	var kb [][][2]string
	var btnRow [][2]string
	for i, h := range list {
		if i == 0 || h.ChatID != lastChat {
			title := strconv.FormatInt(h.ChatID, 10)
			if c, ok := snap.ChatConf(id, h.ChatID); ok && c.Title != "" {
				title = c.Title
			}
			fmt.Fprintf(&sb, "\n<b>%s</b>\n", html.EscapeString(title))
			lastChat = h.ChatID
		}
		if h.Verdict == "clean" {
			fmt.Fprintf(&sb, "• <code>#%d</code> ↩️ 复判为正常 · %s · %s",
				h.ID, userLink(h.UserID), ActionLabel(h.Action))
		} else {
			fmt.Fprintf(&sb, "• <code>#%d</code> %s %.0f%% · %s · %s",
				h.ID, html.EscapeString(adKindLabel(h.Kind)), h.Confidence*100,
				userLink(h.UserID), ActionLabel(h.Action))
		}
		if strings.Contains(h.Reason, purgeNote) {
			sb.WriteString(" · 全删")
		}
		// 失败说明都是「xx失败: 原因」的形状（见 ApplyAction）；模型写的理由
		// 用全角标点，不会误中。
		if strings.Contains(h.Reason, "失败: ") {
			sb.WriteString(" ⚠️")
		}
		sb.WriteString("\n")
		if i < adSummaryButtons {
			btnRow = append(btnRow, [2]string{fmt.Sprintf("📋 #%d", h.ID),
				fmt.Sprintf("a:ad:rec:%d", h.ID)})
			if len(btnRow) == 4 {
				kb, btnRow = append(kb, btnRow), nil
			}
		}
	}
	if len(btnRow) > 0 {
		kb = append(kb, btnRow)
	}
	if more := hits + cleared - int64(len(list)); more > 0 {
		fmt.Fprintf(&sb, "… 另有 %d 条，见「📋 拦截记录」\n", more)
	}
	if errs > 0 {
		sb.WriteString("\n判定失败的消息已放行，持续出现请检查上游与模型配置。\n")
	}
	return sb.String(), tg.InlineKB(kb...), true
}

// RenderAdRecord 渲染一条流水的记录卡片（汇总里点进来的），只发到管理员私聊，
// 所以带原文与理由；处置按钮按记录上实际发生过的动作给。
func RenderAdRecord(b *core.Bot, r AdLogRow) (string, map[string]any) {
	dryrun := strings.HasPrefix(r.Action, "dryrun:")
	name := strings.TrimPrefix(r.Action, "dryrun:")
	act := adAction{Name: name,
		Delete: strings.Contains(name, "deleted"),
		Mute:   strings.Contains(name, "muted"),
		Ban:    strings.Contains(name, "banned")}

	loc := b.Cache.Snap().Location()
	var sb strings.Builder
	fmt.Fprintf(&sb, "📋 <b>记录 #%d</b>\n\n", r.ID)
	fmt.Fprintf(&sb, "时间: %s\n", time.Unix(r.CreatedAt, 0).In(loc).Format("01-02 15:04"))
	fmt.Fprintf(&sb, "群组: <code>%d</code>\n用户: %s\n", r.ChatID, userLink(r.UserID))
	fmt.Fprintf(&sb, "判定: %s · %s · 置信度 %.0f%% · 来源 %s\n",
		map[string]string{"ad": "广告", "clean": "正常", "error": "失败", "skipped": "未送检"}[r.Verdict],
		html.EscapeString(adKindLabel(r.Kind)), r.Confidence*100, html.EscapeString(r.Decider))
	fmt.Fprintf(&sb, "处置: %s\n", html.EscapeString(ActionLabel(r.Action)))
	// 原文与理由直接给管理员看：记录卡片的用途就是让他当场判断，
	// 再让人去网页上翻一遍是把判断成本推给了他。
	if r.Reason != "" {
		fmt.Fprintf(&sb, "理由: %s\n", html.EscapeString(r.Reason))
	}
	fmt.Fprintf(&sb, "\n原文:\n<code>%s</code>",
		html.EscapeString(core.TruncateRunes(r.Text, 500)))
	rows := adAlertRows(act, r.Action, r.Reason, r.ID, dryrun,
		"🔇 "+MuteLabel(b.Cache.Snap().BotSettingInt(r.BotID, "antiad_mute_minutes", 1440)))
	if links := adAlertLinks(b, r.ID); len(links) > 0 {
		rows = append(rows, links)
	}
	return sb.String(), tg.InlineKB(rows...)
}

// ActionLabel 把 antiad_log.action 的机器值翻成人话。
// 完整词汇表：none/alerted/deleted/muted/deleted_muted/deleted_banned/banned/
// join_muted/join_checked/prewarm_muted/prewarm_checked/undone，以及以上任意值前缀 "dryrun:" 表示演练期本应执行、实际未执行。
// 漏掉任何一个值都会导致面板直接显示原始字符串。
func ActionLabel(a string) string {
	switch {
	case a == "none":
		return "未处置"
	case a == "alerted":
		return "仅告警"
	case a == "deleted":
		return "已删除"
	case a == "muted":
		return "已禁言"
	case a == "deleted_muted":
		return "已删除+禁言"
	case a == "deleted_banned":
		return "已删除+封禁"
	case a == "banned":
		return "已封禁"
	case a == "join_muted":
		return "进群限制发言"
	case a == "join_checked":
		return "入群检查"
	case a == actionPrewarmMuted:
		return "前置号限制发言"
	case a == actionPrewarmChecked:
		return "前置号检查"
	case a == "undone":
		return "已标记误判"
	case strings.HasPrefix(a, "dryrun:"):
		return "演练（本应" + ActionLabel(strings.TrimPrefix(a, "dryrun:")) + "）"
	}
	return a
}

// AdKindLabel 供面板使用，见 adKindLabel。
func AdKindLabel(kind string) string { return adKindLabel(kind) }
