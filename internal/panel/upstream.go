package panel

import (
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"

	"menshen/internal/core"
	"menshen/internal/tg"
	"menshen/internal/upstream"
)

func handleUpstreamCallback(b *core.Bot, q *tg.CallbackQuery) {
	chatID, msgID := q.Message.Chat.ID, q.Message.MessageID
	parts := strings.Split(q.Data, ":")

	if len(parts) == 2 { // a:up - 上游列表
		b.AnswerCallback(q.ID, "")
		showUpstreamList(b, chatID, msgID)
		return
	}

	if parts[2] == "new" { // a:up:new - 开始新增
		b.AnswerCallback(q.ID, "")
		if len(parts) >= 4 && parts[3] == "t" {
			// a:up:new:t:<uid>:<mask>:<action> — 端点选择界面
			handleUpstreamNewType(b, q)
			return
		}
		if len(parts) >= 4 && parts[3] == "k" {
			// a:up:new:k:<uid>:<kind> — 渠道类型选择
			handleUpstreamNewKind(b, q)
			return
		}
		b.AskInput(chatID, q.From.ID, "up_new_name", "", "请输入上游名称：")
		return
	}

	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		b.AnswerCallback(q.ID, "ID 无效")
		return
	}

	action := ""
	if len(parts) >= 4 {
		action = parts[3]
	}
	confirmed := len(parts) >= 5 && parts[4] == "y"

	switch action {
	case "": // a:up:<id> - 详情面板
		b.AnswerCallback(q.ID, "")
		showUpstreamDetail(b, chatID, msgID, id)

	case "t": // a:up:<id>:t:<which> - 切换端点开关
		if len(parts) < 5 {
			b.AnswerCallback(q.ID, "参数缺失")
			return
		}
		if err := toggleUpstreamSupports(b, id, parts[4]); err != nil {
			b.AnswerCallback(q.ID, err.Error())
			return
		}
		b.AnswerCallback(q.ID, "已切换")
		showUpstreamDetail(b, chatID, msgID, id)

	case "k": // a:up:<id>:k[:<kind>] - 查看/切换渠道类型
		if len(parts) < 5 {
			b.AnswerCallback(q.ID, "")
			showUpstreamKindPicker(b, chatID, msgID, 0, id)
			return
		}
		if err := setUpstreamKind(b, id, parts[4]); err != nil {
			b.AnswerCallback(q.ID, err.Error())
			return
		}
		b.AnswerCallback(q.ID, "已切换")
		showUpstreamDetail(b, chatID, msgID, id)

	case "s": // a:up:<id>:s - 启用/停用
		if err := toggleUpstreamStatus(b, id); err != nil {
			b.AnswerCallback(q.ID, "切换失败")
			return
		}
		b.AnswerCallback(q.ID, "已切换")
		showUpstreamDetail(b, chatID, msgID, id)

	case "e": // a:up:<id>:e:<field> - 编辑字段
		if len(parts) < 5 {
			b.AnswerCallback(q.ID, "参数缺失")
			return
		}
		b.AnswerCallback(q.ID, "")
		switch parts[4] {
		case "name":
			b.AskInput(chatID, q.From.ID, "up_edit_name", parts[2],
				fmt.Sprintf("请输入上游 %d 的新名称：", id))
		case "url":
			b.AskInput(chatID, q.From.ID, "up_edit_url", parts[2],
				fmt.Sprintf("请输入上游 %d 的新 base_url：", id))
		case "key":
			b.AskInput(chatID, q.From.ID, "up_edit_key", parts[2],
				fmt.Sprintf("请输入上游 %d 的新 api_key：", id))
		case "w":
			b.AskInput(chatID, q.From.ID, "up_edit_weight", parts[2],
				fmt.Sprintf("请输入上游 %d 的新权重（正整数）：", id))
		}

	case "d": // a:up:<id>:d[:y] - 删除/确认删除
		if !confirmed {
			b.AnswerCallback(q.ID, "")
			b.Edit(chatID, msgID, fmt.Sprintf("⚠️ 确认删除上游 %d？", id),
				tg.InlineKB(
					[][2]string{{"✅ 确认删除", fmt.Sprintf("a:up:%d:d:y", id)}},
					[][2]string{{"◀️ 取消", fmt.Sprintf("a:up:%d", id)}},
				))
			return
		}
		// 名下有模型时拒绝：模型名里嵌着上游名，删掉上游会留下一批
		// 「绑定的上游不存在」的死引用 —— 判定每次都失败，而面板上
		// 看不出原因。
		if names := core.ModelsOfUpstream(b.Cache.Snap(), id); len(names) > 0 {
			show := names
			if len(show) > 10 {
				show = append(show[:10:10], "……")
			}
			b.AnswerCallback(q.ID, "该上游名下有模型")
			b.Edit(chatID, msgID, fmt.Sprintf(
				"⛔ <b>该上游名下有 %d 个模型，不能删除</b>\n\n%s\n\n"+
					"请先到「🤖 模型定价」删掉它们，或把它们改到别的上游名下。",
				len(names), html.EscapeString(strings.Join(show, "\n"))),
				tg.InlineKB([][2]string{{"◀️ 返回", fmt.Sprintf("a:up:%d", id)}}))
			return
		}
		if err := b.DeleteUpstream(id); err != nil {
			b.AnswerCallback(q.ID, opText(err, "删除失败"))
			return
		}
		b.AnswerCallback(q.ID, "已删除")
		showUpstreamList(b, chatID, msgID)

	default:
		b.AnswerCallback(q.ID, "")
	}
}

