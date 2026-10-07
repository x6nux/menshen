package antiad

import (
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"menshen/internal/billing"
	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// chatActive 报告反广告在该群对本 bot 是否生效，并带回该群的配置。
//
// 两道门都是纯内存判断，未生效时零 API 调用、零数据库访问——
// bot 可能还在一堆无关群里，一开开关就对所有群生效是不可接受的。
//
//   - antiad_enabled 是**全平台急停**，只有主管理员能动
//   - bot_chats 里那一行的 enabled 是这个 bot 在这个群的开关，归 owner 管
func chatActive(b *core.Bot, chatID int64) (store.BotChat, bool) {
	snap := b.Cache.Snap()
	if snap.SettingInt("antiad_enabled", 0) != 1 {
		return store.BotChat{}, false
	}
	c, ok := snap.ChatConf(b.BotID(), chatID)
	if !ok || !c.Enabled {
		return store.BotChat{}, false
	}
	return c, true
}

// HandleGroupMessage 是群消息的总入口，也接编辑过的消息（edited_message）。
func HandleGroupMessage(b *core.Bot, m *tg.Message) {
	if m.Chat == nil || m.From == nil {
		return
	}
	// 以频道身份发言、访客 bot 代发的，换成实际发言者：豁免、画像、留底、
	// 处置都落在他身上，删除仍作用于这条消息本身。bot 一律豁免对这两类
	// 不成立，否则 @ 几个广告 bot、或换成频道身份即可绕过检测。
	m = asSender(m)
	at := m.Date
	if at == 0 {
		at = time.Now().Unix()
	}

	// 入群 service 消息同理：先把入群时间记下来，再判这个群是否开启反广告 ——
	// bot 亲眼看到的入群是 MTProto 回查拿不到时的回退值。真正的进群处理
	// （通知配对、冷判定）在下面 active 检查之后继续走一遍。
	if len(m.NewChatMembers) > 0 {
		for _, nu := range m.NewChatMembers {
			if nu != nil {
				recordJoin(b, m.Chat.ID, nu.ID, at)
			}
		}
	}

	conf, active := chatActive(b, m.Chat.ID)
	if !active {
		return
	}
	// 标题只在添加群那一刻抓过；先加配置、后把 bot 拉进群的群标题是空的。
	// 收到消息说明 bot 现在能看到它了，按群限频补一次（失败下一条再试）。
	if conf.Title == "" && b.AdLimits.Allow(fmt.Sprintf("chat:title:%d", conf.ChatID), 1) {
		chatID := conf.ChatID
		b.AdSubmit(func() { b.RefreshChatTitle(chatID) })
	}
	edited := m.EditDate != 0

	// service 消息兜底：chat_member 在 bot 权限变动期间可能漏收，
	// 而谁刚进群是整个分档的基础，两条路都要接。
	// 进群按进群的人分给 bot，不按发这条 service 消息的人：
	// 管理员拉人进群时 from 是管理员。
	if len(m.NewChatMembers) > 0 {
		for _, nu := range m.NewChatMembers {
			if nu == nil {
				continue
			}
			// 与冷判定结果配对：命中禁言时这条入群提示也要删
			// （广告号的昵称会原样出现在里面），而谁先到都有可能。
			noteJoinNotice(b, m.Chat.ID, nu.ID, m.MessageID)
			if b.ClaimSender(m.Chat.ID, nu.ID, time.Now()) {
				onJoin(b, conf, nu, at)
			}
		}
		return // 入群 service 消息本身没有正文，不进判定
	}

	// 同群有几个 bot 时，这个人只归其中一个（见 core/shard.go）。必须排在
	// 画像计数之前：每个 bot 各计一次的话，新人的发言数会翻倍涨成老成员。
	if !b.ClaimSender(m.Chat.ID, m.From.ID, time.Now()) {
		return
	}

	// 资历累计必须先于无正文早退。纯图/贴纸同样是在群里活动的证据，
	// 不计的话这类成员 msg_count 永不增长，在 isNewbie 的发言数轴上
	// 永远算新人；而年龄轴在 joined_at 缺失时本就不参与判定，
	// 两条轴一起失效就等于老成员低风险对他们完全不成立。
	// 编辑不是新发言：计进资历的话，反复编辑一条就能把新人刷成老成员。
	var gm groupMember
	if edited {
		gm, _ = loadMember(b.Store, m.Chat.ID, m.From.ID)
	} else {
		gm = touchMember(b, m.Chat.ID, m.From.ID, at)
	}
	// 画像读不到：用留底条数估算，照常判定（见 profileFromKept），
	// 编辑路径最容易走到这里。
	if !gm.Known {
		gm = profileFromKept(b, m.Chat.ID, m.From.ID)
	}

	// 解禁码兑换：四条门槛全满足才截下这条消息（见 handleGroupRedeem），
	// 否则照常留底、照常判定。
	if handleGroupRedeem(b, conf, m) {
		return
	}

	// 命令不是群聊内容：不留底（留了会被送进下一次复查），也不进判定。
	// 取本人正文而不是 displayText，否则回复形态下被引用的原文会混进参数。
	// 命令只在发出时执行一次：编辑一条旧命令不该再执行一遍。
	if cmd, arg, ok := parseAdCommand(m.Text); ok {
		if !edited {
			switch cmd {
			case "/check":
				HandleAdCommand(b, conf, m, arg)
			case "/ban":
				HandleBanCommand(b, conf, m, arg)
			case "/banad":
				HandleBanAdCommand(b, conf, m, arg)
			case "/white":
				HandleAdwCommand(b, conf, m, arg)
			case "/uad":
				HandleUadCommand(b, conf, m, arg)
			case "/ungban":
				HandleUngbanCommand(b, conf, m, arg)
			case "/jtime":
				HandleJtimeCommand(b, conf, m, arg)
			}
		}
		return
	}

	// 联合封禁在发言路径同样生效（进群路径是 gbanGuard）：白名单优先，
	// 命中即禁言、删除本条，不再留底、不再送检。
	if GbanMessageGuard(b, conf, m.From, m.MessageID, gm.Whitelisted) {
		return
	}

	// 判空看的是本人正文与引用内容的合集，不能只看本人正文：
	// 规避形态的极限版是一个字都不发（只发贴纸），载荷全在引用块里。
	text := displayText(m)

	// TG 会因为 bot 用不到的字段变化推来编辑事件（官方文档所述）。
	// 正文没变就什么都不做，否则每次这类变动都要再花一次送检的钱。
	if edited && recordedText(b, m.Chat.ID, m.MessageID) == core.TruncateRunes(text, gmsgTextLimit) {
		return
	}

	// 留底先于一切判定分支。豁免者、被护栏拦下的、判定失败的都要留：
	// /check 复查与 recent_context 需要的恰恰是这些没被判过的消息。
	// 没有文字的（图片、贴纸）也留：判成广告号时要连带删掉此人近期的
	// 全部消息，靠的是这里的 ID。
	recordMessage(b, m.Chat.ID, m.MessageID, m.From.ID, text, at, m.MediaGroupID)

	// 相册里判定之后才到的那几张：整组已判成广告，到一张删一张。
	if m.MediaGroupID != "" && albumDoomed(b, m.Chat.ID, m.MediaGroupID) {
		markPunished(b, m.Chat.ID, m.MessageID)
		b.CallOK("deleteMessage", map[string]any{"chat_id": m.Chat.ID, "message_id": m.MessageID})
		return
	}

	snap := b.Cache.Snap()
	// 纯图/贴纸配了识图模型才判（识图在判定 worker 里做，见 judgeAndAct）。
	if _, seeable := visualOf(m); text == "" && !(seeable && visionOn(snap)) {
		return
	}

	if adExempt(b, snap, m.Chat.ID, m.From, gm.Whitelisted) {
		return
	}

	// 必封规则门：AI 从历史封禁里总结出的正则，零 AI 成本。排在豁免之后
	// （管理员与白名单不在此规则范围内）、一切护栏与送检之前。
	//
	// enforce 规则命中即按最高档处置，动作与人工标记广告同一档；非 enforce
	// 的命中不在这里处置 —— 它稍后走先删后判专线：立即删除 + 临时禁言，
	// 跳过 systemone，直接交大模型复判定案（见 buildState 的 matched_rules
	// 与 judgeAndAct）。命中计数对所有规则都记。
	if hits := MatchRules(snap, judgingText(m)); len(hits) > 0 {
		for _, r := range hits {
			BumpRuleHits(b.Shared, r.ID)
		}
		if r, ok := firstEnforcedRule(hits); ok {
			v := adVerdict{
				IsAd: true, Confidence: 1, Kind: r.Category,
				Decider: "rule:" + strconv.FormatInt(r.ID, 10),
				Reason:  fmt.Sprintf("命中必封规则《%s》", r.Name),
			}
			act := withPunish(adAction{Delete: true, Mute: true, Alert: true,
				Name: "deleted_muted"}, snap.BanMode(conf))
			note := ApplyAction(b, m, act, conf.Dryrun)
			// action 名在演练期由 logAction 加 dryrun: 前缀，与其余处置一致。
			logAd(b, m, v, logAction(act, conf.Dryrun), note)
			slog.Info("反广告：命中必封规则，按最高档处置", "chat", m.Chat.ID,
				"uid", m.From.ID, "rule", r.ID, "name", r.Name,
				"dryrun", conf.Dryrun)
			return
		}
	}

	profile := buildProfile(b, m, gm, at)
	// 硬规则前置：资料（昵称/用户名）或正文里出现国家领导人姓名就直接封禁
	// 出群，不送检 —— 模型对这类角色扮演内容判的是正常。
	// 排在豁免之后：管理员与白名单里的人不在此规则范围内。
	if leaderGate(b, conf, m, "") {
		return
	}
	// buildState 必须留在同步段：recent_context 读的是本人此前的留底，
	// 更新按到达顺序串行处理，此刻库里恰好是这条之前的全部发言。
	// 挪进判定 worker 的话，此人随后几条也可能已经落库，模型会把
	// 之后说的话当成上下文。
	state := buildState(b, snap, m, profile)

	// 必封规则（非强制）命中：不等 systemone，立即进判定 worker 先删后判
	// ——删除 + 临时禁言，大模型定案（复判正常会自动解除）。与哈希命中
	// 一样不占群送检额度：规则是经过全库零误封测试的高精度形态。
	if len(state.MatchedRules) > 0 {
		if !b.AdSubmit(func() { judgeAndAct(b, snap, conf, m, profile, state) }) {
			slog.Warn("反广告：规则命中但判定队列已满，未处置",
				"chat", m.Chat.ID, "uid", m.From.ID,
				"rules", matchedRuleIDs(state.MatchedRules))
			logAd(b, m, adVerdict{Reason: "命中必封规则，但判定队列已满"},
				"none", "判定队列已满，未处置")
		}
		return
	}

	// 同样的内容此前已判为消息级广告：不送检、不占本群送检额度，直接删，
	// 禁言交给复判模型——必须排在护栏之前。
	if text != "" {
		if h, ok := lookupAdHash(b, text); ok {
			if !b.AdSubmit(func() { hashHit(b, snap, conf, m, profile, state, h, text) }) {
				slog.Warn("反广告：判定队列已满，哈希命中未处置", "chat", m.Chat.ID, "uid", m.From.ID)
			}
			return
		}
	}

	if ok, why := adAllow(b, snap, m.Chat.ID, m.MessageID, m.EditDate,
		isNewbie(b, snap, profile)); !ok {
		// 护栏拦下的同样按放行处理，只是不消耗这次 AI 调用。
		slog.Info("反广告：护栏拦下，未送检", "chat", m.Chat.ID,
			"uid", m.From.ID, "why", why)
		// 重复投递的是已经处理过的同一条，不用再记；其余的要记：
		// 那是一条真实的消息被放过了，面板上必须看得见。
		if why != adDupMessage {
			logAd(b, m, adVerdict{Decider: adDeciderSkipped, Reason: why}, "none", "未送检")
		}
		return
	}

	// 前置号复核优先于普通消息判定：新成员的第一条短招呼不判这条
	// 消息是不是广告，而判这个账号是不是批量注册的广告前置号。
	// 候选条件很窄（首条、短、无链接），开关默认关。
	if prewarmCandidate(b, snap, gm, m, edited) {
		if !b.AdSubmit(func() { prewarmJudge(b, snap, conf, m, state) }) {
			slog.Warn("反广告：判定队列已满，前置号复核未跑", "chat", m.Chat.ID, "uid", m.From.ID)
			logAd(b, m, adVerdict{Reason: "判定队列已满"}, "none", "判定队列已满，未送检")
		}
		return
	}

	// 判定要发 1~2 次 AI 请求（带重试最坏几十秒），而更新处理是串行的。
	// 同步等在这里，上游一慢整个 bot 就停摆 —— 管理员连关闭反广告
	// 都点不动，而上游抖动恰恰是最需要关掉它的时刻。
	if !b.AdSubmit(func() { judgeAndAct(b, snap, conf, m, profile, state) }) {
		// 队列已满就放行这一条。与超时放行是同一个失败方向：判定链路
		// 不能反过来拖垮 bot 本身。
		slog.Warn("反广告：判定队列已满，本条放行", "chat", m.Chat.ID, "uid", m.From.ID)
		logAd(b, m, adVerdict{Reason: "判定队列已满"}, "none", "判定队列已满，未送检")
	}
}

// judgeAndAct 是判定与处置，跑在判定 worker 上。
//
// 先删后判：初判（systemone）一出结论就先动手——删消息、要罚的先临时禁言
// （tempMute），再把大模型复判投进**单独的**复判队列，定案后补正式处罚、
// 连带删除与告警。大模型再慢，广告也不会一直挂在群里，发广告的人也发不了
// 下一条。初判低于采信线、要罚、或命中了必封规则证据（matched_rules）时
// 才复判（needReview / forceReview）。
//
// 进程退出时不等待在飞的判定：它们最长几十秒，且并发有上限，全部只会
// 写自己的流水。store 关闭后写操作返回错误而非 panic（database/sql
// 的既有行为），最坏结果是日志里多几条写失败，不值得为此引入一整套
// 等待机制。
func judgeAndAct(b *core.Bot, snap *store.Snapshot, conf store.BotChat, m *tg.Message,
	profile senderProfile, state adState) {

	// 简介与简介里的链接要发 getChat，所以放在异步段取：同步段每多一次 TG
	// 往返，更新处理就多停一次。
	enrichSender(b, &state.Sender)
	// 入群时间未知时按需实时查一次：年龄轴决定老成员免禁言这类处置，
	// 缺了它老成员会被按新人处置。查询也在异步段，不拖更新处理。
	if m.Chat != nil {
		ensureJoinAge(b, m.Chat.ID, &state.Sender)
	}
	// 定案用的是同一份补全过的画像：planAction 的资历判断、以及资料放行的
	// 指纹都在后面按它算。指纹必须与下次判定时算出来的完全一致，否则放行
	// 永远命中不了。
	profile = state.Sender

	// 第二次前置判断：这里刚好拿到简介（判定本来就要取，零额外开销）。
	if hit, _ := leaderGateWorker(b, snap, conf, m, state.Sender); hit != "" {
		return
	}

	// 内容哈希按识图之前的文字记，与 HandleGroupMessage 查的是同一个键。
	// 纯图没有文字：空串当键的话，之后所有纯图都会互相命中。
	text := displayText(m)
	m, state, vis, ok := seeVisual(b, snap, m, state, text)
	if !ok {
		return
	}

	// 必封规则命中时不跑 systemone：规则本身就是初判（启用前经过全库
	// 零误封测试），直接用规则作为 prior 交给大模型复判定案。命中就先删
	// + 临时禁言，不必等一次初判往返；没有复判模型时才按处置矩阵定案。
	ruleHit := len(state.MatchedRules) > 0
	var v adVerdict
	if ruleHit {
		v = ruleVerdict(state.MatchedRules)
		slog.Info("反广告：命中必封规则，跳过初判直接复判", "chat", m.Chat.ID,
			"uid", m.From.ID, "rules", matchedRuleIDs(state.MatchedRules))
	} else {
		var err error
		v, err = judgeFirst(b, snap, state)
		if err != nil {
			// 失败一律放行：判失败时按广告处理会误删整个群聊的消息。
			slog.Warn("反广告：判定失败，放行", "chat", m.Chat.ID,
				"uid", m.From.ID, "err", err)
			logAd(b, m, adVerdict{Reason: err.Error(), Usage: vis.Usage, Cost: vis.Cost},
				"none", "判定失败")
			return
		}
	}
	// 识图的钱也是判这条消息花的。
	v.Usage, v.Cost = billing.MergeUsage(v.Usage, vis.Usage), v.Cost+vis.Cost

	finish := func(v adVerdict, pre adAction, preNote string) {
		// 规则命中但最终判为正常：把命中未被采信记进日志。规则页的
		// 命中数是运营数据，这里补定案视角 —— 一条规则总被推翻，说明它
		// 覆盖过宽或只是与正常讨论同形。
		if len(state.MatchedRules) > 0 && !v.IsAd {
			slog.Info("反广告：必封规则命中但最终判为正常", "chat", m.Chat.ID,
				"uid", m.From.ID, "rules", matchedRuleIDs(state.MatchedRules),
				"decider", v.Decider)
		}
		actOnVerdict(b, snap, conf, m, profile, v, pre, preNote, text)
	}
	act := planAction(b, snap, conf, isNewbie(b, snap, profile), v)
	// 规则命中时必须过大模型复判（systemone 已被跳过）；其余沿用采信线与
	// 要罚才复判。
	forceReview := ruleHit && hasLLM(b, snap)
	if !needReview(b, snap, v, act) && !forceReview {
		finish(v, adAction{}, "")
		return
	}
	// 初判下限：置信度低于它的广告结论是噪声，直接放行、连复判都不跑 ——
	// 那种结论跑复判只是花钱买一个必然被推翻的结果。按未定记流水，管理员
	// 在记录里能看到为什么放行。
	if v.IsAd && v.Confidence*100 < float64(snap.BotSettingInt(
		b.BotID(), "antiad_so_floor", store.DefaultSoFloor)) {
		floor := snap.BotSettingInt(b.BotID(), "antiad_so_floor", store.DefaultSoFloor)
		v.IsAd, v.Kind, v.Scope, v.Severity = false, "none", "message", 0
		v.Reason = fmt.Sprintf("（初判置信度 %.0f%% 低于下限线 %d%%，直接放行，未复判）",
			v.Confidence*100, floor) + v.Reason
		finish(v, adAction{}, "")
		return
	}

	// 复判前先按初判动手：删消息、临时禁言。连带删除、封禁、告警都等复判
	// 定了再做 —— 封禁踢出群，不适合当先行动作。频道身份不先封：TG 封频道
	// 身份不支持限时，封了就得等人手工解。
	//
	// 但先行动作要过初判线（antiad_pre_act_conf，默认 75）：初判置信度低于
	// 它时只送复判，不先删也不禁。低置信初判本来就拿不准，而按模型结论定档
	// 不看置信度，先行动作会把拿不准直接变成删消息 + 临时禁言。
	// 规则命中不受这条线约束：规则是全库零误封测试过的高精度形态，
	// 一律先删 + 临时禁言，复判正常时 actOnVerdict 会自动解除。
	var pre adAction
	var preNote string
	if ruleHit {
		pre = adAction{Delete: true, Mute: m.From.ID > 0, Temp: true}
		preNote = ApplyAction(b, m, pre, conf.Dryrun)
		if pre.Mute && !conf.Dryrun {
			// 记下来，终判不罚时（或管理员复查发现正常时）主动解掉。
			NoteTempMute(b.Shared, m.Chat.ID, m.From.ID)
		}
	} else if v.Confidence*100 >= float64(snap.BotSettingInt(
		b.BotID(), "antiad_pre_act_conf", store.DefaultPreActConf)) {
		pre = adAction{Delete: act.Delete, Mute: (act.Mute || act.Ban) && m.From.ID > 0, Temp: true}
		preNote = ApplyAction(b, m, pre, conf.Dryrun)
		if pre.Mute && !conf.Dryrun {
			// 记下来，终判不罚时（或管理员复查发现正常时）主动解掉。
			NoteTempMute(b.Shared, m.Chat.ID, m.From.ID)
		}
	} else {
		slog.Info("反广告：初判置信度低于初判线，先不动手，等复判",
			"chat", m.Chat.ID, "uid", m.From.ID, "置信度", v.Confidence)
	}
	if !b.AdReview(func() { finish(review(b, snap, state, v, llmSystemPrompt), pre, preNote) }) {
		// 按初判定案，临时禁言转为正式处罚：若让它到期自行解除，等于未处置。
		slog.Warn("反广告：复判队列已满，按初判定案", "chat", m.Chat.ID, "uid", m.From.ID)
		if ruleHit {
			v.Reason = "（复判队列已满，仅采信必封规则）" + v.Reason
		} else {
			v.Reason = "（复判队列已满，仅采信 systemone 初判）" + v.Reason
		}
		finish(v, pre, preNote)
	}
}

// adAllow 是送检前的滥用护栏，返回 false 表示这条不送检。
//
// 用户明确选择了全量送检，所以这里不做抽样、不关送检，只堵滥用：
// 任何普通群成员都能靠刷屏消耗额度，这与按设计每条都判是两回事。
func adAllow(b *core.Bot, snap *store.Snapshot, chatID, msgID, editDate int64, newbie bool) (bool, string) {
	// 去重按消息 ID，只挡同一条消息被处理两次（重启后 TG 重发了更新）。
	// 不按文字去重：那样多号轮番刷同一段模板，只有第一个号被判，其余会被
	// 当成重复而既不判也不删。同文刷屏的成本由内容哈希（hash.go）覆盖，
	// 它排在这之前。
	// 带上编辑时间：编辑成广告的那一版与原版是同一个 ID，常常就在一分钟内。
	if !b.AdLimits.Allow(fmt.Sprintf("ad:d:%d:%d:%d", chatID, msgID, editDate), 1) {
		return false, adDupMessage
	}
	// 每群上限只管老成员，新人既不受限也不占额度：广告几乎都出自新号，而额度
	// 多半被老成员的日常聊天占满 —— 若先到先得，刷屏高峰里最先被放过的
	// 恰恰是广告号。新号刷屏的成本由内容哈希和判成广告后的禁言覆盖。
	if !newbie && !b.AdLimits.Allow(fmt.Sprintf("ad:c:%d", chatID),
		snap.BotSettingInt(b.BotID(), "antiad_rpm_chat", 30)) {
		return false, "超出本群送检频率上限"
	}
	return true, ""
}

const (
	adDupMessage = "同一条消息重复投递"
	// adDeciderSkipped 标记护栏拦下、没有送检的流水，verdict 记为 skipped。
	adDeciderSkipped = "skipped"
)

// adExempt 报告该发送者是否跳过判定。
//
// 顺序按成本排：三个纯内存判断在前，唯一要发 API 的群管理员判断在最后，
// 且带 10 分钟缓存。whitelisted 由调用方从画像行带进来（见 touchMember），
// 省掉一次本群白名单单查。
func adExempt(b *core.Bot, snap *store.Snapshot, chatID int64, u *tg.TGUser,
	whitelisted bool) bool {

	if u == nil {
		return true
	}
	// 关联频道自动转发（777000）：能往关联频道发帖的只有频道方，判它等于
	// 判频道自己的帖子。匿名管理员就是管理员本人，任何开关下都不判。
	if u.ID == tgServiceUID || u.ID == groupAnonymousBotID {
		return true
	}
	// bot 的豁免只给有管理员权限的 —— 由末尾的群管理员判断兜底（另
	// 一个反广告 bot 通常是管理员，判它的告警会互相删来删去）。普通成员
	// bot（工具 bot）发什么判什么；antiad_judge_bots=0 时全部豁免。
	// 访客 bot 与频道身份不走这里：入口处已把发送者换成了召唤者/频道。
	if u.IsBot && snap.BotSettingInt(b.BotID(), "antiad_judge_bots", 1) != 1 {
		return true
	}
	// 主管理员与本 bot 的归属人豁免：他们要能在群里说话而不被本服务
	// 拦下。其他次级管理员不豁免 —— 他管的是自己的群，
	// 在别人的群里他就是个普通成员。
	if b.IsMain(u.ID) || u.ID == b.Owner() {
		return true
	}
	if slices.Contains(snap.BotSettingInt64List(b.BotID(), "antiad_exempt_users"), u.ID) {
		return true
	}
	// 按群白名单（/white）：画像行里已带出来。
	if whitelisted {
		return true
	}
	// 申诉解禁 / /white 产生的白名单：纯内存判断，同样排在群管理员之前。
	if snap.Whitelisted(b.BotID(), chatID, u.ID, time.Now().Unix()) {
		return true
	}
	return IsChatAdmin(b, chatID, u.ID)
}
