package antiad

import (
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// adReviewLimit 是一次复查送检的留底条数上限。
// 全部历史一次性给模型是这条命令的意义所在，但仍要有个天花板：
// 一个刷了几千条的号会把单次请求撑成天价。
const adReviewLimit = 60

const adCmdUsage = "用法：回复某人的消息发 <code>/check</code>，" +
	"或直接发 <code>/check &lt;user_id&gt;</code> / <code>/check @用户名</code>" +
	"（频道填 -100 开头的频道 ID）。\n" +
	"会把该用户在本群的全部留底一次性交给两个模型复查；" +
	"数据库里查无此人时直接回复「未有该用户数据」，不发模型请求；" +
	"只有入群记录、还没发过言的按进群资料复查。"

// parseAdCommand 识别 /check、/ban、/white、/uad、/jtime 并取出命令名与参数。
//
// 群里 TG 客户端会自动补成 /check@botname，必须一并认。
// 几条命令合并识别，因为 /ban、/white 以 /check 为前缀 —— 分开写的话，
// 先匹配 /check 的那一方会把它们也吃掉。
func parseAdCommand(text string) (cmd, arg string, ok bool) {
	f := strings.Fields(text)
	if len(f) == 0 {
		return "", "", false
	}
	head := f[0]
	if i := strings.Index(head, "@"); i > 0 {
		head = head[:i]
	}
	switch head {
	case "/check", "/ban", "/banad", "/white", "/uad", "/ungban", "/jtime":
		return head, strings.Join(f[1:], " "), true
	}
	return "", "", false
}

// handleAdCommand 处理群内 /check 复查。
//
// 命令对所有人开放（它不直接封禁，走的是与自动判定同一套处置矩阵），
// 因此它本身就是一个花钱入口：一次复查跑两个模型、带上目标全部历史。
// 必须按发起人限频，否则任何成员都能靠刷命令烧钱。
func HandleAdCommand(b *core.Bot, conf store.BotChat, m *tg.Message, arg string) {
	snap := b.Cache.Snap()
	ttl := time.Duration(snap.BotSettingInt(b.BotID(), "antiad_alert_ttl", 300)) * time.Second
	reply := func(text string) { groupNotice(b, conf.ChatID, text, nil, ttl) }

	if !b.AdLimits.Allow(fmt.Sprintf("ad:cmd:%d", m.From.ID),
		snap.BotSettingInt(b.BotID(), "antiad_cmd_rpm", 3)) {
		reply("复查太频繁，请稍后再试。")
		return
	}
	// 命令本身立即删除，usage/报错与复查卡片按 antiad_alert_ttl 延迟撤回
	// （与 /jtime、/uad 一致）：/check 对全群开放，命令和卡片留在群里
	// 只会引人去刷，结论看过就该退场。
	b.TG.Call("deleteMessage", map[string]any{
		"chat_id": conf.ChatID, "message_id": m.MessageID})

	var target *tg.TGUser
	var targetMsgID int64
	switch {
	case m.ReplyToMessage != nil && m.ReplyToMessage.From != nil:
		target = senderOf(m.ReplyToMessage)
		targetMsgID = m.ReplyToMessage.MessageID
	case arg != "":
		// 负数是频道 ID（以频道身份发言的记录就记在频道名下）。
		uid, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
		if err != nil || uid == 0 {
			// @username：群成员记住的往往是用户名而不是数字 ID。
			if name, ok := strings.CutPrefix(strings.TrimSpace(arg), "@"); ok {
				var found bool
				if uid, found = resolveUsernameID(b, name); !found {
					reply("查不到这个用户名，请改用 user_id。" +
						"（私有用户名查不到）")
					return
				}
			} else {
				reply(adCmdUsage)
				return
			}
		}
		target = &tg.TGUser{ID: uid}
	default:
		reply(adCmdUsage)
		return
	}

	// 复查看全部历史：判过、罚过的消息也是这个账号的证据（是否收回
	// 是申诉流程的事），不按 punished 过滤。
	hist := loadUserMessages(b.Store, m.Chat.ID, target.ID, adReviewLimit, false)
	if len(hist) == 0 {
		// 数据库里连这个人的任何痕迹都没有（发言留底、判定流水、入群画像
		// 三样皆无）时直接给结论，不请求模型：随机 user_id 查资料既花钱，
		// 模型也给不出可信结论（线上真实反馈）。
		if !hasUserData(b.Store, m.Chat.ID, target.ID) {
			notice := fmt.Sprintf("🔎 <b>复查结果</b>\n未有该用户数据：<code>%d</code> "+
				"在本群没有发言留底、判定流水或入群记录，未做判定。", target.ID)
			scheduleAlertCleanup(b, m.Chat.ID,
				b.SendGetIDNoPreview(m.Chat.ID, notice, nil),
				time.Duration(snap.BotSettingInt(b.BotID(), "antiad_alert_ttl", 300))*time.Second)
			return
		}
		// 有入群画像、只是还没发过言（刚进群的人）：这类人能看的只有资料，
		// 走冷判定那套提示词与采信线。
		p := newCheckProgress(b, m.Chat.ID, target.ID)
		if !b.AdSubmit(func() { reviewProfileOnly(b, snap, conf, target, p) }) {
			p.busy(alertTTLOf(b, snap))
		}
		return
	}

	// 构造一条「代表消息」：处置要作用在具体消息上（删除），
	// 而 /check <user_id> 这一路没有具体消息，此时 MessageID 为 0，
	// 下面会把删除动作摘掉。
	// 代表消息的正文：回复形态下用被回复的那一条，否则用最新一条留底。
	// 两者不一致会让告警展示的原文与「删除」按钮实际作用的消息对不上。
	repText := hist[len(hist)-1].Text
	if m.ReplyToMessage != nil {
		if t := displayText(m.ReplyToMessage); t != "" {
			repText = t
		}
	}
	tgt := &tg.Message{Chat: m.Chat, From: target, MessageID: targetMsgID, Text: repText}
	gm, ok := loadMember(b.Store, m.Chat.ID, target.ID)
	if !ok {
		// 没有画像行（比如目标只发过言、画像还没建，或清理过）时不能直接
		// 放弃复查：留底已经取到了，用留底条数当发言数，年龄按未知处理
		// （AgeKnown=false，isNewbie 不拿年龄轴加重怀疑）。
		slog.Warn("反广告：复查目标没有画像行，按留底条数估算",
			"chat", m.Chat.ID, "uid", target.ID)
		gm = groupMember{ChatID: m.Chat.ID, UserID: target.ID,
			MsgCount: int64(len(hist)), Known: true}
	}
	profile := buildProfile(b, tgt, gm, time.Now().Unix())
	state := buildState(b, snap, tgt, profile)
	state.ReviewHistory = make([]core.CtxMsg, 0, len(hist))
	for _, h := range hist {
		// 群内引用是别人的话，复查的是这个账号本人说了什么：剥掉引用段，
		// 免得「引用广告提醒管理员」在他的历史里看起来像他自己在发广告。
		t := stripQuotedTail(h.Text)
		if strings.TrimSpace(t) == "" {
			continue
		}
		state.ReviewHistory = append(state.ReviewHistory,
			core.CtxMsg{Name: senderName(target), Text: t, At: h.At})
	}

	// 多步响应先发「当前状态」，之后每完成一级编辑一次（见 check_progress.go）。
	p := newCheckProgress(b, m.Chat.ID, target.ID)
	if !b.AdSubmit(func() {
		// 复查同样按需补入群时间：worker 上查，不拖更新处理；负缓存清掉，
		// 给一次全新的实时查询。
		ensureJoinAgeFresh(b, m.Chat.ID, target.ID, &state.Sender)
		reviewAndAct(b, snap, conf, tgt, profile, state, p)
	}) {
		p.busy(alertTTLOf(b, snap))
	}
}

// canMarkAd 报告此人能否用 /ban 直接标记广告。
//
// 比 /check 严格得多：/check 只是花钱跑一次判定，处置仍由矩阵决定；
// /ban 绕过判定直接删人禁言，对所有人开放等于把删消息的权力给了全群。
func canMarkAd(b *core.Bot, chatID, uid int64) bool {
	if b.IsMain(uid) || uid == b.Owner() {
		return true
	}
	return IsChatAdmin(b, chatID, uid)
}

const banadCmdUsage = "用法：<b>回复</b>要标记的那条消息发送 <code>/banad</code>，" +
	"或直接发 <code>/banad &lt;user_id&gt;</code> / <code>/banad @用户名</code>" +
	"（按该用户最新一条留底处置；频道填 -100 开头的频道 ID）。\n" +
	"会直接按最高档处置（删除 + 禁言/封禁），不经过 AI 判定；" +
	"结果进正例池并参与联合封禁。"

// resolveCmdTarget 解析 /ban、/banad 这类命令的目标：回复形态取被回复的
// 实际发言者；参数形态支持 user_id / @用户名，并按「此人最新一条留底」
// 做一张可处置的合成消息（删除与流水都指向真实消息；没有留底时只罚人，
// MessageID 为 0 时调用方会摘掉删除动作）。
func resolveCmdTarget(b *core.Bot, chatID int64, m *tg.Message, arg string) (*tg.Message, bool) {
	if m.ReplyToMessage != nil && m.ReplyToMessage.From != nil {
		// 以频道身份或访客 bot 发的，处置要落在频道/召唤者身上。
		cp := *m.ReplyToMessage
		cp.From = asSender(m.ReplyToMessage).From
		return &cp, true
	}
	if strings.TrimSpace(arg) == "" {
		return nil, false
	}
	uid, ok := resolveUIDArg(b, arg)
	if !ok {
		return nil, false
	}
	t := &tg.Message{Chat: &tg.Chat{ID: chatID}, From: &tg.TGUser{ID: uid}}
	if msgID, text, ok := latestKept(b.Store, chatID, uid); ok {
		t.MessageID, t.Text = msgID, text
	}
	return t, true
}

const banCmdUsage = "用法：<b>回复</b>某人的消息发 <code>/ban</code>，" +
	"或发 <code>/ban &lt;user_id&gt;</code> / <code>/ban @用户名</code>。\n" +
	"直接把此人封禁出群，并留一条可撤销的处罚记录（告警上有「🔓 解封」）。\n" +
	"不删他的发言、不进样本池、不进联合封禁 —— 发广告请用 <code>/banad</code>。"

// HandleBanCommand 群管理员直接封禁一个人（纯封禁，不判广告）。
//
// 与 /banad（人工标记广告）分开：管理员有时只是要清一个捣乱的人，不该
// 顺带删掉他的发言、让形态摘要学错样本、更不该把他全平台联封。
func HandleBanCommand(b *core.Bot, conf store.BotChat, m *tg.Message, arg string) {
	if !canMarkAd(b, conf.ChatID, m.From.ID) {
		return
	}
	b.TG.Call("deleteMessage", map[string]any{
		"chat_id": conf.ChatID, "message_id": m.MessageID,
	})

	target, ok := resolveCmdTarget(b, conf.ChatID, m, arg)
	if !ok {
		groupNotice(b, conf.ChatID, banCmdUsage, nil, 30*time.Second)
		return
	}
	if target.From.ID == b.BotID() {
		// 同 /banad：回复 bot 自己的消息是常见手滑，静默会让人以为命令坏了。
		groupNotice(b, conf.ChatID, "这条是 bot 自己的消息，不能封 bot。"+
			"请<b>回复目标的消息</b>发送 /ban，或直接发 "+
			"<code>/ban &lt;user_id&gt;</code>。", nil, 30*time.Second)
		return
	}

	snap := b.Cache.Snap()
	if !b.AdLimits.Allow(fmt.Sprintf("ban:%d", m.From.ID),
		snap.BotSettingInt(b.BotID(), "antiad_cmd_rpm", 3)) {
		groupNotice(b, conf.ChatID, "操作太频繁，请稍后再试。", nil, 30*time.Second)
		return
	}

	// 只封禁：不删消息、不计 ad_hits、不进正例池、不联封 —— 这是治安动作，
	// 不是广告判定。真广告用 /banad。
	act := adAction{Ban: true, Name: "banned"}
	note := ApplyAction(b, target, act, conf.Dryrun)
	v := adVerdict{Decider: "manual-ban",
		Reason: fmt.Sprintf("由 %s (%d) 人工封禁", senderName(m.From), m.From.ID)}
	logID := logAd(b, target, v, logAction(act, conf.Dryrun), note)

	ttl := time.Duration(snap.BotSettingInt(b.BotID(), "antiad_alert_ttl", 300)) * time.Second
	text := "🚫 <b>已封禁</b> " + userLink(target.From.ID)
	if conf.Dryrun {
		text = "🧪 <b>演练：未真正封禁</b> " + userLink(target.From.ID)
	} else if note != "" {
		text += "\n<i>" + html.EscapeString(core.TruncateRunes(note, 120)) + "</i>"
	}
	groupNotice(b, conf.ChatID, text,
		tg.InlineKB([][2]string{{"🔓 解封", fmt.Sprintf("a:ad:rel:%d", logID)}}), ttl)
	slog.Info("反广告：管理员封禁", "chat", conf.ChatID, "uid", target.From.ID,
		"by", m.From.ID, "dryrun", conf.Dryrun)
}

// HandleBanAdCommand 人工把一条消息标记为广告并立即处置。
//
// 不发任何 AI 请求：人已经看明白了，再花一次钱去问模型没有意义。
// 代价是这条判定没有置信度可言，所以它只对群管理员及以上开放。
//
// 标记结果会进**正例池**（verdict='ad' 且 action != 'undone'），
// 形态总结下一轮就能学到它 —— 人工标记是质量最高的训练样本。
//
// 两种用法：回复某条消息（处置那条），或 /ban <user_id>（处置此人最新一条
// 留底；没有留底时只罚人、不删消息，仍落一条人工标记流水）。
func HandleBanAdCommand(b *core.Bot, conf store.BotChat, m *tg.Message, arg string) {
	// 非授权者静默忽略：回一句「你没有权限」等于告诉刷屏的人这条命令
	// 存在、值得去试。
	if !canMarkAd(b, conf.ChatID, m.From.ID) {
		return
	}
	// 命令本身不该留在群里。删不掉也不影响后续，忽略结果。
	b.TG.Call("deleteMessage", map[string]any{
		"chat_id": conf.ChatID, "message_id": m.MessageID,
	})

	target, ok := resolveCmdTarget(b, conf.ChatID, m, arg)
	if !ok {
		groupNotice(b, conf.ChatID, banadCmdUsage, nil, 30*time.Second)
		return
	}
	if target.From.ID == b.BotID() {
		// 管理员很自然地会去回复告警/复查结果卡片（里面带着原文），但那是
		// bot 自己的消息，标记它等于让 bot 自罚。这里不能静默 return：
		// 线上真实反馈是「命令没反应」，管理员只能改用别处入口。
		groupNotice(b, conf.ChatID, "这条是 bot 自己的消息，不能作为标记对象。"+
			"请<b>回复发广告的那条原消息</b>发送 /banad，或直接发 "+
			"<code>/banad &lt;user_id&gt;</code>。", nil, 30*time.Second)
		return
	}

	snap := b.Cache.Snap()
	if !b.AdLimits.Allow(fmt.Sprintf("adb:%d", m.From.ID),
		snap.BotSettingInt(b.BotID(), "antiad_cmd_rpm", 3)) {
		groupNotice(b, conf.ChatID, "操作太频繁，请稍后再试。", nil, 30*time.Second)
		return
	}

	v := adVerdict{
		IsAd: true, Confidence: 1, Kind: "manual",
		Decider: "manual",
		Reason: fmt.Sprintf("由 %s (%d) 人工标记为广告",
			senderName(m.From), m.From.ID),
	}
	// 人工标记直接取最高档：判断已经由人做出，不必再过阈值矩阵。
	// 禁言与否仍服从本群的处罚方式（可改为封禁）。
	act := withPunish(adAction{Delete: true, Mute: true, Alert: true, Name: "deleted_muted"},
		snap.BanMode(conf))
	// /ban <user_id> 而这个人没有留底时没有消息可删：摘掉删除动作，
	// 否则 deleteMessage 必然失败，告警里还会多一条假的失败说明。
	if target.MessageID == 0 {
		act.Delete = false
		if act.Name == "deleted_muted" {
			act.Name = "muted"
		} else if act.Name == "deleted_banned" {
			act.Name = "banned"
		}
	}

	note := ApplyAction(b, target, act, conf.Dryrun)
	logID := logAd(b, target, v, logAction(act, conf.Dryrun), note)

	if !conf.Dryrun {
		// 人工标记广告后，之前的资料放行作废：否则这份资料还能继续挡
		// 后续判定的资料一路，人工结论被一份过期的自动放行架空。
		DropProfileOK(b, target.From.ID, "人工标记为广告")
		BumpAdHits(b, conf.ChatID, target.From.ID, 1)
		maybeGban(b, conf.ChatID, target.From.ID,
			"人工标记："+core.TruncateRunes(displayText(target), 60))
	}

	text, kb := renderAdAlertBrief(b, target, v, act, note, logID, conf.Dryrun)
	groupNotice(b, conf.ChatID, "🖐 <b>人工标记</b>\n"+text, kb, time.Duration(snap.BotSettingInt(b.BotID(), "antiad_alert_ttl", 300))*time.Second)
}

// latestKept 取此人最新一条留底，不限有没有文字 —— 图片、贴纸广告也要能删。
func latestKept(s *store.Store, chatID, uid int64) (int64, string, bool) {
	var msgID int64
	var text string
	s.Read.QueryRow(`SELECT message_id,text FROM group_messages
		WHERE chat_id=? AND user_id=? ORDER BY at DESC, message_id DESC LIMIT 1`,
		chatID, uid).Scan(&msgID, &text)
	return msgID, text, msgID != 0
}

// resolveUIDArg 把命令参数换成 user_id：数字 ID 或 @用户名。
// 解析失败返回 false，调用方回用法说明。
func resolveUIDArg(b *core.Bot, arg string) (int64, bool) {
	arg = strings.TrimSpace(arg)
	if uid, err := strconv.ParseInt(arg, 10, 64); err == nil && uid != 0 {
		return uid, true
	}
	if name, ok := strings.CutPrefix(arg, "@"); ok {
		return resolveUsernameID(b, name)
	}
	return 0, false
}

// alertTTLOf 是群内复查卡片的存活时间（与告警同一配置）。
func alertTTLOf(b *core.Bot, snap *store.Snapshot) time.Duration {
	return time.Duration(snap.BotSettingInt(b.BotID(), "antiad_alert_ttl", 300)) * time.Second
}

// reviewAndAct 跑复查并按处置矩阵动作，结果逐步编辑进进度消息。
func reviewAndAct(b *core.Bot, snap *store.Snapshot, conf store.BotChat, tgt *tg.Message,
	profile senderProfile, state adState, p *checkProgress) {

	chatID := conf.ChatID
	// 复查绕过全部缓存：简介与资料挂链取最新，资料放行不套用
	// （见 enrichSenderFresh）—— 复查审的就是资料本身，不能被之前
	// 「缓存个人简介通过」挡成维持原判。
	enrichSenderFresh(b, &state.Sender)
	// 同上：定案与资料放行都用补全过的画像。
	profile = state.Sender
	ttl := alertTTLOf(b, snap)

	// ① 硬规则最先判：人工复查同样先过一遍（模型对冒用角色扮演会判正常，
	// 管理员复查这种人时不该被模型带偏）。
	if hit, where := leaderGateWorker(b, snap, conf, tgt, state.Sender); hit != "" {
		p.finish(leaderHitSection(hit, where, conf.Dryrun), nil, ttl)
		return
	}
	rules := "① <b>规则检查</b>：未命中。"
	if len(state.MatchedRules) > 0 {
		names := make([]string, 0, len(state.MatchedRules))
		for _, r := range state.MatchedRules {
			names = append(names, fmt.Sprintf("#%d %s", r.ID, r.Name))
		}
		rules += fmt.Sprintf("另有 %d 条非强制必封规则命中（%s），已作为强证据随两级模型复核。",
			len(state.MatchedRules), html.EscapeString(strings.Join(names, "、")))
	}
	p.step(rules)

	// ② 初判 → ③ 复判：每级跑完就把结论编辑进进度消息。
	v, err := judgeBoth(b, snap, state, soInstructions, llmSystemPrompt, p.judgeStep)
	if err != nil {
		slog.Warn("反广告：复查失败", "chat", chatID, "uid", tgt.From.ID, "err", err)
		p.finish("🔎 <b>复查结果</b>\n❌ 复查失败："+html.EscapeString(publicError(err)), nil, ttl)
		return
	}

	dryrun := conf.Dryrun
	act := planAction(b, snap, conf, isNewbie(b, snap, profile), v)
	if tgt.MessageID == 0 {
		// /check <user_id> 没有指向具体消息，删不了任何东西。
		// 不摘掉的话 deleteMessage 必然失败，告警里还会多一条假的失败说明。
		act.Delete = false
	}
	note := ApplyAction(b, tgt, act, dryrun)
	// 复查结论正常时，顺手把复判期那条还没来得及解的临时禁言解掉：
	// 管理员多半就是看到「判定正常却还禁着言」才来复查的。只有确实是
	// 我们刚上的临时禁言才会动（见 LiftTempMuteIfFresh），别人的正式
	// 处罚不会被踩掉。
	lifted := false
	if !v.IsAd && !dryrun && LiftTempMuteIfFresh(b, chatID, tgt.From.ID) {
		lifted = true
		note = joinNotes(note, "已解除临时禁言")
	}
	// 人工复查同样能给资料放行：管理员发起复查、复判确认资料没问题时，
	// 别让他之后又被同一份资料缠上。
	if !v.IsAd && !dryrun && v.ProfileOKHours > 0 {
		if GrantProfileOK(b, profile, v.ProfileOKHours, "复查放行："+v.Reason) > 0 {
			note = joinNotes(note, profileOKNote(clampProfileHours(v.ProfileOKHours)))
		}
	}
	if !dryrun && v.IsAd && v.Scope == "account" {
		DropProfileOK(b, tgt.From.ID, "复查判为账号广告号")
	}
	logID := logAd(b, tgt, v, logAction(act, dryrun), logNote(act, note, dryrun))
	if act.Name != "none" && !dryrun {
		BumpAdHits(b, chatID, tgt.From.ID, 1)
		if act.Mute || act.Ban {
			maybeGban(b, chatID, tgt.From.ID, "复查判定："+core.TruncateRunes(v.Reason, 80))
		}
	}

	// 结论正常时不能用告警那套渲染：🚫 与「点我申诉」是给被判成广告的人
	// 准备的，配在正常结论上只会让被复查的人以为自己又被罚了（实测群里
	// 看到「🚫 … 正常 92% + 点我申诉」都在问是不是误判）。广告结论照旧
	// 走告警渲染，同一个渠道不该有两种长度。
	var text string
	var kb map[string]any
	if v.IsAd {
		text, kb = renderAdAlertBrief(b, tgt, v, act, note, logID, dryrun)
	} else {
		text = renderReviewClean(b, tgt, v, lifted)
	}
	// 处置之后限制状态可能变了（这次上的禁言/封禁、顺手解掉的临时禁言）：
	// 与初始响应里的状态不一样才补一段，没变就不重复。
	status, linkRows := RestrictionStatusText(b.Shared, b.BotID(), tgt.From.ID)
	if status == "" {
		status = "✅ 当前没有生效中的限制。"
	}
	section := "🔎 <b>复查结果</b>\n" + text
	if status != p.status {
		section += "\n\n" + status
	}
	for _, row := range linkRows {
		kb = tg.KBAppend(kb, [][2]string{row})
	}
	p.finish(section, kb, ttl)
}

// resolveUsernameID 把 @username 换成 user_id。群成员记住的往往是用户名
// 而不是数字 ID；查不到（私有、打错、已注销）返回 false。
//
// getChat 要走一次 TG，所以只在 /check 这类人工命令上用，判定链路里
// 不做这种事。
func resolveUsernameID(b *core.Bot, name string) (int64, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, false
	}
	raw, err := b.TG.Call("getChat", map[string]any{"chat_id": "@" + name})
	if err != nil {
		return 0, false
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			ID int64 `json:"id"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &resp) != nil || !resp.OK || resp.Result.ID == 0 {
		return 0, false
	}
	return resp.Result.ID, true
}

// reviewProfileOnly 只看资料复查一个人（/check 没有留底时的分支）。
//
// 用的是进群冷判定那一套提示词与采信线：他还没在本群发过言，能看的只有
// 资料，而冷判定本来就是干这个的。命中且过了采信线就按进群限制处理，
// 与自动链路完全一致（含群内通知与申诉入口）。
//
// 判定编排与消息复查同一路（judgeBoth）：初判与复判都跑、互相印证，
// 每级跑完编辑一次进度消息。
func reviewProfileOnly(b *core.Bot, snap *store.Snapshot, conf store.BotChat,
	u *tg.TGUser, p *checkProgress) {

	chatID := conf.ChatID
	msg := &tg.Message{Chat: &tg.Chat{ID: chatID, Title: conf.Title}, From: u}
	gm, _ := loadMember(b.Store, chatID, u.ID)
	prof := buildProfile(b, msg, gm, time.Now().Unix())
	// 复查绕过全部缓存：简介与挂链取最新、资料放行不套用（见
	// enrichSenderFresh）—— 对方可能刚改过资料，且显式复查的对象就是
	// 资料本身。
	enrichSenderFresh(b, &prof)
	// 年龄轴要用的入群时间缺了时按需补一次（查到即入库，之后不再查）；
	// 「查不到」的负缓存同样清掉，给一次全新的实时查询。
	ensureJoinAgeFresh(b, chatID, u.ID, &prof)
	ttl := alertTTLOf(b, snap)

	// ① 硬规则（冒用国家领导人）同样最先判：资料命中直接封禁。
	if hit, where := leaderGateWorker(b, snap, conf, msg, prof); hit != "" {
		p.finish(leaderHitSection(hit, where, conf.Dryrun), nil, ttl)
		return
	}
	p.step("① <b>规则检查</b>：未命中。")

	st := adState{Chat: adChatInfo{ID: chatID, Title: conf.Title},
		Sender: prof, JoinCheck: true}
	st.KnownAdPatterns, st.KnownFalsePositives = splitDigest(snap.Setting("antiad_digest"))

	// ② 初判 → ③ 复判。
	v, err := judgeBoth(b, snap, st, coldInstructions, coldLLMPrompt, p.judgeStep)
	if err != nil {
		slog.Warn("反广告：资料复查失败", "chat", chatID, "uid", u.ID, "err", err)
		p.finish("🔎 <b>资料复查结果</b>\n❌ 复查失败："+html.EscapeString(publicError(err)), nil, ttl)
		return
	}
	// 采信线与进群冷判定同一根：资料证据比一条消息少得多。
	line := float64(snap.BotSettingInt(b.BotID(), "antiad_cold_conf", 85))
	if !v.IsAd || v.Confidence*100 < line {
		if !v.IsAd && v.ProfileOKHours > 0 && !conf.Dryrun {
			GrantProfileOK(b, prof, v.ProfileOKHours, "资料复查放行："+v.Reason)
		}
		// 与消息路径的正常结论同一份渲染：不带 🚫、不附申诉入口。
		p.finish("🔎 <b>资料复查结果</b>\n"+
			renderReviewClean(b, msg, v, false), nil, ttl)
		return
	}
	// 复查判成广告号：之前的资料放行作废 —— 那份资料重新成了广告证据。
	if !conf.Dryrun {
		DropProfileOK(b, u.ID, "资料复查判为广告号")
	}
	// 已经在进群类限制里：applyJoinMuteNotify 会 no-op，但管理员显式发起
	// 的复查不能一声不吭（线上反馈「命令像没生效」）。给一条说明收尾即可，
	// 不重复禁言、不重复流水。
	if _, ok := loadJoinMute(b.Store, chatID, u.ID); ok {
		p.finish("🔎 <b>资料复查结果</b>\n🔒 "+userLink(u.ID)+
			" 已在进群类限制中，未重复处置。", nil, ttl)
		return
	}
	// 处置与进群冷判定同档，但回执必须发：这是管理员显式发的命令，而群内
	// 展示默认关，靠 applyJoinMute 的自动通知会一条回执都没有，命令看起来
	// 像没生效（线上真实反馈）。
	applyJoinMuteNotify(b, conf, u, v, joinMuteSpec{
		Kind: kindProfile, Action: actionJoinMuted, Note: "资料复查",
		Body:     joinProfileText(u, prof.Bio, v),
		Reason:   "账号资料中含有推广或引流内容",
		Announce: true,
		Shape:    profileShape(prof),
	})
	if _, ok := loadJoinMute(b.Store, chatID, u.ID); ok {
		p.finish(fmt.Sprintf("🔎 <b>资料复查结果</b>\n🔒 %s 已按进群资料限制处置（%s，置信度 %.0f%%）。",
			userLink(u.ID), html.EscapeString(adKindLabel(v.Kind)), v.Confidence*100), nil, ttl)
	} else {
		p.finish("🔎 <b>资料复查结果</b>\n⚠️ 限制发言失败，详见流水与日志。", nil, ttl)
	}
}