func showUpstreamList(b *core.Bot, chatID, msgID int64) {
	snap := b.Cache.Snap()
	var sb strings.Builder
	sb.WriteString("🔌 <b>上游渠道</b>\n\n")

	if len(snap.Upstreams) == 0 {
		sb.WriteString("暂无上游。判定链路需要至少一个上游才能工作。\n\n" +
			"渠道类型决定协议：<b>OpenAI 兼容</b>走 /v1/chat/completions 与 " +
			"/v1/systemone；<b>Cloudflare Workers AI</b>走 /ai/run，主判定直接调 " +
			"Clef。\n" +
			"<b>systemone</b> 是主判定端点（TypeSafe jev，返回带置信度的" +
			"结构化答案），<b>chat</b> 用于大模型复判与形态总结。\n" +
			"只配 chat 也能跑：主判定不可用时会自动退化成只用大模型。")
	} else {
		for _, u := range snap.Upstreams {
			status := "✅"
			if u.Status == 0 {
				status = "❌"
			}
			fmt.Fprintf(&sb, "%s <b>%s</b> (ID:%d)\n",
				status, html.EscapeString(u.Name), u.ID)
			sb.WriteString("  " + html.EscapeString(u.BaseURL) + "\n")
			fmt.Fprintf(&sb, "  Key: <code>%s</code> ｜ 权重 %d\n",
				upstream.MaskKey(u.APIKey), u.Weight)
			sb.WriteString("  支持: " + upstreamCaps(u) + "\n\n")
		}
	}

	rows := [][][2]string{{{"➕ 新增上游", "a:up:new"}}}
	for _, u := range snap.Upstreams {
		rows = append(rows, [][2]string{
			{fmt.Sprintf("⚙️ %s", btnValue(u.Name)), fmt.Sprintf("a:up:%d", u.ID)}})
	}
	rows = append(rows, [][2]string{{"◀️ 返回主菜单", "a:main"}})
	b.EditOrSend(chatID, msgID, sb.String(), tg.InlineKB(rows...))
}

// upstreamCaps 把渠道类型与端点开关渲染成一行人话。
func upstreamCaps(u *upstream.Upstream) string {
	kind := u.EffectiveKind()
	if kind.ChatOnly() {
		return kind.Label() + " ｜ chat（复判/总结）"
	}
	var caps []string
	if u.SupportsChat {
		caps = append(caps, "chat")
	}
	if u.SupportsSystemOne {
		if kind == upstream.KindCloudflare {
			caps = append(caps, "主判定（Clef）")
		} else {
			caps = append(caps, "systemone")
		}
	}
	if len(caps) == 0 {
		return kind.Label() + " ｜ 无（该上游不会被选中）"
	}
	return kind.Label() + " ｜ " + strings.Join(caps, ", ")
}

