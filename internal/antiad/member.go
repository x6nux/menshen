package antiad

import (
	"database/sql"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// groupMember 是 group_members 的一行。
type groupMember struct {
	ChatID, UserID      int64
	JoinedAt, FirstSeen int64
	MsgCount, LastMsgAt int64
	AdHits              int64
	// Whitelisted 是 /white 的本群白名单。随画像一起读出来，判定链路
	// 就不必再为每条消息单查一次这一列。
	Whitelisted bool
	// ProfileHash 是上次复查时落下的资料指纹（profileHash）。空串表示
	// 还没查过；指纹没变时复查只推时间、不花 AI。
	ProfileHash string
	// PrewarmNextAt 是下一次前置号复查的到期时间（预排）。0 = 从未复查。
	// 选人走 SQL 的 prewarm_next_at <= now，这个字段用于测试断言与排查。
	PrewarmNextAt int64
	// Known 表示这一行确实读到了。读失败时为零值，而零值画像会让
	// isNewbie 把老成员当新人从重处置——失败方向反了，所以调用方
	// 必须先看这个标记。
	Known bool
}

func loadMember(s *store.Store, chatID, uid int64) (groupMember, bool) {
	gm := groupMember{ChatID: chatID, UserID: uid}
	err := s.Read.QueryRow(`SELECT joined_at,first_seen,msg_count,last_msg_at,
		ad_hits,whitelisted,profile_hash,prewarm_next_at
		FROM group_members WHERE chat_id=? AND user_id=?`, chatID, uid).
		Scan(&gm.JoinedAt, &gm.FirstSeen, &gm.MsgCount, &gm.LastMsgAt,
			&gm.AdHits, &gm.Whitelisted, &gm.ProfileHash, &gm.PrewarmNextAt)
	if err != nil {
		// 没有行是常态（编辑过的消息、行被清理过），读失败要看得见：
		// 两者都返回 false，混在一起排查时会以为是「没这条画像」。
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Error("反广告：读取成员画像失败", "chat", chatID, "uid", uid, "err", err)
		}
		return gm, false
	}
	gm.Known = true
	return gm, true
}

// profileFromKept 在画像读不到时用留底条数凑一份最小画像。
//
// 不能直接不判：**编辑过的消息**很容易走到这里（原消息不在留底里，或行被
// 清理过），而「先发正常、再编辑成广告」正是要拦的规避形态。用留底条数当
// 发言数、年龄按未知处理（AgeKnown 不上），与 /check 复查同一套口径 ——
// 拿零值画像当新人反而会把老成员按最严档处置。
func profileFromKept(b *core.Bot, chatID, uid int64) groupMember {
	gm := groupMember{ChatID: chatID, UserID: uid, Known: true}
	var kept int64
	if err := b.Store.Read.QueryRow(`SELECT COUNT(*) FROM group_messages
		WHERE chat_id=? AND user_id=?`, chatID, uid).Scan(&kept); err != nil {
		slog.Error("反广告：统计留底条数失败", "chat", chatID, "uid", uid, "err", err)
	}
	gm.MsgCount = kept
	slog.Warn("反广告：画像读不到，按留底条数估算", "chat", chatID,
		"uid", uid, "留底", kept)
	return gm
}

