package panel

import (
	"encoding/json"
	"fmt"
	"html"
	"slices"
	"strconv"
	"strings"

	"menshen/internal/antiad"
	"menshen/internal/core"
	"menshen/internal/tg"
)

// settingSpec 描述一个可在面板上编辑的整数型设置项。
// min/max 是闭区间；max 为 0 表示无上限。
//
// group 同时决定了两件事：出现在哪个面板，以及**谁能改**。
//   - ""       全局设置页，只有主管理员能进
//   - "antiad" 每个 bot 的参数页，owner 可以为自己的 bot 覆盖，
//     不覆盖就沿用全局默认
//
// 分组不能靠 key 前缀猜，必须显式声明。
type settingSpec struct {
	key   string
	label string
	hint  string // 输入提示里的取值说明
	min   int64
	max   int64
	group string
}

// settingSpecs 决定面板的展示顺序与校验规则。
//
// 不在此表的另外三类：布尔开关（antiad_enabled / gban_enabled 等）走
// 一键切换；每群开关（enabled/dryrun/group_alert）是 bot_chats 的列；
// 豁免名单是列表项，走专门的增删入口。
var settingSpecs = []settingSpec{
	{"tz_offset", "时区偏移（小时）", "整数，取值范围 -12 ~ 14", -12, 14, ""},
	{"log_retention_days", "记录保留天数", "正整数，至少 1；同时作用于判定流水与群消息留底", 1, 0, ""},
	{"max_bots_per_admin", "每人 bot 数上限", "非负整数，0 = 不限；主管理员不受限", 0, 0, ""},
	{"antiad_digest_min", "形态总结触发样本数", "非负整数，0 = 关闭自动总结", 0, 0, ""},
	{"antiad_digest_max", "形态摘要字数上限", "正整数，它会乘以每一条群消息的成本", 1, 0, ""},
	{"antiad_unban_base", "自助解除重试基数（秒）", "正整数，第 n 次要等 base×2^(n-1)，封顶 1 小时", 1, 0, ""},
	{"antiad_so_timeout_ms", "主判单次时限（毫秒）", "100-60000 的整数；超过它没回就换一个立即重试", 100, 60000, ""},
	{"antiad_llm_ttft_ms", "复判首字时限（毫秒）", "100-60000 的整数；首字超过它没到就换一个立即重试", 100, 60000, ""},
	{"antiad_hedge_retries", "并发模式触发：每分钟重试数", "非负整数，1 分钟内重试超过它就进入并发模式；0 = 关闭", 0, 0, ""},
	{"antiad_hedge_minutes", "并发模式持续（分钟）", "1-1440 的整数，期间再次触发会顺延", 1, 1440, ""},
	{"antiad_hedge_fanout", "并发模式路数", "2-5 的整数；并发期间开销随之成倍", 2, 5, ""},
	{"antiad_upstream_alert_after", "上游异常告警阈值", "连续失败达到它才私聊告警；0 = 关闭", 0, 0, ""},
	{"antiad_upstream_alert_minutes", "上游异常告警冷却（分钟）", "1-1440 的整数，冷却期内不重复告警", 1, 1440, ""},

	// ---- 每个 bot 可覆盖的参数 ----
	{"antiad_so_trust", "采信线：systemone 置信度", "0-100 的整数，低于它才转大模型复判", 0, 100, "antiad"},
	{"antiad_act_hard", "处置线：删除+禁言", "0-100 的整数，新人达到它才禁言", 0, 100, "antiad"},
	{"antiad_act_soft", "处置线：删除", "0-100 的整数，低于它不处置", 0, 100, "antiad"},
	{"antiad_cold", "进群冷判定", "1 = 开，0 = 关；开启后每个进群的人都可能花一次 AI 开销。默认跟随全局，单 bot 可覆盖", 0, 1, "both"},
	{"antiad_group_silent", "群内静默", "1 = 群里不发任何通知（告警、限制提示、命令回复都不发），判定与处置照常；0 = 正常发", 0, 1, "antiad"},
	{"antiad_cold_conf", "冷判定采信线", "0-100 的整数，建议高于处置线——进群画像的证据更少", 0, 100, "antiad"},
	{"antiad_cold_prefilter", "冷判定本地预筛", "1 = 开（只有资料可疑的才送检），0 = 关（人人送检）", 0, 1, "antiad"},
	{"antiad_new_hours", "新人界定：进群小时数", "非负整数，小于它算新人", 0, 0, "antiad"},
	{"antiad_new_msgs", "新人界定：群内消息数", "非负整数，少于它算新人", 0, 0, "antiad"},
	{"antiad_mute_hours", "禁言时长（小时）", "正整数", 1, 0, "antiad"},
	{"antiad_rpm_chat", "每群每分钟送检上限", "非负整数，0 = 不限；防刷屏烧钱", 0, 0, "antiad"},
	{"antiad_alert_rpm", "同一人每分钟告警上限", "非负整数，0 = 不限；防刷爆私聊", 0, 0, "antiad"},
	{"antiad_cmd_rpm", "每人每分钟命令上限", "非负整数，0 = 不限；/check 一次要跑两个模型", 0, 0, "antiad"},
	{"antiad_alert_ttl", "群内告警自动撤回（秒）", "非负整数，0 = 永不撤回；撤回后按钮也会消失", 0, 0, "antiad"},
	{"antiad_ctx_msgs", "携带上下文条数", "0-20 的整数，越多越准也越贵", 0, 20, "antiad"},
	{"antiad_dm_admins", "私聊汇总", "1 = 开，0 = 关；命中与判定失败定时汇成一条私聊", 0, 1, "antiad"},
	{"antiad_alert_every", "私聊汇总间隔（分钟）", "1-1440 的整数，每隔这么久最多一条", 1, 1440, "antiad"},
	{"antiad_ban", "禁言改为封禁", "1 = 该禁言的改为永久封禁出群，0 = 禁言；每群可单独覆盖", 0, 1, "antiad"},
	{"antiad_judge_bots", "判定成员 bot", "1 = 判（仍豁免管理员 bot），0 = 豁免；需对方 bot 开启 Bot-to-Bot 模式才收得到", 0, 1, "antiad"},
}

