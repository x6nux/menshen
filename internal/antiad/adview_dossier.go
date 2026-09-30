package antiad

import (
	"fmt"
	"strconv"
	"strings"

	"menshen/internal/core"
)

// ---- 申诉详情页左栏：用户资料 ----
//
// 管理员打开一张申诉单，先要回答三个问题：这是谁、他以前干过什么、
// 现在还有哪些限制生效。左栏把这些「资料」一次列全，右栏才是申诉单
// 本身的复核与解禁码。数据只取本 bot 名下（它自己的流水与群），不跨
// bot 泄露别的群主的数据。

// 各种上限：资料栏不能被一个刷了几千条的号撑爆。
const (
	dossierMsgLimit   = 100 // 留底发言
	dossierMsgShow    = 30  // 留底先展开几条，其余折叠
	dossierLogLimit   = 30  // 判定流水
	dossierPenaLimit  = 20  // 处罚记录
	dossierCheckLimit = 20  // 网页验证
	dossierTextLen    = 200 // 单条发言截断
	dossierNoteLen    = 300 // 理由 / 信号等说明性文字截断
)

// clip 去掉首尾空白并截断成 n 个字符。
func clip(s string, n int) string {
	return strings.TrimSpace(core.TruncateRunes(strings.TrimSpace(s), n))
}

// inClause 生成 IN (?,?,...) 与其参数；空列表返回 IN (NULL)（永不命中）。
func inClause(ids []int64) (string, []any) {
	if len(ids) == 0 {
		return "IN (NULL)", nil
	}
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
	}
	return "IN (" + strings.Join(ph, ",") + ")", args
}

// appealStatusLabel 把申诉单状态翻成管理员看得懂的中文，措辞与 Mini App
// 的申诉列表保持一致。
func appealStatusLabel(s string) string {
	switch s {
	case "statement":
		return "待写理由"
	case "ai":
		return "AI 复核中"
	case "web":
		return "等网页验证"
	case "noweb":
		return "待人工处理"
	case "code":
		return "已发解禁码"
	case "redeemed":
		return "已兑换"
	case "lifted":
		return "已解除"
	case "rejected":
		return "已驳回"
	case "expired":
		return "已过期"
	}
	return s
}

// penaltyLabel 把处罚类型翻成管理员看得懂的词。
func penaltyLabel(t string) string {
	switch t {
	case "join_profile":
		return "进群资料审核"
	case "message":
		return "消息判定"
	case "gban":
		return "联合封禁"
	case "gban_own":
		return "专属联合封禁"
	}
	return t
}

// penaKey 是「同一条处罚」的身份：类型 + 群 + 时间，用来把最近流水里
// 的条目与仍在生效的限制对上。
func penaKey(typ string, chatID, at int64) string {
	return fmt.Sprintf("%s|%d|%d", typ, chatID, at)
}