// handleChatMemberUpdate 把「进群」落成 joined_at。
//
// 只认「从非成员状态变为成员状态」这一次跃迁：status 在 member 与
// restricted 之间来回跳（群管给人挂临时限制）不是进群，按进群处理会
// 把一个老成员重新变成「新人」，下一条消息就被按最严档处置。
func HandleChatMemberUpdate(b *core.Bot, cu *tg.ChatMemberUpdated) {
	if cu == nil || cu.Chat == nil || cu.NewChatMember == nil ||
		cu.NewChatMember.User == nil {
		return
	}

	// 「进群」这一跃迁先把入群时间落库再谈别的 —— 这是 bot 亲眼看到入群
	// 的原始证据，不该受「这个群开没开反广告」「这个人是不是分给了别的
	// bot」影响。MTProto 回查拿不到（权限不够、已退群）时它就是 /jtime
	// 与年龄轴的回退值。recordJoin 幂等，下面 onJoin 再走一次也无妨。
	old := ""
	if cu.OldChatMember != nil {
		old = cu.OldChatMember.Status
	}
	if isMemberStatus(cu.NewChatMember.Status) && !isMemberStatus(old) {
		at := cu.Date
		if at == 0 {
			at = time.Now().Unix()
		}
		recordJoin(b, cu.Chat.ID, cu.NewChatMember.User.ID, at)
	}

	conf, ok := chatActive(b, cu.Chat.ID)
	if !ok {
		return
	}
	// 我们的禁言被外部解除时重新施加（入群验证机器人验证通过后会把权限
	// 全量开回来，那一下会盖掉我们给的进群限制）。
	reassertMute(b, conf, cu)

	if !isMemberStatus(cu.NewChatMember.Status) || isMemberStatus(old) {
		return
	}
	if !b.ClaimSender(cu.Chat.ID, cu.NewChatMember.User.ID, time.Now()) {
		return
	}
	at := cu.Date
	if at == 0 {
		at = time.Now().Unix()
	}
	onJoin(b, conf, cu.NewChatMember.User, at)
}

// isMemberStatus 报告该状态是否代表「人在群里」。
// restricted 也算：它是 TG 表达「权限非默认满值」的方式，
// 普通成员在设了默认限制的群里就是这个状态。
func isMemberStatus(s string) bool {
	switch s {
	case "member", "administrator", "creator", "restricted":
		return true
	}
	return false
}

// recordJoin 写入进群时刻。已有记录则更新 joined_at 并把 prewarm_next_at
// 清零——退群重进的人按新成员节奏复查，而不是继承旧的高档位间隔；不动
// msg_count —— 退群重进的人，历史发言量仍是有效画像。
//
// 返回更新后的画像（含 whitelisted）：进群路径接着就要判白名单与豁免，
// RETURNING 顺手带回来，省掉一次单查。
func recordJoin(b *core.Bot, chatID, uid, at int64) groupMember {
	gm := groupMember{ChatID: chatID, UserID: uid}
	err := b.Store.Write.QueryRow(`INSERT INTO group_members
		(chat_id,user_id,joined_at,first_seen,msg_count,last_msg_at,ad_hits)
		VALUES (?,?,?,?,0,0,0)
		ON CONFLICT(chat_id,user_id) DO UPDATE SET joined_at=excluded.joined_at,
		  prewarm_next_at=0
		RETURNING joined_at,first_seen,msg_count,last_msg_at,ad_hits,whitelisted`,
		chatID, uid, at, at).
		Scan(&gm.JoinedAt, &gm.FirstSeen, &gm.MsgCount, &gm.LastMsgAt,
			&gm.AdHits, &gm.Whitelisted)
	if err != nil {
		slog.Error("反广告：记录进群失败", "chat", chatID, "uid", uid, "err", err)
		if got, ok := loadMember(b.Store, chatID, uid); ok {
			return got
		}
		return gm // Known=false：调用方按「画像未知」处理
	}
	gm.Known = true
	return gm
}