func settingSpecByKey(k string) *settingSpec {
	for i := range settingSpecs {
		if settingSpecs[i].key == k {
			return &settingSpecs[i]
		}
	}
	return nil
}

// specsInGroup 按分组过滤，g="" 取全局设置面板的项。
//
// "both" 表示两处都要出现：全局设置页给默认值（仅主管理员能改），
// 每个 bot 的参数页可以覆盖（带 ✏️），不覆盖就跟随全局。
func specsInGroup(g string) []settingSpec {
	out := make([]settingSpec, 0, len(settingSpecs))
	for _, sp := range settingSpecs {
		if sp.group == g || sp.group == "both" {
			out = append(out, sp)
		}
	}
	return out
}

// settingTarget 把「改哪个 bot 的哪一项」编码进待输入会话的 target。
// botID 为 0 表示改全局值。
func settingTarget(botID int64, key string) string {
	return strconv.FormatInt(botID, 10) + "|" + key
}

func parseSettingTarget(t string) (int64, string, bool) {
	idStr, key, ok := strings.Cut(t, "|")
	if !ok {
		return 0, "", false
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, "", false
	}
	return id, key, true
}

func handleSettingsCallback(b *core.Bot, q *tg.CallbackQuery) {
	chatID, msgID := q.Message.Chat.ID, q.Message.MessageID
	parts := strings.Split(q.Data, ":")

	if len(parts) == 2 { // a:st —— 全局设置总览
		if !b.IsMain(q.From.ID) {
			b.AnswerCallback(q.ID, "")
			return
		}
		b.AnswerCallback(q.ID, "")
		showSettings(b, chatID, msgID)
		return
	}

	switch parts[2] {
	case "e": // a:st:e:<key> —— 全局项
		if !b.IsMain(q.From.ID) {
			b.AnswerCallback(q.ID, "")
			return
		}
		if len(parts) < 4 {
			b.AnswerCallback(q.ID, "参数缺失")
			return
		}
		sp := settingSpecByKey(parts[3])
		if sp == nil || (sp.group != "" && sp.group != "both") {
			b.AnswerCallback(q.ID, "未知设置项")
			return
		}
		b.AnswerCallback(q.ID, "")
		askSettingInput(b, chatID, q.From.ID, 0, sp)

	case "t": // a:st:t:<key> —— 翻转一个全局 0/1 开关
		if !b.IsMain(q.From.ID) {
			b.AnswerCallback(q.ID, "")
			return
		}
		if len(parts) < 4 {
			b.AnswerCallback(q.ID, "参数缺失")
			return
		}
		key := parts[3]
		// 白名单：不能让任意 key 被这条路径写成 0/1，
		// 否则伪造一个 callback 就能把 tz_offset 改成 1。
		switch key {
		case "antiad_enabled", "alert_copy_main":
		default:
			b.AnswerCallback(q.ID, "未知开关")
			return
		}
		next := "1"
		if b.Cache.Snap().SettingInt(key, 0) == 1 {
			next = "0"
		}
		if err := b.PutSetting(key, next); err != nil {
			b.AnswerCallback(q.ID, "切换失败")
			return
		}
		b.AnswerCallback(q.ID, "已切换")
		showSettings(b, chatID, msgID)

	case "m": // a:st:m:so|llm|vision —— 全局默认模型（列表或单值）
		if !b.IsMain(q.From.ID) {
			b.AnswerCallback(q.ID, "")
			return
		}
		if len(parts) < 4 {
			b.AnswerCallback(q.ID, "参数缺失")
			return
		}
		key, label := "antiad_so_models", "默认判定模型（systemone）"
		single := false
		switch parts[3] {
		case "llm":
			key, label = "antiad_llm_models", "默认复判/总结模型（大模型）"
		case "vision":
			key, label = "antiad_vision_model", "识图模型（须支持图片输入；留空则图片与贴纸不判）"
			single = true
		}
		snap := b.Cache.Snap()
		cur := "（未配置）"
		if single {
			if v := snap.Setting(key); v != "" {
				cur = v
			}
		} else {
			so, llm := snap.ModelsFor(0)
			list := so
			if key == "antiad_llm_models" {
				list = llm
			}
			if len(list) > 0 {
				cur = strings.Join(list, ", ")
			}
		}
		hint := "请输入<b>" + label + "</b>的模型名（必须已存在于「模型定价」且为启用状态）："
		if !single {
			hint = "请输入<b>" + label + "</b>列表，按<b>重试顺序</b>用逗号分隔：\n" +
				"<code>a/gpt-5-mini, b/gpt-5-mini, a/deepseek-v3</code>\n\n" +
				"每项形如 <code>上游名/模型ID</code>，必须已存在于「模型定价」且为启用状态。"
		}
		b.AnswerCallback(q.ID, "")
		b.AskInput(chatID, q.From.ID, "st_model", key,
			hint+"\n\n填 <code>-</code> 可清空。\n\n当前值：<code>"+
				html.EscapeString(cur)+"</code>")

	case "b": // a:st:b:<botID>:<key> —— 某个 bot 的覆盖值
		if len(parts) < 5 {
			b.AnswerCallback(q.ID, "参数缺失")
			return
		}
		botID, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil {
			b.AnswerCallback(q.ID, "参数无效")
			return
		}
		if !b.CanManageBot(q.From.ID, botID) {
			b.AnswerCallback(q.ID, "")
			return
		}
		sp := settingSpecByKey(parts[4])
		if sp == nil || (sp.group != "antiad" && sp.group != "both") {
			b.AnswerCallback(q.ID, "未知设置项")
			return
		}
		b.AnswerCallback(q.ID, "")
		askSettingInput(b, chatID, q.From.ID, botID, sp)

	default:
		b.AnswerCallback(q.ID, "")
	}
}

