package panel

import (
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"

	"menshen/internal/core"
	"menshen/internal/store"
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
			b.AnswerCallback(q.ID, "切换失败")
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
		if names := modelsOfUpstream(b.Cache.Snap(), id); len(names) > 0 {
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
		if _, err := b.Store.Write.Exec(`DELETE FROM upstreams WHERE id=?`, id); err != nil {
			b.AnswerCallback(q.ID, "删除失败")
			return
		}
		if err := b.Cache.Reload(); err != nil {
			slog.Error("删除上游后 reload 失败", "id", id, "err", err)
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

// upstreamCaps 把端点开关渲染成一行人话。
func upstreamCaps(u *upstream.Upstream) string {
	var caps []string
	if u.SupportsChat {
		caps = append(caps, "chat")
	}
	if u.SupportsSystemOne {
		caps = append(caps, "systemone")
	}
	if len(caps) == 0 {
		return "无（该上游不会被选中）"
	}
	return strings.Join(caps, ", ")
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
	sb.WriteString("Base URL: " + html.EscapeString(up.BaseURL) + "\n")
	sb.WriteString("API Key: <code>" + upstream.MaskKey(up.APIKey) + "</code>\n")
	fmt.Fprintf(&sb, "权重: %d\n状态: %s\n\n", up.Weight, status)
	fmt.Fprintf(&sb, "supports_chat: %s\n", mark(up.SupportsChat))
	fmt.Fprintf(&sb, "supports_systemone: %s\n", mark(up.SupportsSystemOne))

	rows := [][][2]string{
		{
			{"🔄 Chat", fmt.Sprintf("a:up:%d:t:chat", id)},
			{"🔄 SystemOne", fmt.Sprintf("a:up:%d:t:so", id)},
		},
		{
			{"📝 改名称", fmt.Sprintf("a:up:%d:e:name", id)},
			{"📝 改 URL", fmt.Sprintf("a:up:%d:e:url", id)},
		},
		{
			{"🔑 改 Key", fmt.Sprintf("a:up:%d:e:key", id)},
			{"⚖️ 改权重", fmt.Sprintf("a:up:%d:e:w", id)},
		},
		{
			{"🔄 启停", fmt.Sprintf("a:up:%d:s", id)},
			{"🗑 删除", fmt.Sprintf("a:up:%d:d", id)},
		},
		{{"◀️ 返回", "a:up"}},
	}
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
		if !strings.HasPrefix(text, "http://") && !strings.HasPrefix(text, "https://") {
			b.Send(chatID, "base_url 必须以 http:// 或 https:// 开头，请重新输入：", nil)
			return
		}
		text = strings.TrimSuffix(text, "/")
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
		// 默认只勾 chat：多数渠道认这条路径，而 systemone 是 TypeSafe
		// 特有的，默认勾上只会换来一堆 404。
		showUpstreamTypePicker(b, chatID, 0, m.From.ID, 0b01)
	}
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
	text := "🔌 <b>新增上游 — 选择支持的端点</b>\n\n" +
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
		raw, ok := b.UpstreamNewDraft.Load(uid)
		if !ok {
			b.AnswerCallback(q.ID, "会话已过期，请重新添加")
			b.Edit(chatID, msgID, "会话已过期，请重新点击「新增上游」。",
				tg.InlineKB([][2]string{{"◀️ 返回", "a:up"}}))
			return
		}
		b.UpstreamNewDraft.Delete(uid)
		dp := strings.Split(raw.(string), "\x00")
		if len(dp) != 3 {
			b.AnswerCallback(q.ID, "数据异常")
			return
		}
		_, err := b.Store.Write.Exec(
			`INSERT INTO upstreams (name,base_url,api_key,weight,status,
			 supports_chat,supports_systemone) VALUES (?,?,?,1,1,?,?)`,
			dp[0], dp[1], dp[2], boolToInt(mask&1 != 0), boolToInt(mask&2 != 0))
		if err != nil {
			slog.Error("新增上游失败", "err", err)
			b.AnswerCallback(q.ID, "新增失败")
			b.Edit(chatID, msgID, "新增失败："+html.EscapeString(err.Error()),
				tg.InlineKB([][2]string{{"◀️ 返回", "a:up"}}))
			return
		}
		if err := b.Cache.Reload(); err != nil {
			slog.Error("新增上游后 reload 失败", "err", err)
		}
		b.AnswerCallback(q.ID, "✅ 已添加")
		showUpstreamList(b, chatID, msgID)
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

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func handleUpstreamEditInput(b *core.Bot, m *tg.Message, p core.PendingInput, text string) {
	chatID := m.Chat.ID
	id, err := strconv.ParseInt(p.Target, 10, 64)
	if err != nil {
		b.DropPending(m.From.ID)
		b.Send(chatID, "目标上游 ID 无效，操作取消。", nil)
		return
	}

	// col 是要更新的列，val 是已校验的值。校验失败一律 return 并保留
	// 会话，让管理员直接重填。
	var col string
	var val any

	switch p.Op {
	case "up_edit_url":
		if !strings.HasPrefix(text, "http://") && !strings.HasPrefix(text, "https://") {
			b.Send(chatID, "base_url 必须以 http:// 或 https:// 开头，请重新输入：", nil)
			return
		}
		col, val = "base_url", strings.TrimSuffix(text, "/")

	case "up_edit_name":
		name := strings.TrimSpace(text)
		if err := core.ValidUpstreamName(name); err != nil {
			b.Send(chatID, "❌ "+err.Error()+"，请重新输入：", nil)
			return
		}
		if err := b.RenameUpstream(id, name); err != nil {
			b.Send(chatID, "改名失败："+html.EscapeString(err.Error()), nil)
			return
		}
		b.DropPending(m.From.ID)
		b.Send(chatID, "✅ 已改名。引用它的模型名与默认模型设置已一并更新。", nil)
		showUpstreamDetail(b, chatID, 0, id)
		return

	case "up_edit_key":
		if text == "" {
			b.Send(chatID, "api_key 不能为空，请重新输入：", nil)
			return
		}
		col, val = "api_key", text

	case "up_edit_weight":
		w, err := strconv.ParseInt(text, 10, 64)
		if err != nil || w <= 0 {
			// 权重 0 会让候选池总权重为 0，加权选择退化成除零，必须拒绝
			b.Send(chatID, "权重必须是正整数（&gt;0），请重新输入：", nil)
			return
		}
		col, val = "weight", w

	default:
		b.DropPending(m.From.ID)
		return
	}

	b.DropPending(m.From.ID)
	// col 只可能取上面几个字面量，不存在注入面。
	if _, err := b.Store.Write.Exec(
		`UPDATE upstreams SET `+col+`=? WHERE id=?`, val, id); err != nil {
		b.Send(chatID, "更新失败："+html.EscapeString(err.Error()), nil)
		return
	}
	if err := b.Cache.Reload(); err != nil {
		slog.Error("改上游后 reload 失败", "id", id, "col", col, "err", err)
	}
	b.Send(chatID, "✅ 已更新", nil)
	showUpstreamDetail(b, chatID, 0, id)
}

// modelsOfUpstream 返回绑在这个上游名下的模型名（全名，稳定排序）。
func modelsOfUpstream(snap *store.Snapshot, id int64) []string {
	var name string
	for _, u := range snap.Upstreams {
		if u.ID == id {
			name = u.Name
			break
		}
	}
	if name == "" {
		return nil
	}
	var out []string
	for full := range snap.Models {
		if up, _ := upstream.SplitModelName(full); up == name {
			out = append(out, full)
		}
	}
	sortStrings(out)
	return out
}

func toggleUpstreamSupports(b *core.Bot, id int64, which string) error {
	var field string
	switch which {
	case "chat":
		field = "supports_chat"
	case "so":
		field = "supports_systemone"
	default:
		return fmt.Errorf("未知字段: %s", which)
	}
	return toggleUpstreamColumn(b, id, field)
}

func toggleUpstreamStatus(b *core.Bot, id int64) error {
	return toggleUpstreamColumn(b, id, "status")
}

// toggleUpstreamColumn 翻转一个 0/1 列。field 只来自上面两个函数里的
// 字面量，不存在注入面。
func toggleUpstreamColumn(b *core.Bot, id int64, field string) error {
	var cur int64
	if err := b.Store.Read.QueryRow(
		`SELECT `+field+` FROM upstreams WHERE id=?`, id).Scan(&cur); err != nil {
		return err
	}
	next := int64(0)
	if cur == 0 {
		next = 1
	}
	if _, err := b.Store.Write.Exec(
		`UPDATE upstreams SET `+field+`=? WHERE id=?`, next, id); err != nil {
		return err
	}
	if err := b.Cache.Reload(); err != nil {
		slog.Error("切换上游开关后 reload 失败", "id", id, "field", field, "err", err)
		return err
	}
	return nil
}