func showUpstreamDetail(b *core.Bot, chatID, msgID, id int64) {
	var up *upstream.Upstream
	for _, u := range b.Cache.Snap().Upstreams {
		if u.ID == id {
			up = u
			break
		}
	}
	if up == nil {
		b.Edit(chatID, msgID, "上游不存在。", tg.InlineKB([][2]string{{"◀️ 返回", "a:up"}}))
		return
	}

	status := "✅ 启用"
	if up.Status == 0 {
		status = "❌ 停用"
	}
	mark := func(v bool) string {
		if v {
			return "✅"
		}
		return "❌"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "🔌 <b>上游详情</b>\n\nID: %d\n", up.ID)
	sb.WriteString("名称: " + html.EscapeString(up.Name) + "\n")
	fmt.Fprintf(&sb, "类型: %s\n", up.EffectiveKind().Label())
	sb.WriteString("Base URL: " + html.EscapeString(up.BaseURL) + "\n")
	sb.WriteString("API Key: <code>" + upstream.MaskKey(up.APIKey) + "</code>\n")
	fmt.Fprintf(&sb, "权重: %d\n状态: %s\n\n", up.Weight, status)
	kind := up.EffectiveKind()
	switch {
	case kind.ChatOnly():
		fmt.Fprintf(&sb, "能力: 固定 chat（复判/形态总结），%s 是对话协议，不能做主判定。\n",
			kind.Label())
	case kind == upstream.KindCloudflare:
		fmt.Fprintf(&sb, "supports_chat: %s\n", mark(up.SupportsChat))
		fmt.Fprintf(&sb, "supports_systemone: %s\n", mark(up.SupportsSystemOne))
		sb.WriteString("主判定走 /ai/run/&lt;模型ID&gt;（Clef）：base_url 需含 " +
			"/client/v4/accounts/&lt;账号ID&gt;，模型 ID 填 <code>@cf/cloudflare/clef</code>。\n")
	default:
		fmt.Fprintf(&sb, "supports_chat: %s\n", mark(up.SupportsChat))
		fmt.Fprintf(&sb, "supports_systemone: %s\n", mark(up.SupportsSystemOne))
	}

	var rows [][][2]string
	if !kind.ChatOnly() {
		rows = append(rows, [][2]string{
			{"🔄 Chat", fmt.Sprintf("a:up:%d:t:chat", id)},
			{"🔄 SystemOne", fmt.Sprintf("a:up:%d:t:so", id)},
		})
	}
	rows = append(rows,
		[][2]string{
			{"📝 改名称", fmt.Sprintf("a:up:%d:e:name", id)},
			{"📝 改 URL", fmt.Sprintf("a:up:%d:e:url", id)},
		},
		[][2]string{
			{"🔑 改 Key", fmt.Sprintf("a:up:%d:e:key", id)},
			{"⚖️ 改权重", fmt.Sprintf("a:up:%d:e:w", id)},
		},
		[][2]string{
			{"🔀 切换类型", fmt.Sprintf("a:up:%d:k", id)},
			{"🔄 启停", fmt.Sprintf("a:up:%d:s", id)},
		},
		[][2]string{{"🗑 删除", fmt.Sprintf("a:up:%d:d", id)}},
		[][2]string{{"◀️ 返回", "a:up"}},
	)
	b.EditOrSend(chatID, msgID, sb.String(), tg.InlineKB(rows...))
}

