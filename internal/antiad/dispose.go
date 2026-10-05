package antiad

import (
	"encoding/json"
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

// ---- 复判期的临时禁言：谁上的、什么时候上的 ----
//
// 终判不罚时要主动解掉它（见 actOnVerdict）：它只是复判期间的占位，
// 让一个已经被判为正常的人白等 5 分钟发不了言，代价比漏判一条同类
// 消息大。存内存即可 —— 复判一结束就在同一条路径上处理，进程重启后
// 最多再挂几分钟自己到期。

func tempMuteKey(chatID, uid int64) string {
	return fmt.Sprintf("%d:%d", chatID, uid)
}

// NoteTempMute 记下这次临时禁言（dryrun 下不曾真的禁言，不要记）。
func NoteTempMute(sh *core.Shared, chatID, uid int64) {
	if uid > 0 {
		cachesOf(sh).tempMutes.Set(tempMuteKey(chatID, uid), struct{}{}, tempMute)
	}
}

// LiftTempMute 解除复判期的临时禁言，返回是否真的解了。
//
// 同一人在本群另有未解除的处罚时不动手：那种禁言是他另一条消息挣来的，
// 主动解禁会把它一起踩掉。
func LiftTempMute(b *core.Bot, chatID, uid int64) bool {
	if uid <= 0 {
		return false
	}
	if penaltyRowsActive(b.Shared, b.BotID(), chatID, uid) {
		slog.Info("反广告：临时禁言保留（本群另有生效处罚）", "chat", chatID, "uid", uid)
		return false
	}
	ok, desc := Unmute(b, chatID, uid)
	if !ok {
		slog.Warn("反广告：解除临时禁言失败", "chat", chatID, "uid", uid, "tg_error", desc)
		return false
	}
	cachesOf(b.Shared).tempMutes.Delete(tempMuteKey(chatID, uid))
	slog.Info("反广告：复判正常，已解除临时禁言", "chat", chatID, "uid", uid)
	return true
}

// LiftTempMuteIfFresh 只在确实是复判期的临时禁言时才解：进程里有标记、
// 且没超过临时禁言时长。人工复查（/check）走这一支 —— 复查时那个人可能
// 正被另一条消息的正式处罚禁着，那种不该动。
func LiftTempMuteIfFresh(b *core.Bot, chatID, uid int64) bool {
	if _, ok := cachesOf(b.Shared).tempMutes.Get(tempMuteKey(chatID, uid)); !ok {
		return false
	}
	return LiftTempMute(b, chatID, uid)
}

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
	// 短禁言也是禁言，同样要过禁言置信度下限：低置信的结论只删不禁，
	// 没必要让一个可能清白的人 5 分钟发不了言。默认档位本来就 ≥75%，
	// 这条只在「按模型结论定档」把低置信结论放进删除档时才起作用。
	if act.Name == "deleted" &&
		snap.BotSettingInt(b.BotID(), "antiad_short_mute", 0) == 1 &&
		!belowMuteConf(b, snap, v) {
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

// deleteMessageGone 报告 deleteMessage 的失败是不是「消息已经不在了」。
// Telegram 对已删除或从未存在的消息回 “message to delete not found”；
// 个别反代与旧版网关回 MESSAGE_ID_INVALID。两者都不是故障：这次处置的
// 目标就是让这条消息消失，它本来已经不在了。
func deleteMessageGone(desc string) bool {
	d := strings.ToLower(desc)
	return strings.Contains(d, "message to delete not found") ||
		strings.Contains(d, "message_id_invalid")
}

// DeleteMessage 删一条消息，并把「消息已经不在了」当成功。
//
// 管理员手快、另一个 bot 先删、消息随账号一起消失时，再报「删除失败」
// 只会给管理员一个点了也没用的补删按钮，还会在汇总里标一个假 ⚠️。
func DeleteMessage(b *core.Bot, chatID, msgID int64) (bool, string) {
	ok, desc := b.CallOK("deleteMessage", map[string]any{
		"chat_id": chatID, "message_id": msgID})
	if !ok && deleteMessageGone(desc) {
		slog.Info("反广告：消息已不存在，删除跳过", "chat", chatID, "msg", msgID)
		return true, ""
	}
	return ok, desc
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
		if ok, desc := DeleteMessage(b, m.Chat.ID, m.MessageID); !ok {
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
		ids, qerr := gmsgIDs(b, `chat_id=? AND user_id=? AND at > ?`,
			m.Chat.ID, m.From.ID, time.Now().Add(-purgeWindow).Unix())
		if qerr != nil {
			// 查询失败时 ids 为空，若照旧调 deleteMessages 会返回成功，
			// 流水里还会写「已连带删除此人近期全部消息」—— 一次静默的假成功。
			notes = append(notes, "连带删除失败: 读取留底出错")
		} else if ok, desc := deleteMessages(b, m.Chat.ID, ids); !ok {
			notes = append(notes, "连带删除失败: "+desc)
		}
	}
	if act.Mute {
		minutes := b.Cache.Snap().BotSettingInt(b.BotID(), "antiad_mute_minutes", 1440)
		d := time.Duration(minutes) * time.Minute
		if act.Temp {
			d = tempMute
		}
		if s := muteOrExplain(b, m.Chat.ID, m.From.ID, d); s != "" {
			notes = append(notes, s)
		}
	}
	// 仅删除档的短禁言。频道身份（负 ID）跳过：MuteSender 对频道走的是
	// banChatSenderChat，那是永久封频道，比这一档该有的分量重得多。
	if act.Short && m.From.ID > 0 {
		if s := muteOrExplain(b, m.Chat.ID, m.From.ID, tempMute); s != "" {
			notes = append(notes, s)
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

// participantGoneDesc 报告禁言失败的错误文本是否直接说明对方不在群里。
// 这两种描述出现时 getChatMember 也查不到有效成员，能省一次 TG 往返。
func participantGoneDesc(desc string) bool {
	d := strings.ToLower(desc)
	return strings.Contains(d, "participant_id_invalid") ||
		strings.Contains(d, "user_not_participant")
}

// MuteMoot 在禁言失败后做二次判断：这次失败是不是因为「已经不用禁了」——
// 对方已经退群 / 被踢 / 被封禁出群，或已经处于发不出言的状态。
// 返回给管理员看的说明；空串表示确实失败，调用方照常上报「禁言失败」。
//
// getChatMember 查不成时按「确实失败」处理：宁可多报一次失败，也不能
// 因为一次 TG 抖动把没禁上的人显示成已处理。
func MuteMoot(b *core.Bot, chatID, uid int64, desc string) string {
	if uid <= 0 {
		return "" // 频道身份没有成员状态可查
	}
	if participantGoneDesc(desc) {
		slog.Info("反广告：禁言跳过（对方已不在群/已被封禁出群）",
			"chat", chatID, "uid", uid)
		return "对方已不在群里（可能已被封禁出群）"
	}
	raw, err := b.TG.Call("getChatMember", map[string]any{
		"chat_id": chatID, "user_id": uid,
	})
	if err != nil {
		return ""
	}
	var resp tg.ChatMemberResp
	if json.Unmarshal(raw, &resp) != nil || !resp.OK {
		return ""
	}
	switch resp.Result.Status {
	case "left":
		slog.Info("反广告：禁言跳过（对方已退群）", "chat", chatID, "uid", uid)
		return "对方已退群"
	case "kicked":
		slog.Info("反广告：禁言跳过（对方已被封禁出群）", "chat", chatID, "uid", uid)
		return "对方已被封禁出群"
	case "restricted":
		if resp.Result.IsMember != nil && !*resp.Result.IsMember {
			slog.Info("反广告：禁言跳过（对方已不在群）", "chat", chatID, "uid", uid)
			return "对方已不在群里"
		}
		if resp.Result.CanSendMessages != nil && !*resp.Result.CanSendMessages {
			slog.Info("反广告：禁言跳过（对方已处于禁言状态）",
				"chat", chatID, "uid", uid)
			return "对方已处于禁言状态"
		}
	}
	return ""
}

// muteOrExplain 禁言并在失败时给出说明：成功返回空串；失败但已经不用禁
// 返回「无需禁言：…」；其余按「禁言失败」上报。告警按钮只认失败前缀来
// 放回补刀入口（见 adAlertRows），两种结果的措辞必须分开。
func muteOrExplain(b *core.Bot, chatID, uid int64, d time.Duration) string {
	ok, desc := MuteSender(b, chatID, uid, d)
	if ok {
		return ""
	}
	if why := MuteMoot(b, chatID, uid, desc); why != "" {
		return "无需禁言：" + why
	}
	return noteMuteFailed + ": " + desc
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
	cachesOf(b.Shared).doomedAlbums.Set(albumKey(m.Chat.ID, m.MediaGroupID), struct{}{}, albumDoomTTL)
	ids, err := gmsgIDs(b, `chat_id=? AND user_id=? AND media_group=?`,
		m.Chat.ID, m.From.ID, m.MediaGroupID)
	if err != nil {
		return false, "读取相册留底失败"
	}
	return deleteMessages(b, m.Chat.ID, ids)
}

func albumDoomed(b *core.Bot, chatID int64, album string) bool {
	_, ok := cachesOf(b.Shared).doomedAlbums.Get(albumKey(chatID, album))
	return ok
}

// gmsgIDs 按条件取留底里的消息 ID。where 只来自本包的字面量。
// 查询失败返回错误：调用方必须把「一条都没取到」与「查询失败」分开，
// 后者当成空列表会让连带删除静默地什么都不做却报成功。
func gmsgIDs(b *core.Bot, where string, args ...any) ([]int64, error) {
	rows, err := b.Store.Read.Query(`SELECT message_id FROM group_messages WHERE `+where, args...)
	if err != nil {
		slog.Error("反广告：读取留底 ID 失败", "err", err)
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		slog.Error("反广告：读取留底 ID 游标出错", "err", err)
		return ids, err
	}
	return ids, nil
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

// gbanSevereLine 是联合封禁的第二条证据线：危害度达到它就算够格，即使置信度
// 没过硬线（按模型结论定档时置信度不参与档位）。用固定常量而非某个设置项：
// 全平台封禁的门槛不该跟着展示向的开关（如短撤回阈值 antiad_alert_severe）
// 漂移 —— 把那个开关调成 0 会让门槛凭空消失。危害度的语义见 severeAd。
const gbanSevereLine = 2.0

// gbanWorthy 报告这次判定够不够格进联合封禁名单。
//
// 联合封禁会把人从**所有**接入群一起请出去，比单群禁言重得多，所以除了
// 「是广告 + 要罚」之外还要一条**独立的证据线**：按置信度分档时「禁言档」
// 本身就意味着过了处置线；按模型结论定档（antiad_bool_verdict）时置信度
// 不参与档位，必须把这条线单独补回来——否则 62% 的误报也会把人全平台封掉。
func gbanWorthy(b *core.Bot, snap *store.Snapshot, v adVerdict) bool {
	hard := float64(snap.BotSettingInt(b.BotID(), "antiad_act_hard", 90))
	confHigh := v.Confidence*100 >= hard
	severe := v.Severity >= gbanSevereLine
	if !confHigh && !severe {
		return false
	}
	// 账号级结论（「资料本身就是广告位」）再抬一道：它的证据全在昵称、
	// 用户名、简介上，靠的是关键词与形态归纳 —— 最容易误杀（实测：简介里
	// 写「双向机器人」被判成色情推广，人直接进了全平台名单）。名单是全平台
	// 的封禁，误伤一个正常用户的代价远大于漏掉一个广告号；这一档只认真有
	// 把握的。
	if v.Scope == "account" {
		return confHigh && severe
	}
	return true
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
	// 初判删过的不再删；临时禁言由这里的正式处罚接替。终判不罚时把那条
	// 临时禁言主动解掉：它只是复判期间的占位，让人白等 5 分钟发不了言
	// 比漏判一条同类消息更糟（同一人另有生效处罚时 LiftTempMute 会跳过，
	// 那种禁言是他另一条消息挣来的）。
	todo := act
	todo.Delete = act.Delete && !pre.Delete
	note := joinNotes(preNote, ApplyAction(b, m, todo, dryrun))
	if pre.Mute && pre.Temp && !v.IsAd && !dryrun &&
		!todo.Mute && !todo.Ban && !act.Short {
		if LiftTempMute(b, m.Chat.ID, m.From.ID) {
			note = joinNotes(note, "复判正常，已解除临时禁言")
		}
	}
	// 复判确认「只有资料可疑、正文没问题」时给资料一个临时放行：资料是长期
	// 存在的东西，不给的话他之后每发一条消息都会被同一份资料拖进删除。
	if !dryrun && !v.IsAd && v.ProfileOKHours > 0 {
		if GrantProfileOK(b, profile, v.ProfileOKHours, "复判放行："+v.Reason) > 0 {
			note = joinNotes(note, profileOKNote(clampProfileHours(v.ProfileOKHours)))
		}
	}
	// 反过来：这条判成「账号本身就是广告号」时，之前的资料放行作废 ——
	// 那份资料重新成了广告证据，不该再挡着后续判定。
	if !dryrun && v.IsAd && v.Scope == "account" {
		DropProfileOK(b, m.From.ID, "资料又判为广告号")
	}
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
