package antiad

import (
	"fmt"
	"html"
	"log/slog"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
)

// ---- 用户资料卡（面板 /user 与 Mini App 用户页共用） ----
//
// 管理员查一个人时真正要看的四件事：这是谁（昵称/用户名/ID）、他的资料写了
// 什么（简介）、他在这里活跃到什么程度（留底/发言/命中/首见/最近）、以及
// 他被处理过哪些发言（默认只看处置过的，噪音少）。

// UserDossier 是一个人的资料与流水摘要。
type UserDossier struct {
	UID      int64
	Name     string // 昵称（TG 当前值，取不到时留空）
	Username string
	Bio      string

	Kept      int64 // 本 bot 名下的留底条数
	Msgs      int64 // 画像累计发言（group_members.msg_count 之和）
	Hits      int64 // 历史命中（ad_hits 之和）
	ChatCount int64 // 出现在本 bot 名下几个群
	FirstSeen int64
	LastMsg   int64

	Total     int64 // 判定记录总数
	Processed int64 // 其中被真正处置过的
}

// LoadUserDossier 读一个人的资料与摘要。
//
// 统计范围：工作 bot 只覆盖**自己名下的群**（多租户下不能把别人的群数据算
// 进来）；主 bot 是平台级视图 —— 它自己不入群、不判定，统计按全局算（所有
// bot 名下的群）。
func LoadUserDossier(sh *core.Shared, botID, uid int64) UserDossier {
	d := UserDossier{UID: uid}
	if uid == 0 {
		return d
	}
	snap := sh.Cache.Snap()

	// 画像统计：首见 / 最近发言 / 发言数 / 命中数 / 群数。
	chats := dossierChats(snap, botID)
	in, inArgs := inClause(chats)
	args := append([]any{uid}, inArgs...)
	sh.Store.Read.QueryRow(`SELECT COALESCE(MIN(first_seen),0),
		COALESCE(MAX(last_msg_at),0), COALESCE(SUM(msg_count),0),
		COALESCE(SUM(ad_hits),0), COUNT(*) FROM group_members
		WHERE user_id=? AND chat_id `+in, args...).
		Scan(&d.FirstSeen, &d.LastMsg, &d.Msgs, &d.Hits, &d.ChatCount)

	// 留底条数（判定与复查都靠它）。
	sh.Store.Read.QueryRow(`SELECT COUNT(*) FROM group_messages
		WHERE user_id=? AND chat_id `+in, args...).Scan(&d.Kept)

	// 判定流水：总数与处置数分开算，管理员一眼能看出「查过 vs 罚过」。
	scope, scopeArgs := logScope(snap, botID)
	sh.Store.Read.QueryRow(`SELECT COUNT(*) FROM antiad_log WHERE user_id=?`+scope,
		append([]any{uid}, scopeArgs...)...).Scan(&d.Total)
	sh.Store.Read.QueryRow(`SELECT COUNT(*) FROM antiad_log WHERE user_id=?`+scope+
		` AND `+ProcessedCond, append([]any{uid}, scopeArgs...)...).Scan(&d.Processed)
	return d
}

// dossierChats 返回资料统计覆盖的群：工作 bot 只算自己的，主 bot 算全局
// （所有 bot 名下已启用的群，同一个群被多个 bot 覆盖只算一次）。
func dossierChats(snap *store.Snapshot, botID int64) []int64 {
	if rec := snap.Bots[botID]; rec == nil || !rec.IsMain {
		out := make([]int64, 0, 8)
		for _, c := range snap.ChatsOf(botID) {
			out = append(out, c.ChatID)
		}
		return out
	}
	seen := map[int64]bool{}
	var out []int64
	for _, rec := range snap.Bots {
		for _, c := range snap.ChatsOf(rec.BotID) {
			if !c.Enabled || seen[c.ChatID] {
				continue
			}
			seen[c.ChatID] = true
			out = append(out, c.ChatID)
		}
	}
	return out
}

// logScope 是判定流水的过滤条件：主 bot 不过滤（平台级视图），工作 bot 只看
// 自己判的。
func logScope(snap *store.Snapshot, botID int64) (string, []any) {
	if rec := snap.Bots[botID]; rec != nil && rec.IsMain {
		return "", nil
	}
	return " AND bot_id=?", []any{botID}
}