func handleUpstreamNewInput(b *core.Bot, m *tg.Message, p core.PendingInput, text string) {
	chatID := m.Chat.ID

	switch p.Op {
	case "up_new_name":
		name := strings.TrimSpace(text)
		if err := core.ValidUpstreamName(name); err != nil {
			b.Send(chatID, "❌ "+err.Error()+"，请重新输入：", nil)
			return
		}
		b.AskInput(chatID, m.From.ID, "up_new_url", name,
			"请输入 base_url（需含协议头 http:// 或 https://）：")

	case "up_new_url":
		text, err := core.CleanBaseURL(text)
		if err != nil {
			b.Send(chatID, err.Error()+"，请重新输入：", nil)
			return
		}
		// 用 \x00 分隔已填字段，串联到最后一步一次性 INSERT
		b.AskInput(chatID, m.From.ID, "up_new_key", p.Target+"\x00"+text, "请输入 api_key：")

	case "up_new_key":
		b.DropPending(m.From.ID)
		parts := strings.Split(p.Target, "\x00")
		if len(parts) != 2 {
			b.Send(chatID, "会话数据异常，请重新点击「新增上游」。", nil)
			return
		}
		if text == "" {
			b.Send(chatID, "api_key 不能为空，操作取消。", nil)
			return
		}
		b.UpstreamNewDraft.Store(m.From.ID, parts[0]+"\x00"+parts[1]+"\x00"+text)
		// 先选渠道类型：类型决定协议与可选端点，选错类型的话端点勾选
		// 全都没有意义。
		showUpstreamKindPicker(b, chatID, 0, m.From.ID, 0)
	}
}

// showUpstreamKindPicker 展示渠道类型选择面板。
// id > 0 时是给已有上游「切换类型」；否则是新增流程（uid 对应草稿）。
func showUpstreamKindPicker(b *core.Bot, chatID, msgID, uid, id int64) {
	text := "🔌 <b>选择渠道类型</b>\n\n" +
		"• <b>OpenAI Completions</b>：/v1/chat/completions；勾选 systemone 时走 /v1/systemone\n" +
		"• <b>OpenAI Responses</b>：/v1/responses（仅复判）\n" +
		"• <b>Anthropic Messages</b>：/v1/messages（仅复判）\n" +
		"• <b>Google Gemini</b>：generateContent（仅复判）\n" +
		"• <b>Cloudflare Workers AI</b>：/ai/run，主判定直调 Clef（可再开 chat）\n\n" +
		"base_url 约定：Gemini 填 https://generativelanguage.googleapis.com/v1beta；" +
		"Cloudflare 需含 /client/v4/accounts/&lt;账号ID&gt;，模型 ID 填 " +
		"<code>@cf/cloudflare/clef</code>；其余填到域名。\n"
	if id > 0 {
		text += "\n切换类型会按类型重置能力开关。"
	} else {
		text += "\nResponses / Anthropic / Gemini 是对话协议，只能用于复判与形态总结。"
	}

	cb := func(kind upstream.Kind) string {
		if id > 0 {
			return fmt.Sprintf("a:up:%d:k:%s", id, kind)
		}
		return fmt.Sprintf("a:up:new:k:%d:%s", uid, kind)
	}
	rows := [][][2]string{
		{
			{"OpenAI Completions", cb(upstream.KindOpenAI)},
			{"OpenAI Responses", cb(upstream.KindOpenAIResp)},
		},
		{
			{"Anthropic", cb(upstream.KindAnthropic)},
			{"Gemini", cb(upstream.KindGemini)},
		},
		{
			{"Cloudflare", cb(upstream.KindCloudflare)},
			{"❌ 取消", "a:up"},
		},
	}
	b.EditOrSend(chatID, msgID, text, tg.InlineKB(rows...))
}

// handleUpstreamNewKind 处理新增流程的渠道类型回调：
// a:up:new:k:<uid>:<kind>
func handleUpstreamNewKind(b *core.Bot, q *tg.CallbackQuery) {
	chatID, msgID := q.Message.Chat.ID, q.Message.MessageID
	parts := strings.Split(q.Data, ":")
	if len(parts) < 6 {
		b.AnswerCallback(q.ID, "参数缺失")
		return
	}
	uid, err := strconv.ParseInt(parts[4], 10, 64)
	if err != nil {
		b.AnswerCallback(q.ID, "参数无效")
		return
	}
	kind, err := upstream.ParseKind(parts[5])
	if err != nil {
		b.AnswerCallback(q.ID, "参数无效")
		return
	}

	// OpenAI Completions 继续走端点勾选（chat/systemone 都可选）；
	// 其余类型的能力由类型决定，直接创建。
	if kind == upstream.KindOpenAI {
		b.AnswerCallback(q.ID, "")
		showUpstreamTypePicker(b, chatID, msgID, uid, 0b01)
		return
	}
	chat, so := kind.ResolveCaps(false, false, false)
	createUpstreamFromDraft(b, q, chatID, msgID, uid, kind, chat, so)
}