// touchMember 记一条发言并返回更新后的画像。
//
// first_seen 只在插入时写，后续发言不得覆盖：它是 joined_at 缺失时
// 唯一的年龄下界，被每条消息刷新的话所有人都会永远是「刚出现」。
//
// 用 RETURNING 一条语句拿回更新后的整行：这是每条群消息的必经之路，
// 而写连接只有一条，INSERT+SELECT 两次往返就是两次排队。顺带把
// whitelisted 带回来，判定链路不必再单查一次白名单。
func touchMember(b *core.Bot, chatID, uid, at int64) groupMember {
	gm := groupMember{ChatID: chatID, UserID: uid}
	err := b.Store.Write.QueryRow(`INSERT INTO group_members
		(chat_id,user_id,joined_at,first_seen,msg_count,last_msg_at,ad_hits)
		VALUES (?,?,0,?,1,?,0)
		ON CONFLICT(chat_id,user_id) DO UPDATE SET
		  msg_count = msg_count + 1,
		  last_msg_at = excluded.last_msg_at
		RETURNING joined_at,first_seen,msg_count,last_msg_at,ad_hits,whitelisted`,
		chatID, uid, at, at).
		Scan(&gm.JoinedAt, &gm.FirstSeen, &gm.MsgCount, &gm.LastMsgAt,
			&gm.AdHits, &gm.Whitelisted)
	if err != nil {
		slog.Error("反广告：记录发言失败", "chat", chatID, "uid", uid, "err", err)
		// 退回一次显式读取，尽量别把画像丢成零值。
		if got, ok := loadMember(b.Store, chatID, uid); ok {
			return got
		}
		return gm // Known=false：调用方按「画像未知」处理
	}
	gm.Known = true
	return gm
}

// gmsgTextLimit 是留底正文的字符上限。与判定日志同一量级：
// 留底是给人和模型复查用的，不是原样归档聊天记录。
const gmsgTextLimit = 1000

// recordMessage 把一条群消息留底，供 /check 事后复查与连带删除。编辑过的
// 消息覆盖正文：复查要看的是群里现在显示的样子。album 是相册 ID。
//
// 失败只记日志不中断：留底是复查用的辅助设施，不该让一次写失败
// 把这条消息的判定也一起拖没。
func recordMessage(b *core.Bot, chatID, msgID, uid int64, text string, at int64, album string) {
	if _, err := b.Store.Write.Exec(`INSERT INTO group_messages
		(chat_id,message_id,user_id,text,at,media_group) VALUES (?,?,?,?,?,?)
		ON CONFLICT(chat_id,message_id) DO UPDATE SET text=excluded.text`,
		chatID, msgID, uid, core.TruncateRunes(text, gmsgTextLimit), at, album); err != nil {
		slog.Error("反广告：留底失败", "chat", chatID, "msg", msgID, "err", err)
	}
}

// recordedText 取这条消息的留底正文，没有留底时返回空串。
func recordedText(b *core.Bot, chatID, msgID int64) string {
	var t string
	b.Store.Read.QueryRow(`SELECT text FROM group_messages
		WHERE chat_id=? AND message_id=?`, chatID, msgID).Scan(&t)
	return t
}

// gmsgRow 是一条留底记录。
type gmsgRow struct {
	MessageID int64
	Text      string
	At        int64
}

