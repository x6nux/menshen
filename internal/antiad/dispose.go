package antiad

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// ---- 处置：动作组合、执行、定案 ----

// adAction 是一次处置的动作组合。
type adAction struct {
	Delete bool
	// Mute 与 Ban 互斥：禁言档按本群设置可改为封禁出群（见 planAction）。
	Mute bool
	Ban  bool
	// Short 是「仅删除」档附带的一记短时禁言（tempMute）。刻意不并入 Mute：
	// Mute 会触发大模型复判与复判前的临时禁言，而这一档不值得为它多花
	// 一次复判——短禁言只是堵住删完就接着发的空档。
	Short bool
	// Purge 是连带删除此人近期的全部消息（账号本身就是广告号时）。
	// 不单列动作名：流水里仍记 deleted_muted，另在理由里注明（见 logNote）。
	Purge bool
	// Temp 是复判前的临时禁言，时长 tempMute（见 judgeAndAct）。
	Temp  bool
	Alert bool
	Name  string // none / alerted / deleted / muted / deleted_muted / deleted_banned
}

// tempMute 是复判前临时禁言的时长。必须长过复判的最坏耗时（aiTotalBudget），
// 否则正式禁言还没落地它就到期了；TG 把不足 30 秒的当成永久。
// 给到 5 分钟：复判最坏 45 秒，但上游抖动时会重试，余量要留足。
const tempMute = 5 * time.Minute

// purgeWindow 是连带删除的时间窗：TG 只允许删 48 小时内的消息，留一小时余量。
const purgeWindow = 47 * time.Hour

// albumDoomTTL 是「这个相册已判成广告」的记忆时长，覆盖判定之后才到的那几张。
const albumDoomTTL = 10 * time.Minute

// 失败说明的前缀。写入方（ApplyAction）与判读方（adAlertKB、汇总）共用
// 同一份常量：告警要据此决定补刀按钮给不给，两处各写一份字面量的话，
// 改了文案就会静默丢掉按钮。
const (
	noteDeleteFailed = "删除失败"
	noteMuteFailed   = "禁言失败"
	noteBanFailed    = "封禁失败"
	// purgeNote 记进流水理由，标明这次连带删除了此人近期的全部消息（见 logNote）。
	purgeNote = "连带删除此人近期全部消息"
)

// planAction 按判定结论与本群设置定处置：禁言档在封禁模式下改为封禁出群；
// 「仅删除」档可选附一记短时禁言（antiad_short_mute）。
func planAction(b *core.Bot, snap *store.Snapshot, conf store.BotChat, newbie bool, v adVerdict) adAction {
	act := withPunish(decideAction(b, snap, newbie, v), snap.BanMode(conf))
	if act.Name == "deleted" &&
		snap.BotSettingInt(b.BotID(), "antiad_short_mute", 0) == 1 {
		act.Short = true
	}
	return act
}

// withPunish 把禁言档换成封禁（ban 为真时）。/ban 与自动判定共用。
func withPunish(act adAction, ban bool) adAction {
	if !ban || !act.Mute {
		return act
	}
	act.Mute, act.Ban = false, true
	act.Name = strings.Replace(act.Name, "muted", "banned", 1)
	return act
}

// ApplyAction 执行处置，返回失败说明（全部成功时为空）。
//
// dryrun 为真时完整跳过所有群内写操作：试运行期必须能看清 AI 会怎么判，
// 而又不真的动群里的人。
//
// 各动作互相独立：一个失败不得连带取消另一个，否则广告号会既留着消息
// 又不受任何限制。
func ApplyAction(b *core.Bot, m *tg.Message, act adAction, dryrun bool) string {
	if dryrun {
		return ""
	}
	var notes []string
	if act.Delete {
		if ok, desc := b.CallOK("deleteMessage", map[string]any{
			"chat_id": m.Chat.ID, "message_id": m.MessageID,
		}); !ok {
			notes = append(notes, noteDeleteFailed+": "+desc)
		}
		if m.MediaGroupID != "" {
			if ok, desc := deleteAlbum(b, m); !ok {
				notes = append(notes, "删除相册其余图片失败: "+desc)
			}
		}
	}
	if act.Purge {
		// 判定的那条上面已删过，再删一次 TG 会自动跳过，不必剔除。
		ids := gmsgIDs(b, `chat_id=? AND user_id=? AND at > ?`,
			m.Chat.ID, m.From.ID, time.Now().Add(-purgeWindow).Unix())
		if ok, desc := deleteMessages(b, m.Chat.ID, ids); !ok {
			notes = append(notes, "连带删除失败: "+desc)
		}
	}
	if act.Mute {
		minutes := b.Cache.Snap().BotSettingInt(b.BotID(), "antiad_mute_minutes", 1440)
		d := time.Duration(minutes) * time.Minute
		if act.Temp {
			d = tempMute
		}
		if ok, desc := MuteSender(b, m.Chat.ID, m.From.ID, d); !ok {
			notes = append(notes, noteMuteFailed+": "+desc)
		}
	}
	// 仅删除档的短禁言。频道身份（负 ID）跳过：MuteSender 对频道走的是
	// banChatSenderChat，那是永久封频道，比这一档该有的分量重得多。
	if act.Short && m.From.ID > 0 {
		if ok, desc := MuteSender(b, m.Chat.ID, m.From.ID, tempMute); !ok {
			notes = append(notes, noteMuteFailed+": "+desc)
		}
	}
	if act.Ban {
		if ok, desc := BanSender(b, m.Chat.ID, m.From.ID); !ok {
			notes = append(notes, noteBanFailed+": "+desc)
		}
	}
	return strings.Join(notes, "; ")
}