// askSettingInput 弹出某个设置项的输入框。
func askSettingInput(b *core.Bot, chatID, uid, botID int64, sp *settingSpec) {
	snap := b.Cache.Snap()
	cur := snap.Setting(sp.key)
	extra := ""
	if botID != 0 {
		cur = snap.BotSetting(botID, sp.key)
		extra = "\n\n填 <code>-</code> 可恢复为全局默认值 <code>" +
			html.EscapeString(snap.Setting(sp.key)) + "</code>。"
	}
	b.AskInput(chatID, uid, "st_edit", settingTarget(botID, sp.key),
		"请输入<b>"+html.EscapeString(sp.label)+"</b>的新值（"+sp.hint+"）："+
			extra+"\n\n当前值：<code>"+html.EscapeString(cur)+"</code>")
}

// ---- 设置持久化 ----

// ---- 全局设置面板（仅主管理员）----

func showSettings(b *core.Bot, chatID, msgID int64) {
	snap := b.Cache.Snap()

	var sb strings.Builder
	sb.WriteString("⚙️ <b>全局设置</b>\n\n")

	if snap.SettingInt("antiad_enabled", 0) == 1 {
		sb.WriteString("总开关: ✅ 已启用\n")
	} else {
		sb.WriteString("总开关: ⛔ <b>已关闭</b> —— 所有 bot 的所有群都不判定\n")
	}
	if snap.SettingInt("alert_copy_main", 0) == 1 {
		sb.WriteString("告警抄送主管理员: 开\n")
	} else {
		sb.WriteString("告警抄送主管理员: 关（各 bot 的告警只发给它的归属人）\n")
	}
	sb.WriteString("\n")

	for _, sp := range specsInGroup("") {
		sb.WriteString(html.EscapeString(sp.label) + "：<code>" +
			html.EscapeString(snap.Setting(sp.key)) + "</code>\n")
	}

	sb.WriteString("\n<b>默认模型</b>（按重试顺序，可在每个 bot 上单独覆盖）\n")
	so, llm := snap.ModelsFor(0)
	fmt.Fprintf(&sb, "判定: %s\n复判: %s\n识图: %s\n",
		modelListLabel(so), modelListLabel(llm),
		modelLabel(snap.Setting("antiad_vision_model")))

	sb.WriteString("\n<i>保留天数同时作用于判定流水、群消息留底与群成员画像" +
		"（有命中史的画像行保留，那是风控证据）。</i>\n")

	onLabel := "✅ 启用总开关"
	if snap.SettingInt("antiad_enabled", 0) == 1 {
		onLabel = "⛔ 关闭总开关"
	}
	copyLabel := "📨 开启告警抄送"
	if snap.SettingInt("alert_copy_main", 0) == 1 {
		copyLabel = "📭 关闭告警抄送"
	}

	rows := [][][2]string{
		{{onLabel, "a:st:t:antiad_enabled"}},
		{{copyLabel, "a:st:t:alert_copy_main"}},
		{
			{"🤖 默认判定模型", "a:st:m:so"},
			{"🤖 默认复判模型", "a:st:m:llm"},
		},
		{{"👁 识图模型", "a:st:m:vision"}},
		{{"📝 形态摘要", "a:ad:dg"}},
	}
	// 一行一个：按钮上带了当前值，两列会在手机端被截断，
	// 而截断掉的恰好是值——那按钮就白带了。
	for _, sp := range specsInGroup("") {
		rows = append(rows, [][2]string{{
			"📝 " + sp.label + " · " + btnValue(snap.Setting(sp.key)),
			"a:st:e:" + sp.key,
		}})
	}
	rows = append(rows, [][2]string{{"◀️ 返回主菜单", "a:main"}})

	b.EditOrSend(chatID, msgID, sb.String(), tg.InlineKB(rows...))
}