// createUpstreamFromDraft 把当前新增草稿按给定类型与能力落库。
func createUpstreamFromDraft(b *core.Bot, q *tg.CallbackQuery, chatID, msgID, uid int64,
	kind upstream.Kind, chat, so bool) bool {

	raw, ok := b.UpstreamNewDraft.Load(uid)
	if !ok {
		b.AnswerCallback(q.ID, "会话已过期，请重新添加")
		b.Edit(chatID, msgID, "会话已过期，请重新点击「新增上游」。",
			tg.InlineKB([][2]string{{"◀️ 返回", "a:up"}}))
		return false
	}
	b.UpstreamNewDraft.Delete(uid)
	dp := strings.Split(raw.(string), "\x00")
	if len(dp) != 3 {
		b.AnswerCallback(q.ID, "数据异常")
		return false
	}
	if err := b.AddUpstream(core.UpstreamPatch{Name: &dp[0], BaseURL: &dp[1],
		APIKey: &dp[2], Kind: &kind, Chat: &chat, SystemOne: &so}); err != nil {
		b.AnswerCallback(q.ID, "新增失败")
		b.Edit(chatID, msgID, "新增失败："+html.EscapeString(opText(err, "内部错误")),
			tg.InlineKB([][2]string{{"◀️ 返回", "a:up"}}))
		return false
	}
	b.AnswerCallback(q.ID, "✅ 已添加")
	showUpstreamList(b, chatID, msgID)
	return true
}

// showUpstreamTypePicker 展示端点类型选择面板。
// mask: bit0=chat, bit1=systemone。
func showUpstreamTypePicker(b *core.Bot, chatID, msgID, uid int64, mask int) {
	check := func(bit int) string {
		if mask&bit != 0 {
			return "✅ "
		}
		return "☑️ "
	}
	picked := func(bit int) string {
		if mask&bit != 0 {
			return "✅ 已选"
		}
		return "未选"
	}
	text := "🔌 <b>新增上游（OpenAI 兼容）— 选择支持的端点</b>\n\n" +
		"点击按钮切换勾选，至少选一项后点「✅ 确认添加」。\n\n" +
		fmt.Sprintf("Chat Completions（复判/形态总结）：%s\n", picked(1)) +
		fmt.Sprintf("System One（TypeSafe jev，主判定）：%s\n", picked(2))

	// callback_data: a:up:new:t:<uid>:<mask>:<toggle|y>
	rows := [][][2]string{
		{
			{check(1) + "Chat", fmt.Sprintf("a:up:new:t:%d:%d:0", uid, mask)},
			{check(2) + "SystemOne", fmt.Sprintf("a:up:new:t:%d:%d:1", uid, mask)},
		},
		{{"✅ 确认添加", fmt.Sprintf("a:up:new:t:%d:%d:y", uid, mask)}},
		{{"❌ 取消", "a:up"}},
	}
	b.EditOrSend(chatID, msgID, text, tg.InlineKB(rows...))
}

// handleUpstreamNewType 处理端点选择界面的回调。
// data: a:up:new:t:<uid>:<mask>:<toggle|y>
func handleUpstreamNewType(b *core.Bot, q *tg.CallbackQuery) {
	chatID, msgID := q.Message.Chat.ID, q.Message.MessageID
	parts := strings.Split(q.Data, ":")
	if len(parts) < 7 {
		b.AnswerCallback(q.ID, "参数缺失")
		return
	}
	uid, err1 := strconv.ParseInt(parts[4], 10, 64)
	mask, err2 := strconv.Atoi(parts[5])
	if err1 != nil || err2 != nil {
		b.AnswerCallback(q.ID, "参数无效")
		return
	}
	action := parts[6]

	if action == "y" {
		if mask == 0 {
			b.AnswerCallback(q.ID, "至少选一种端点")
			return
		}
		createUpstreamFromDraft(b, q, chatID, msgID, uid, upstream.KindOpenAI,
			mask&1 != 0, mask&2 != 0)
		return
	}

	// 切换某一位。上界跟着 mask 的位数走——漏掉扩容会让新按钮点下去
	// 只报「参数无效」。
	bit, err := strconv.Atoi(action)
	if err != nil || bit < 0 || bit > 1 {
		b.AnswerCallback(q.ID, "参数无效")
		return
	}
	b.AnswerCallback(q.ID, "")
	showUpstreamTypePicker(b, chatID, msgID, uid, mask^(1<<bit))
}