// processedCond 是「被真正处置过」的判据：动作不是「未处置/未送检」，也不是
// 演练前缀（演练什么都没做）。默认列表按它过滤，噪音少。
const processedCond = `action NOT IN ('none','skipped') AND action NOT LIKE 'dryrun:%'`

// ProcessedCond 是 processedCond 的导出版，面板与 Mini App 的 SQL 直接用。
const ProcessedCond = processedCond

// UserLogRow 是一条判定记录的摘要行（列表用，正文不进列表）。
type UserLogRow struct {
	ID      int64
	ChatID  int64
	Verdict string
	Conf    float64
	Kind    string
	Action  string
	Reason  string
	Text    string
	At      int64
}

// LoadUserLogs 读某人的判定记录，按 id 倒序。
//
// processedOnly 为真时只给被处置过的（见 processedCond）。
func LoadUserLogs(sh *core.Shared, botID, uid int64, processedOnly bool,
	limit, offset int) []UserLogRow {

	snap := sh.Cache.Snap()
	scope, scopeArgs := logScope(snap, botID)
	where := `user_id=?` + scope
	args := append([]any{uid}, scopeArgs...)
	if processedOnly {
		where += ` AND ` + processedCond
	}
	args = append(args, limit, offset)
	rows, err := sh.Store.Read.Query(`SELECT id,chat_id,verdict,confidence,ad_kind,
		action,reason,text,created_at FROM antiad_log WHERE `+where+`
		ORDER BY id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		slog.Error("用户资料：读取判定记录失败", "uid", uid, "err", err)
		return nil
	}
	defer rows.Close()
	var out []UserLogRow
	for rows.Next() {
		var r UserLogRow
		if rows.Scan(&r.ID, &r.ChatID, &r.Verdict, &r.Conf, &r.Kind, &r.Action,
			&r.Reason, &r.Text, &r.At) == nil {
			out = append(out, r)
		}
	}
	return out
}

// UserProfile 取此人的 TG 资料（昵称、用户名、简介），带一小时缓存
// （见 userInfo）。取不到时返回空串，不报错：查不到资料不是故障。
func UserProfile(b *core.Bot, uid int64) (name, username, bio string) {
	info := userInfo(b, uid)
	name = info.firstName
	if info.lastName != "" {
		name = name + " " + info.lastName
	}
	return name, info.username, info.bio
}

// UserDossierText 把资料渲染成面板用的多行文本（HTML 已转义）。
func UserDossierText(b *core.Bot, d UserDossier, loc *time.Location) string {
	ts := func(unix int64) string {
		if unix <= 0 {
			return "—"
		}
		return time.Unix(unix, 0).In(loc).Format("2006-01-02 15:04")
	}
	var sb []byte
	add := func(format string, a ...any) {
		sb = append(sb, fmt.Sprintf(format, a...)...)
		sb = append(sb, '\n')
	}
	add("👤 <b>用户资料</b>")
	add("ID：<code>%d</code>", d.UID)
	if d.Username != "" {
		add("用户名：@%s", escText(d.Username))
	}
	if d.Name != "" {
		add("昵称：%s", escText(d.Name))
	} else {
		add("昵称：（查不到）")
	}
	if d.Bio != "" {
		add("个人简介：%s", escText(core.TruncateRunes(d.Bio, 200)))
	} else {
		add("个人简介：（空或查不到）")
	}
	add("发言：留底 %d 条 ｜ 画像累计 %d 条 ｜ 历史命中 %d 次", d.Kept, d.Msgs, d.Hits)
	add("群组：%d 个 ｜ 首见 %s ｜ 最近发言 %s", d.ChatCount, ts(d.FirstSeen), ts(d.LastMsg))
	add("判定：共 %d 条，其中被处置过 %d 条", d.Total, d.Processed)
	return string(sb)
}

// escText 是资料卡的 HTML 转义（昵称与简介是用户可控的）。
func escText(s string) string { return html.EscapeString(s) }