// ---- 文本输入 ----

// handleSettingsInput 处理管理员补发的那条文本。
//
// 校验失败一律**保留会话**并提示重填：一次手误不该让人从头点一遍菜单。
// 成功或彻底放弃时才 Delete —— 不删的话，5 分钟 TTL 内下一条无关消息
// 会被当成又一次输入。
func handleSettingsInput(b *core.Bot, m *tg.Message, p core.PendingInput, text string) {
	chatID := m.Chat.ID
	uid := m.From.ID

	switch p.Op {
	case "st_edit":
		botID, key, ok := parseSettingTarget(p.Target)
		if !ok {
			b.DropPending(uid)
			b.Send(chatID, "会话数据异常，请重新从菜单进入。", nil)
			return
		}
		sp := settingSpecByKey(key)
		if sp == nil {
			b.DropPending(uid)
			b.Send(chatID, "该设置项已不存在，操作取消。", nil)
			return
		}
		if botID != 0 && !b.CanManageBot(uid, botID) {
			b.DropPending(uid)
			return
		}

		// per-bot 项填 "-" 表示撤销覆盖、回到全局默认。
		if botID != 0 && text == "-" {
			b.DropPending(uid)
			if err := b.PutBotSetting(botID, key,
				b.Cache.Snap().Setting(key)); err != nil {
				b.Send(chatID, "保存失败。", nil)
				return
			}
			b.Send(chatID, "✅ 已恢复为全局默认值。", nil)
			showBotConfig(b, chatID, 0, botID)
			return
		}

		v, err := strconv.ParseInt(text, 10, 64)
		if err != nil || v < sp.min || (sp.max != 0 && v > sp.max) {
			b.Send(chatID, "取值非法（"+sp.hint+"），请重新输入：", nil)
			return
		}
		b.DropPending(uid)

		val := strconv.FormatInt(v, 10)
		if botID == 0 {
			err = b.PutSetting(key, val)
		} else {
			err = b.PutBotSetting(botID, key, val)
		}
		if err != nil {
			b.Send(chatID, "保存失败："+html.EscapeString(err.Error()), nil)
			return
		}
		b.Send(chatID, "✅ 已将<b>"+html.EscapeString(sp.label)+
			"</b>设为 <code>"+val+"</code>", nil)
		if botID == 0 {
			showSettings(b, chatID, 0)
		} else {
			showBotConfig(b, chatID, 0, botID)
		}

	case "st_model":
		if !b.IsMain(uid) {
			b.DropPending(uid)
			return
		}
		name := strings.TrimSpace(text)
		single := p.Target == "antiad_vision_model"
		if name == "-" {
			b.DropPending(uid)
			if single {
				if err := b.PutSetting(p.Target, ""); err != nil {
					b.Send(chatID, "保存失败。", nil)
					return
				}
			} else if err := b.PutSetting(p.Target, "[]"); err != nil {
				b.Send(chatID, "保存失败。", nil)
				return
			} else if old := legacyModelKey(p.Target); old != "" {
				// 旧单值键也要清：回退读会把它当单元素列表捡回来。
				_ = b.PutSetting(old, "")
			}
			b.Send(chatID, "已清空。", nil)
			showSettings(b, chatID, 0)
			return
		}
		if single {
			if !checkModelUsable(b, chatID, name) {
				return // 保留会话让管理员直接重填
			}
			b.DropPending(uid)
			if err := b.PutSetting(p.Target, name); err != nil {
				b.Send(chatID, "保存失败。", nil)
				return
			}
			b.Send(chatID, "已设为 "+modelLabel(name)+"。", nil)
			showSettings(b, chatID, 0)
			return
		}
		models, err := parseModelList(b, name)
		if err != nil {
			b.Send(chatID, "❌ "+html.EscapeString(err.Error())+"\n\n请重新输入：", nil)
			return
		}
		b.DropPending(uid)
		if err := b.PutSetting(p.Target, modelsJSON(models)); err != nil {
			b.Send(chatID, "保存失败。", nil)
			return
		}
		if old := legacyModelKey(p.Target); old != "" {
			_ = b.PutSetting(old, "")
		}
		b.Send(chatID, "已设为（按重试顺序）："+modelListLabel(models)+"。", nil)
		showSettings(b, chatID, 0)

	case "bot_model_so", "bot_model_llm":
		if !b.IsMain(uid) {
			b.DropPending(uid)
			return
		}
		botID, err := strconv.ParseInt(p.Target, 10, 64)
		if err != nil {
			b.DropPending(uid)
			return
		}
		which := "so"
		if p.Op == "bot_model_llm" {
			which = "llm"
		}
		text = strings.TrimSpace(text)
		var models []string
		if text != "-" {
			if models, err = parseModelList(b, text); err != nil {
				b.Send(chatID, "❌ "+html.EscapeString(err.Error())+"\n\n请重新输入：", nil)
				return
			}
		}
		b.DropPending(uid)
		if err := b.SetBotModels(botID, which, models); err != nil {
			b.Send(chatID, "保存失败。", nil)
			return
		}
		if len(models) == 0 {
			b.Send(chatID, "已恢复为全局默认模型。", nil)
		} else {
			b.Send(chatID, "已设为（按重试顺序）："+modelListLabel(models)+"。", nil)
		}
		showBotDetail(b, chatID, 0, uid, botID)

	case "ad_dg_e":
		if !b.IsMain(uid) {
			b.DropPending(uid)
			return
		}
		b.DropPending(uid)
		v := strings.TrimSpace(text)
		if v == "-" {
			v = ""
		}
		maxLen := int(b.Cache.Snap().SettingInt("antiad_digest_max", 1200))
		if err := b.PutSetting("antiad_digest", core.TruncateRunes(v, maxLen)); err != nil {
			b.Send(chatID, "保存失败。", nil)
			return
		}
		b.Send(chatID, "形态摘要已更新。", nil)
		showSettings(b, chatID, 0)

	case "bot_add":
		if b.Reg == nil {
			b.DropPending(uid)
			b.Send(chatID, "内部错误：实例表不可用。", nil)
			return
		}
		token := strings.TrimSpace(text)

		// 先删掉他发的那条。token 等同于该 bot 的完整控制权，指望用户
		// 自己记得删是不现实的，而它会一直躺在聊天记录里 —— 换台设备
		// 登录、把手机递给别人看一眼，都够了。
		b.TG.Call("deleteMessage", map[string]any{
			"chat_id": chatID, "message_id": m.MessageID,
		})

		// 本地就能判的失败不出网：格式敲错、重复接入、超配额占了失败的
		// 大头，先花一次 getMe 的往返再告诉他「你已经加过了」既慢又蠢。
		if err := b.Reg.Precheck(token, uid); err != nil {
			b.KeepInput(uid, "bot_add", "")
			b.Send(chatID, "❌ "+err.Error()+"\n\n请重新发送 token：", nil)
			return
		}

		b.DropPending(uid)
		progress := b.SendGetID(chatID,
			"🔍 正在验证 token、获取 bot 信息并注册回调地址……", nil)
		// 验真与注册最坏要等到传输层超时，放异步：更新处理是串行的。
		go addBotAndReport(b, chatID, uid, token, progress)

	case "bot_chat_add":
		botID, err := strconv.ParseInt(p.Target, 10, 64)
		if err != nil || !b.CanManageBot(uid, botID) {
			b.DropPending(uid)
			return
		}
		id, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if err != nil {
			// 不接受 @username：群改名后就会永久失效，数字 id 是稳定的。
			b.Send(chatID, "请输入纯数字 chat_id（群组是负数）：", nil)
			return
		}
		b.DropPending(uid)
		if _, dup := b.Cache.Snap().ChatConf(botID, id); dup {
			b.Send(chatID, "该群已在此 bot 名下。", nil)
			showBotDetail(b, chatID, 0, uid, botID)
			return
		}
		if err := addBotChat(b, botID, id); err != nil {
			b.Send(chatID, "保存失败。", nil)
			return
		}
		b.Send(chatID, fmt.Sprintf(
			"✅ 已添加群 <code>%d</code>，当前是<b>演练模式</b>。\n\n"+
				"观察几天拦截记录，确认没有误伤后再切到正式模式。", id), nil)
		showChatDetail(b, chatID, 0, botID, id)

	case "bot_ex_add":
		botID, err := strconv.ParseInt(p.Target, 10, 64)
		if err != nil || !b.CanManageBot(uid, botID) {
			b.DropPending(uid)
			return
		}
		id, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if err != nil {
			b.Send(chatID, "请输入纯数字 user_id：", nil)
			return
		}
		b.DropPending(uid)
		list := b.Cache.Snap().BotSettingInt64List(botID, "antiad_exempt_users")
		if slices.Contains(list, id) {
			b.Send(chatID, "已在名单中。", nil)
			showBotExempt(b, chatID, 0, botID)
			return
		}
		if err := b.PutBotInt64List(botID, "antiad_exempt_users",
			append(list, id)); err != nil {
			b.Send(chatID, "保存失败。", nil)
			return
		}
		b.Send(chatID, fmt.Sprintf("已豁免 <code>%d</code>。", id), nil)
		showBotExempt(b, chatID, 0, botID)

	case "admin_add":
		if !b.IsMain(uid) {
			b.DropPending(uid)
			return
		}
		idStr, note, _ := strings.Cut(strings.TrimSpace(text), " ")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id <= 0 {
			b.Send(chatID, "请输入纯数字 user_id，后面可跟备注：", nil)
			return
		}
		b.DropPending(uid)
		if err := b.AddAdmin(id, strings.TrimSpace(note), uid); err != nil {
			b.Send(chatID, "❌ "+html.EscapeString(err.Error()), nil)
			showAdmins(b, chatID, 0)
			return
		}
		b.Send(chatID, fmt.Sprintf(
			"✅ <code>%d</code> 已成为次级管理员。\n\n"+
				"他现在可以私聊任意一个已接入的 bot 发 /start 打开面板，"+
				"接入自己的机器人。", id), nil)
		showAdmins(b, chatID, 0)

	case "gban_add":
		if !b.IsMain(uid) {
			b.DropPending(uid)
			return
		}
		idStr, reason, _ := strings.Cut(strings.TrimSpace(text), " ")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id <= 0 {
			b.Send(chatID, "请输入纯数字 user_id，后面可跟理由：", nil)
			return
		}
		b.DropPending(uid)
		reason = strings.TrimSpace(reason)
		if reason == "" {
			reason = "管理员手工加入"
		}
		if err := antiad.GbanAdd(b.Shared, id, reason, 0, b.BotID()); err != nil {
			b.Send(chatID, "保存失败。", nil)
			return
		}
		b.Send(chatID, fmt.Sprintf(
			"已把 <code>%d</code> 加入联合封禁名单，正在全平台执行。", id), nil)
		go antiad.EnforceGban(b.Shared, id, reason)
		showGban(b, chatID, 0, 1)

	default:
		b.DropPending(uid)
	}
}