// MuteSender 限时禁言；d <= 0 表示永久禁言（不带 until_date 的
// restrictChatMember 就是无限期，人留在群里但发不了言）。
// 频道身份（负 ID）没有成员权限可改，只能 banChatSenderChat，且不支持限时。
func MuteSender(b *core.Bot, chatID, uid int64, d time.Duration) (bool, string) {
	if uid < 0 {
		return b.CallOK("banChatSenderChat", map[string]any{
			"chat_id": chatID, "sender_chat_id": uid})
	}
	payload := map[string]any{
		"chat_id": chatID, "user_id": uid,
		"permissions": MutedPermissions(),
	}
	if d > 0 {
		payload["until_date"] = time.Now().Add(d).Unix()
	}
	return b.CallOK("restrictChatMember", payload)
}

// BanSender 封禁出群（永久）。频道身份走 banChatSenderChat。
func BanSender(b *core.Bot, chatID, uid int64) (bool, string) {
	if uid < 0 {
		return b.CallOK("banChatSenderChat", map[string]any{
			"chat_id": chatID, "sender_chat_id": uid})
	}
	return b.CallOK("banChatMember", map[string]any{"chat_id": chatID, "user_id": uid})
}

// Unban 解除封禁。必须带 only_if_banned：不带的话 TG 的语义是「先踢出群再解封」，
// 对已经不在封禁状态的人等于把他踢出去。
func Unban(b *core.Bot, chatID, uid int64) (bool, string) {
	if uid < 0 {
		return b.CallOK("unbanChatSenderChat", map[string]any{
			"chat_id": chatID, "sender_chat_id": uid})
	}
	return b.CallOK("unbanChatMember", map[string]any{
		"chat_id": chatID, "user_id": uid, "only_if_banned": true})
}

func albumKey(chatID int64, album string) string { return fmt.Sprintf("%d:%s", chatID, album) }

// deleteAlbum 删掉 m 所在相册的其余消息，并记下这个相册：判定之后才到的也要删。
// 相册的配文只挂在其中一张上，只删判定的那条，其余图片（常常就是二维码、
// 联系方式截图）会留在群里。
func deleteAlbum(b *core.Bot, m *tg.Message) (bool, string) {
	b.DoomedAlbums.Store(albumKey(m.Chat.ID, m.MediaGroupID), time.Now().Add(albumDoomTTL))
	return deleteMessages(b, m.Chat.ID, gmsgIDs(b,
		`chat_id=? AND user_id=? AND media_group=?`, m.Chat.ID, m.From.ID, m.MediaGroupID))
}

func albumDoomed(b *core.Bot, chatID int64, album string) bool {
	v, ok := b.DoomedAlbums.Load(albumKey(chatID, album))
	return ok && time.Now().Before(v.(time.Time))
}

// GCDoomedAlbums 清理过期条目，防止 map 无限增长。
func GCDoomedAlbums(sh *core.Shared) {
	now := time.Now()
	sh.DoomedAlbums.Range(func(k, v any) bool {
		if now.After(v.(time.Time)) {
			sh.DoomedAlbums.Delete(k)
		}
		return true
	})
}