// loadAppealDossier 填充申诉详情页左栏。
func loadAppealDossier(sh *core.Shared, ap appealRec, data *appealViewData) {
	snap := sh.Cache.Snap()
	if r := snap.Bots[ap.BotID]; r != nil {
		data.Bot = r.Label()
	} else {
		data.Bot = strconv.FormatInt(ap.BotID, 10)
	}

	// 群标题：给资料里的 chat_id 配上人看得懂的名字，配置删了就只有 ID。
	titles := map[int64]string{}
	for _, c := range snap.ChatsOf(ap.BotID) {
		if t := strings.TrimSpace(c.Title); t != "" {
			titles[c.ChatID] = t
		}
	}
	label := func(id int64) string {
		if t, ok := titles[id]; ok {
			return t
		}
		return strconv.FormatInt(id, 10)
	}

	// 判定时记下的昵称。广告号被处置后常改名，现场查（也多半查不到：
	// 没私聊过）没有意义。
	sh.Store.Read.QueryRow(`SELECT user_name FROM antiad_log
		WHERE bot_id=? AND user_id=? ORDER BY id DESC LIMIT 1`,
		ap.BotID, ap.UserID).Scan(&data.UName)

	// 本 bot 名下的群；处罚流水里出现过、但群配置已删的也带上，否则
	// 画像与留底会整段消失。
	chats := []int64{}
	seen := map[int64]bool{}
	for _, c := range snap.ChatsOf(ap.BotID) {
		if !seen[c.ChatID] {
			seen[c.ChatID] = true
			chats = append(chats, c.ChatID)
		}
	}
	rows, err := sh.Store.Read.Query(`SELECT DISTINCT chat_id FROM antiad_log
		WHERE bot_id=? AND user_id=? AND chat_id<>0 LIMIT 20`, ap.BotID, ap.UserID)
	if err == nil {
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil && !seen[id] {
				seen[id] = true
				chats = append(chats, id)
			}
		}
		rows.Close()
	}
	in, inArgs := inClause(chats)

	// 账号画像：首次见到 / 最早入群 / 最近发言 / 发言数 / 命中数 / 群数。
	var firstSeen, joined, lastMsg, msgs, hits, chatCount int64
	args := append([]any{ap.UserID}, inArgs...)
	if err := sh.Store.Read.QueryRow(`SELECT COALESCE(MIN(first_seen),0),
		COALESCE(MIN(NULLIF(joined_at,0)),0), COALESCE(MAX(last_msg_at),0),
		COALESCE(SUM(msg_count),0), COALESCE(SUM(ad_hits),0), COUNT(*)
		FROM group_members WHERE user_id=? AND chat_id `+in, args...).
		Scan(&firstSeen, &joined, &lastMsg, &msgs, &hits, &chatCount); err == nil {
		data.FirstSeen = formatTS(sh, firstSeen)
		if joined > 0 {
			data.Joined = formatTS(sh, joined)
		}
		data.LastMsg = formatTS(sh, lastMsg)
		data.Msgs, data.Hits, data.Chats = msgs, hits, chatCount
	}

	if g, ok := snap.Gban[ap.UserID]; ok {
		s := "已列入"
		if r := strings.TrimSpace(g.Reason); r != "" {
			s += "（" + clip(r, 60) + "）"
		}
		data.GBan = s
	}

	// 当前生效的限制：进群资料审核 + 禁言期内未解除的消息处置 + 联合封禁。
	// 这份名单是「为什么他还在被限制」的权威答案，与下面的历史流水分开。
	active := map[string]bool{}
	for _, p := range effectivePenalties(sh, ap.BotID, ap.UserID) {
		v := appealViewPenalty{Type: p.Type, ChatID: p.ChatID, Label: penaltyLabel(p.Type),
			Text: clip(p.Text, dossierTextLen), Reason: clip(p.Reason, dossierNoteLen),
			At: p.At, Time: formatTS(sh, p.At)}
		switch p.Type {
		case "gban":
			v.Chat = "全平台"
		case "gban_own":
			v.Chat = "本 bot 名下群组"
		default:
			v.Chat = label(p.ChatID)
		}
		active[penaKey(p.Type, p.ChatID, p.At)] = true
		data.Limits = append(data.Limits, v)
	}

	// 处罚记录：最近 20 条。生效中的已经单列一张卡（还含联合封禁），这里
	// 只留不再生效的历史，免得同一条罚款在页面上出现两遍。
	rows, err = sh.Store.Read.Query(`SELECT type,chat_id,text,reason,at FROM (
			SELECT 'join_profile' AS type, chat_id, '' AS text, reason, created_at AS at
			FROM join_mutes WHERE bot_id=? AND user_id=?
			UNION ALL
			SELECT 'message', chat_id, text, reason, created_at
			FROM antiad_log WHERE bot_id=? AND user_id=?
			AND action IN ('deleted_muted','muted','deleted_banned','banned')
		) ORDER BY at DESC LIMIT ?`,
		ap.BotID, ap.UserID, ap.BotID, ap.UserID, dossierPenaLimit)
	if err == nil {
		for rows.Next() {
			var p appealViewPenalty
			if rows.Scan(&p.Type, &p.ChatID, &p.Text, &p.Reason, &p.At) == nil {
				if active[penaKey(p.Type, p.ChatID, p.At)] {
					continue
				}
				p.Label = penaltyLabel(p.Type)
				p.Chat = label(p.ChatID)
				p.Time = formatTS(sh, p.At)
				p.Text = clip(p.Text, dossierTextLen)
				p.Reason = clip(p.Reason, dossierNoteLen)
				data.Penalties = append(data.Penalties, p)
			}
		}
		rows.Close()
	}

	// 被处置过的消息：在留底里标出来（真实处置优先于演练命中）。
	marks := map[[2]int64]string{}
	rows, err = sh.Store.Read.Query(`SELECT chat_id,message_id,action FROM antiad_log
		WHERE bot_id=? AND user_id=? AND message_id<>0 AND action IN
		('deleted','deleted_muted','deleted_banned','banned',
		 'dryrun:deleted','dryrun:deleted_muted','dryrun:deleted_banned','dryrun:banned')`,
		ap.BotID, ap.UserID)
	if err == nil {
		for rows.Next() {
			var chatID, msgID int64
			var action string
			if rows.Scan(&chatID, &msgID, &action) != nil {
				continue
			}
			mark := "演练命中"
			if !strings.HasPrefix(action, "dryrun:") {
				mark = "被拦"
			}
			k := [2]int64{chatID, msgID}
			if marks[k] == "" || mark == "被拦" {
				marks[k] = mark
			}
		}
		rows.Close()
	}

	// 留底发言：本 bot 各群里最近的 100 条。这是「他到底发了什么」最
	// 直接的证据，也是判断申诉是否可信的主要依据。同一个群的连续发言
	// 只在第一条上标群名，否则一列全是一样的群名。
	args = append([]any{ap.UserID}, inArgs...)
	args = append(args, dossierMsgLimit)
	rows, err = sh.Store.Read.Query(`SELECT chat_id,message_id,text,at FROM group_messages
		WHERE user_id=? AND text!='' AND chat_id `+in+`
		ORDER BY at DESC LIMIT ?`, args...)
	if err == nil {
		last := "\x00"
		for rows.Next() {
			var m appealViewMsg
			var chatID, msgID int64
			if rows.Scan(&chatID, &msgID, &m.Text, &m.At) != nil {
				continue
			}
			if c := label(chatID); c != last {
				m.Chat, last = c, c
			}
			m.Time = formatTS(sh, m.At)
			m.Text = clip(m.Text, dossierTextLen)
			m.Mark = marks[[2]int64{chatID, msgID}]
			m.Blocked = m.Mark == "被拦"
			data.History = append(data.History, m)
		}
		rows.Close()
	}
	// 太长的留底折起来：资料栏不能被一个刷了一百条的号顶到底。
	data.HistoryCount = len(data.History)
	if len(data.History) > dossierMsgShow {
		data.History, data.HistoryMore = data.History[:dossierMsgShow], data.History[dossierMsgShow:]
	}

	// 判定流水：最近 30 条（含未处置的 none），能看出惯犯还是一次失误。
	rows, err = sh.Store.Read.Query(`SELECT id,chat_id,verdict,confidence,action,reason,created_at
		FROM antiad_log WHERE bot_id=? AND user_id=? ORDER BY id DESC LIMIT ?`,
		ap.BotID, ap.UserID, dossierLogLimit)
	if err == nil {
		for rows.Next() {
			var l appealViewLog
			if rows.Scan(&l.ID, &l.ChatID, &l.Verdict, &l.Conf, &l.Action,
				&l.Reason, &l.At) == nil {
				l.Chat = label(l.ChatID)
				l.Time = formatTS(sh, l.At)
				l.ActionLabel = ActionLabel(l.Action)
				l.Reason = clip(l.Reason, dossierNoteLen)
				data.Logs = append(data.Logs, l)
			}
		}
		rows.Close()
	}

	// 网页验证记录：IP、指纹、UA 对核对关联账号与「是不是同一个人」有用，
	// 所以按账号取全部验证（不只本单）。
	var fp, ip string
	rows, err = sh.Store.Read.Query(`SELECT result,flags,ip,fp,ua,created_at FROM web_checks
		WHERE bot_id=? AND user_id=? ORDER BY id DESC LIMIT ?`,
		ap.BotID, ap.UserID, dossierCheckLimit)
	if err == nil {
		for rows.Next() {
			var c webCheckView
			if rows.Scan(&c.Result, &c.Flags, &c.IP, &c.FP, &c.UA, &c.At) == nil {
				c.Time = formatTS(sh, c.At)
				c.Flags = clip(c.Flags, dossierNoteLen)
				c.UA = clip(c.UA, 120)
				if fp == "" {
					fp, ip = c.FP, c.IP
				}
				data.Checks = append(data.Checks, c)
			}
		}
		rows.Close()
	}
	strong, weak := relatedAccounts(sh, fp, ip, ap.UserID)
	for _, uid := range strong {
		data.Strong = append(data.Strong, relatedView{UID: uid, Mark: relatedMark(sh, uid)})
	}
	for _, uid := range weak {
		data.Weak = append(data.Weak, relatedView{UID: uid, Mark: relatedMark(sh, uid)})
	}
}