func handleUpstreamEditInput(b *core.Bot, m *tg.Message, p core.PendingInput, text string) {
	chatID := m.Chat.ID
	id, err := strconv.ParseInt(p.Target, 10, 64)
	if err != nil {
		b.DropPending(m.From.ID)
		b.Send(chatID, "目标上游 ID 无效，操作取消。", nil)
		return
	}

	var patch core.UpstreamPatch
	done := "✅ 已更新"
	switch p.Op {
	case "up_edit_url":
		patch.BaseURL = &text
	case "up_edit_name":
		patch.Name = &text
		done = "✅ 已改名。引用它的模型名与默认模型设置已一并更新。"
	case "up_edit_key":
		patch.APIKey = &text
	case "up_edit_weight":
		w, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			b.Send(chatID, "权重必须是正整数（&gt;0），请重新输入：", nil)
			return
		}
		patch.Weight = &w
	default:
		b.DropPending(m.From.ID)
		return
	}
	if err := b.UpdateUpstream(id, patch); err != nil {
		var op *core.OpError
		if errors.As(err, &op) {
			// 输入不合法：保留会话，让管理员直接重填。
			b.Send(chatID, "❌ "+html.EscapeString(op.Msg)+"，请重新输入：", nil)
			return
		}
		b.DropPending(m.From.ID)
		b.Send(chatID, opText(err, "更新失败。"), nil)
		return
	}
	b.DropPending(m.From.ID)
	b.Send(chatID, done, nil)
	showUpstreamDetail(b, chatID, 0, id)
}

// toggleUpstreamSupports 翻转一个端点开关。chat-only 渠道的能力是类型
// 决定的，开关按钮在详情里也不显示；这里再挡一道，防止旧消息里的按钮
// 回调绕过 UI（UpdateUpstream 会把它静默收敛回只开 chat）。
func toggleUpstreamSupports(b *core.Bot, id int64, which string) error {
	u := upstreamOf(b, id)
	if u == nil {
		return core.Bad("上游不存在")
	}
	if kind := u.EffectiveKind(); kind.ChatOnly() {
		return core.Bad("%s 只能用于复判（chat），没有可切换的端点", kind.Label())
	}
	switch which {
	case "chat":
		return b.UpdateUpstream(id, core.UpstreamPatch{Chat: ptr(!u.SupportsChat)})
	case "so":
		return b.UpdateUpstream(id, core.UpstreamPatch{SystemOne: ptr(!u.SupportsSystemOne)})
	}
	return core.Bad("未知字段: %s", which)
}

// setUpstreamKind 切换渠道类型；能力按新类型重置（见 UpdateUpstream）。
func setUpstreamKind(b *core.Bot, id int64, raw string) error {
	kind, err := upstream.ParseKind(raw)
	if err != nil {
		return core.Bad("%s", err.Error())
	}
	return b.UpdateUpstream(id, core.UpstreamPatch{Kind: &kind})
}

func toggleUpstreamStatus(b *core.Bot, id int64) error {
	u := upstreamOf(b, id)
	if u == nil {
		return core.Bad("上游不存在")
	}
	return b.UpdateUpstream(id, core.UpstreamPatch{Enabled: ptr(u.Status == 0)})
}

func upstreamOf(b *core.Bot, id int64) *upstream.Upstream {
	for _, u := range b.Cache.Snap().Upstreams {
		if u.ID == id {
			return u
		}
	}
	return nil
}