// gmsgIDs 按条件取留底里的消息 ID。where 只来自本包的字面量。
func gmsgIDs(b *core.Bot, where string, args ...any) []int64 {
	rows, err := b.Store.Read.Query(`SELECT message_id FROM group_messages WHERE `+where, args...)
	if err != nil {
		slog.Error("反广告：读取留底 ID 失败", "err", err)
		return nil
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// deleteMessages 批量删除，每次最多 100 条（TG 的上限），找不到的 TG 会自动跳过。
func deleteMessages(b *core.Bot, chatID int64, ids []int64) (bool, string) {
	for i := 0; i < len(ids); i += 100 {
		if ok, desc := b.CallOK("deleteMessages", map[string]any{
			"chat_id": chatID, "message_ids": ids[i:min(i+100, len(ids))],
		}); !ok {
			return false, desc
		}
	}
	return true, ""
}

// gbanWorthy 报告这次判定够不够格进联合封禁名单。
//
// 联合封禁会把人从**所有**接入群一起请出去，比单群禁言重得多，所以除了
// 「是广告 + 要罚」之外还要一条**独立的证据线**：按置信度分档时「禁言档」
// 本身就意味着过了处置线；按模型结论定档（antiad_bool_verdict）时置信度
// 不参与档位，必须把这条线单独补回来——否则 62% 的误报也会把人全平台封掉。
func gbanWorthy(b *core.Bot, snap *store.Snapshot, v adVerdict) bool {
	hard := float64(snap.BotSettingInt(b.BotID(), "antiad_act_hard", 90))
	severe := float64(snap.BotSettingInt(b.BotID(), "antiad_alert_severe", 2))
	return v.Confidence*100 >= hard || v.Severity >= severe
}

// logAction 是记进流水的动作名。演练期加 dryrun: 前缀：那些动作从未真实
// 发生，误判处理据此不去解禁。
func logAction(act adAction, dryrun bool) string {
	if dryrun && act.Name != "none" {
		return "dryrun:" + act.Name
	}
	return act.Name
}

// logNote 是记进流水的处置说明：失败说明之外注明连带删除，演练期写「本应」。
// 不进告警：告警把 note 当失败说明渲染。
func logNote(act adAction, note string, dryrun bool) string {
	if !act.Purge {
		return note
	}
	p := "已" + purgeNote
	if dryrun {
		p = "本应" + purgeNote
	}
	return joinNotes(p, note)
}

// firstNote 注明复判前先做了什么：终判不认的话，删掉的消息也回不来，得留痕。
func firstNote(pre adAction, dryrun bool) string {
	var did []string
	if pre.Delete {
		did = append(did, "删除消息")
	}
	if pre.Mute {
		did = append(did, fmt.Sprintf("临时禁言 %d 分钟", tempMute/time.Minute))
	}
	if len(did) == 0 {
		return ""
	}
	if dryrun {
		return "初判本应先行：" + strings.Join(did, " + ")
	}
	return "初判先行：" + strings.Join(did, " + ")
}

// joinNotes 用「; 」连起非空的几段说明。
func joinNotes(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "; ")
}

// actOnVerdict 按终判处置、落流水、记/撤内容哈希、发群内告警，返回流水 ID。
//
// pre 是复判前已经先做了的（见 judgeAndAct、hashHit），preNote 是它的失败说明；
// 没有复判时两者为零值。hashText 是这条的内容：消息级广告且要删的，
// 记成哈希（为空表示不记，如纯图）。
func actOnVerdict(b *core.Bot, snap *store.Snapshot, conf store.BotChat, m *tg.Message,
	profile senderProfile, v adVerdict, pre adAction, preNote, hashText string) int64 {

	// 演练是**每个群**各自的状态：新加的群先观察、老群已转正式。
	dryrun := conf.Dryrun
	act := planAction(b, snap, conf, isNewbie(b, snap, profile), v)
	// 当天的内容哈希命中原先一律「只删不罚」（后来者的资料与资历没判过，
	// 见 hashHit）。按模型结论定档时不再豁免：哈希命中的内容本身就是模型
	// 判过的广告，而这条豁免在复判上游超时（复判失败回退到 hash 结论）时
	// 会让「同一条广告换个号再发」变成只删不禁——线上真实发生过。
	if v.Decider == deciderHash &&
		snap.BotSettingInt(b.BotID(), "antiad_bool_verdict", 1) != 1 {
		act.Mute, act.Ban, act.Purge, act.Short = false, false, false, false
		if act.Delete {
			act.Name = "deleted"
		}
	}
	// 初判删过的不再删；临时禁言由这里的正式处罚接替，终判不罚就让它到期
	// ——主动解禁会踩掉同一人另一条消息刚上的禁言。
	todo := act
	todo.Delete = act.Delete && !pre.Delete
	note := joinNotes(preNote, ApplyAction(b, m, todo, dryrun))
	if act.Short {
		p := fmt.Sprintf("附短时禁言 %d 分钟", int(tempMute/time.Minute))
		if dryrun {
			p = "本应" + p
		}
		note = joinNotes(note, p)
	}
	if pre.Delete && !act.Delete {
		// 终判不删，先删掉的也回不来了：照实记成删过。
		act.Delete, act.Name = true, "deleted"
	}
	logID := logAd(b, m, v, logAction(act, dryrun),
		joinNotes(firstNote(pre, dryrun), logNote(act, note, dryrun)))

	// 演练期的判定不该污染真实画像：切回正式模式后，这些人的 prior_ad_hits
	// 应该还是干净的。复判判为正常的也不算。
	if v.IsAd && act.Name != "none" && !dryrun {
		BumpAdHits(b, m.Chat.ID, m.From.ID, 1)
		// 联合封禁只认证据够硬的：它会把人从所有接入群一起请出去，而
		// 按模型结论定档时档位不带置信度信息（见 gbanWorthy）。
		if (act.Mute || act.Ban) && gbanWorthy(b, snap, v) {
			maybeGban(b, m.Chat.ID, m.From.ID, "自动判定："+core.TruncateRunes(v.Reason, 80))
		}
	}
	// 演练群的判定没人核对过，不能据此在同一个 bot 的正式群里直接删。
	if hashText != "" && !dryrun && v.IsAd && v.Scope == "message" && act.Delete {
		rememberAdHash(b, hashText, v, logID)
	}
	if act.Alert {
		sendAdAlert(b, conf, m, v, act, note, logID, dryrun)
	}
	return logID
}