// checkModelUsable 校验模型名可用，不可用时给出提示并返回 false。
//
// 必须校验：配了不存在的模型名，链路会在每条消息上向上游拿回 404，
// 而 404 在 bot 侧只表现为「判定失败 → 放行」，功能静默失效，
// 运维完全看不见。
// parseModelList 解析逗号分隔的模型名列表：逐个校验存在且启用，去重但
// 保持输入顺序（顺序即重试顺序）。返回的错误直接给管理员看。
func parseModelList(b *core.Bot, text string) ([]string, error) {
	snap := b.Cache.Snap()
	var out []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(text, ",") {
		name := strings.TrimSpace(raw)
		if name == "" || seen[name] {
			continue
		}
		m := snap.Models[name]
		if m == nil {
			return nil, fmt.Errorf("模型 %s 不在「模型定价」里", name)
		}
		if !m.Enabled {
			return nil, fmt.Errorf("模型 %s 已被停用", name)
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("至少要填一个模型名")
	}
	return out, nil
}

// modelsJSON 把模型列表序列化成设置值。
func modelsJSON(models []string) string {
	raw, err := json.Marshal(models)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

// legacyModelKey 返回与列表键对应的旧单值键，没有则空串。
func legacyModelKey(listKey string) string {
	switch listKey {
	case "antiad_so_models":
		return "antiad_so_model"
	case "antiad_llm_models":
		return "antiad_llm_model"
	}
	return ""
}

// checkModelUsable 校验一个单值模型名可用（识图模型用）。
func checkModelUsable(b *core.Bot, chatID int64, name string) bool {
	m := b.Cache.Snap().Models[name]
	if m == nil {
		b.Send(chatID, "模型不存在，请在「模型定价」里确认名称后重填：", nil)
		return false
	}
	if !m.Enabled {
		b.Send(chatID, "该模型当前是禁用状态，请先启用它，或换一个：", nil)
		return false
	}
	return true
}