// loadUserMessages 取某人在某群的留底，按时间正序返回最近 limit 条。
//
// 正序是给模型看的：复查要判断「这个账号一路以来在干什么」，
// 倒序会让它把最新的当成开头。
func loadUserMessages(s *store.Store, chatID, uid int64, limit int) []gmsgRow {
	// 没有文字的（图片、贴纸）只为连带删除留着 ID，复查里是空行。
	rows, err := s.Read.Query(`SELECT message_id,text,at FROM group_messages
		WHERE chat_id=? AND user_id=? AND text != ''
		ORDER BY at DESC, message_id DESC LIMIT ?`,
		chatID, uid, limit)
	if err != nil {
		slog.Error("反广告：读取留底失败", "chat", chatID, "uid", uid, "err", err)
		return nil
	}
	defer rows.Close()

	var out []gmsgRow
	for rows.Next() {
		var r gmsgRow
		if err := rows.Scan(&r.MessageID, &r.Text, &r.At); err != nil {
			slog.Error("反广告：留底行解析失败", "err", err)
			continue
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		slog.Error("反广告：留底游标出错，结果可能不完整", "err", err)
	}
	// 查询按倒序取「最近 N 条」，返回前翻正序。
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// hasUserData 报告本群数据库里有没有这个人的痕迹：发言留底（含纯图）、
// 判定流水或成员画像。三者皆无才算「查无此人」—— /check 对这种目标不再
// 请求模型（见 HandleAdCommand）。
//
// 查询出错时按「有数据」处理：宁可多花一次模型调用，也不能把真实用户
// 误报成查无此人。
func hasUserData(s *store.Store, chatID, uid int64) bool {
	var n int
	err := s.Read.QueryRow(`SELECT
		(SELECT COUNT(*) FROM group_messages WHERE chat_id=? AND user_id=?)
	  + (SELECT COUNT(*) FROM antiad_log     WHERE chat_id=? AND user_id=?)
	  + (SELECT COUNT(*) FROM group_members  WHERE chat_id=? AND user_id=?)`,
		chatID, uid, chatID, uid, chatID, uid).Scan(&n)
	if err != nil {
		slog.Error("反广告：查用户数据失败", "chat", chatID, "uid", uid, "err", err)
		return true
	}
	return n > 0
}

// handleMyChatMemberUpdate 监控 bot 自身在群里的状态。
//
// 反广告有一个代码解决不了的前提：bot 必须是群管理员，否则 Telegram
// 的 privacy mode 会让它只收得到命令消息，整条链路静默失效。被加进群
// 却没给权限是最常见的部署事故，这里主动告警，不让它无声无息。
func HandleMyChatMemberUpdate(b *core.Bot, cu *tg.ChatMemberUpdated) {
	if cu == nil || cu.Chat == nil || cu.NewChatMember == nil {
		return
	}
	if cu.Chat.Type == "private" {
		return
	}
	status := cu.NewChatMember.Status
	if !isMemberStatus(status) {
		return
	}
	if status == "administrator" || status == "creator" {
		// 刚拿到管理员权限：顺手补全这个群的历史成员入群时间（Bot API 没有
		// 这个字段，只能起一个 bot 会话走 MTProto，见 joinbackfill.go）。
		// 只在「从非管理员变成管理员」时触发：改权限、置顶也会推这条更新，
		// 每次都跑一遍没有意义（另有 24 小时冷却兜底）。
		old := ""
		if cu.OldChatMember != nil {
			old = cu.OldChatMember.Status
		}
		if old != "administrator" && old != "creator" {
			// 以前这里会自动跑一遍全群补全，现在改成按需：判定、复查、
			// /jtime 用到谁就实时查谁（见 ResolveJoinTime）。要预热整群
			// 时走 Mini App 的「补全历史入群时间」按钮。
			slog.Info("反广告：bot 获得管理员权限（入群时间按需实时查询）",
				"chat", cu.Chat.ID, "from", old)
		}
		return
	}
	// 只看总开关，不要求该群已在白名单里。部署顺序是「先把 bot 加进群
	// → 再在面板添加 chat_id」，用 antiadActive 把关的话，进群那一刻
	// 白名单里还没有它，这条告警就永远发不出去 —— 只剩「已配置的群里
	// bot 被降级」一种情况会响，而那恰恰不是最常见的事故。
	// 总开关仍要判：关着时 bot 可能还在一堆无关群里，每进一个群都私聊是纯噪音。
	if b.Cache.Snap().SettingInt("antiad_enabled", 0) != 1 {
		return
	}
	title := cu.Chat.Title
	if title == "" {
		title = "（无标题）"
	}
	msg := fmt.Sprintf("⚠️ <b>反广告无法在该群工作</b>\n\n群组: %s (<code>%d</code>)\n"+
		"当前身份: %s\n\n请把 bot 设为群管理员并授予<b>删除消息</b>与<b>封禁用户</b>权限，"+
		"否则它收不到群内普通消息。",
		html.EscapeString(title), cu.Chat.ID, status)
	for _, admin := range b.AlertTargets() {
		b.Send(admin, msg, nil)
	}
}
