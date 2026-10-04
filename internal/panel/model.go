package panel

import (
	"fmt"
	"html"
	"strconv"
	"strings"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
	"menshen/internal/upstream"
)

// 列表分页大小。TG 单条消息容得下 10 条模型的四项价格。
const modelPageSize = 10

// modelPriceFields 把 callback 里的短码映射到价格序号（同 core.ModelPriceCols）
// 与展示名。短码刻意用 2-3 字符，为 callback_data 里的模型名腾出预算。
var modelPriceFields = map[string]struct {
	idx   int
	label string
}{
	"pp":  {0, "输入"},
	"cp":  {1, "补全"},
	"crp": {2, "缓存读取"},
	"cwp": {3, "缓存创建"},
}

// handleModelCallback 处理 a:md:*。模型名放在 callback_data 的最后一段，
// 因为模型名本身常含冒号（qwen3:30b、OpenRouter 的 :free），放中间会被切碎。
func handleModelCallback(b *core.Bot, q *tg.CallbackQuery) {
	chatID, msgID := q.Message.Chat.ID, q.Message.MessageID

	if q.Data == "a:md" { // 列表首页
		b.AnswerCallback(q.ID, "")
		showModelList(b, chatID, msgID, 1)
		return
	}
	verb, arg, _ := strings.Cut(strings.TrimPrefix(q.Data, "a:md:"), ":")

	switch verb {
	case "new": // a:md:new - 先选上游
		b.AnswerCallback(q.ID, "")
		showModelUpstreamPick(b, chatID, msgID)

	case "newu": // a:md:newu:<上游ID> - 选定上游，开始输入模型 ID
		b.AnswerCallback(q.ID, "")
		upID, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			b.AnswerCallback(q.ID, "参数无效")
			return
		}
		var up *upstream.Upstream
		for _, u := range b.Cache.Snap().Upstreams {
			if u.ID == upID && u.Status == 1 {
				up = u
				break
			}
		}
		if up == nil {
			b.EditOrSend(chatID, msgID, "该上游不存在或已停用。",
				tg.InlineKB([][2]string{{"◀️ 返回", "a:md"}}))
			return
		}
		b.AskInput(chatID, q.From.ID, "md_new_name", up.Name,
			"上游：<b>"+html.EscapeString(up.Name)+"</b>\n\n"+
				"请输入<b>模型 ID</b>（发给上游的 model 字段，需与上游一致，"+
				"例如 <code>gpt-5-mini</code>；OpenRouter 形态如 "+
				"<code>openai/gpt-4o</code>）：")

	case "p": // a:md:p:<page> - 翻页
		page := 1
		if n, err := strconv.Atoi(arg); err == nil && n > 0 {
			page = n
		}
		b.AnswerCallback(q.ID, "")
		showModelList(b, chatID, msgID, page)

	case "v": // a:md:v:<name> - 详情面板
		b.AnswerCallback(q.ID, "")
		showModelDetail(b, chatID, msgID, arg)

	case "e": // a:md:e:<field>:<name> - 编辑单项价格
		field, name, ok := strings.Cut(arg, ":")
		f, known := modelPriceFields[field]
		if !ok || !known {
			b.AnswerCallback(q.ID, "未知价格项")
			return
		}
		b.AnswerCallback(q.ID, "")
		b.AskInput(chatID, q.From.ID, "md_edit_"+field, name,
			"请输入模型 "+html.EscapeString(name)+" 的新<b>"+f.label+
				"</b>价（$ / 1M tokens，非负数）：")

	case "s": // a:md:s:<name> - 启用/停用
		m := b.Cache.Snap().Models[arg]
		if m == nil {
			b.AnswerCallback(q.ID, "模型不存在")
			return
		}
		if err := b.UpdateModel(arg, core.ModelPatch{Enabled: ptr(!m.Enabled)}); err != nil {
			b.AnswerCallback(q.ID, opText(err, "切换失败"))
			return
		}
		b.AnswerCallback(q.ID, "已切换")
		showModelDetail(b, chatID, msgID, arg)

	case "d": // a:md:d:<name> - 删除前确认
		b.AnswerCallback(q.ID, "")
		b.Edit(chatID, msgID,
			"⚠️ 确认删除模型 <code>"+html.EscapeString(arg)+"</code>？\n\n"+
				"删除后它的判定开销将无法核算（判定本身仍会照跑）。",
			tg.InlineKB(
				[][2]string{{"✅ 确认删除", "a:md:dy:" + arg}},
				[][2]string{{"◀️ 取消", "a:md:v:" + arg}},
			))

	case "dy": // a:md:dy:<name> - 确认后执行删除
		if err := b.DeleteModel(arg); err != nil {
			b.AnswerCallback(q.ID, opText(err, "删除失败"))
			return
		}
		b.AnswerCallback(q.ID, "已删除")
		showModelList(b, chatID, msgID, 1)

	default:
		b.AnswerCallback(q.ID, "")
	}
}

func showModelList(b *core.Bot, chatID, msgID int64, page int) {
	snap := b.Cache.Snap()
	names := make([]string, 0, len(snap.Models))
	for name := range snap.Models {
		names = append(names, name)
	}
	sortStrings(names) // 顺序稳定，翻页才不会漏项或重复

	total := len(names)
	pages := (total + modelPageSize - 1) / modelPageSize
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	start := (page - 1) * modelPageSize
	end := min(start+modelPageSize, total)

	var sb strings.Builder
	sb.WriteString("🤖 <b>模型定价</b>\n价格单位：$ / 1M tokens\n\n")
	if total == 0 {
		sb.WriteString("暂无模型。\n\n模型名格式为 <code>上游名/模型ID</code>" +
			"（如 <code>openrouter/openai/gpt-4o</code>）：名字决定它走哪个上游，" +
			"价格用于把判定开销折算成钱。")
	}
	for _, name := range names[start:end] {
		m := snap.Models[name]
		mark := "✅"
		if !m.Enabled {
			mark = "❌"
		}
		sb.WriteString(mark + " <b>" + html.EscapeString(name) + "</b>\n")
		fmt.Fprintf(&sb, "  输入 $%s ｜ 补全 $%s\n",
			formatPrice(m.PromptPrice), formatPrice(m.CompletionPrice))
		fmt.Fprintf(&sb, "  缓存读 $%s ｜ 缓存写 $%s\n",
			formatPrice(m.CacheReadPrice), formatPrice(m.CacheWritePrice))
	}
	if n := legacyModelCount(snap); n > 0 {
		fmt.Fprintf(&sb, "\n⚠️ 有 %d 个旧格式模型（无上游前缀），会任选可用上游；"+
			"建议删掉后按 <code>上游名/模型ID</code> 重新添加。\n", n)
	}
	if total > 0 {
		fmt.Fprintf(&sb, "\n第 %d / %d 页，共 %d 个模型。点击下方按钮查看详情。",
			page, pages, total)
	}

	rows := make([][][2]string, 0, len(names[start:end])+3)
	for _, name := range names[start:end] {
		rows = append(rows, [][2]string{{name, "a:md:v:" + name}})
	}
	if pages > 1 {
		nav := make([][2]string, 0, 3)
		if page > 1 {
			nav = append(nav, [2]string{"◀️", fmt.Sprintf("a:md:p:%d", page-1)})
		}
		nav = append(nav, [2]string{fmt.Sprintf("%d/%d", page, pages), "a:noop"})
		if page < pages {
			nav = append(nav, [2]string{"▶️", fmt.Sprintf("a:md:p:%d", page+1)})
		}
		rows = append(rows, nav)
	}
	rows = append(rows,
		[][2]string{{"➕ 新增模型", "a:md:new"}},
		[][2]string{{"◀️ 返回主菜单", "a:main"}},
	)

	b.EditOrSend(chatID, msgID, sb.String(), tg.InlineKB(rows...))
}

// showModelUpstreamPick 让管理员先选上游再输入模型 ID。
//
// 模型名 <上游名>/<模型ID> 是绑定关系的唯一来源：不先定上游就拼不出
// 名字，也就无从区分多上游。
func showModelUpstreamPick(b *core.Bot, chatID, msgID int64) {
	var rows [][][2]string
	skipped := 0
	for _, u := range b.Cache.Snap().Upstreams {
		if u.Status != 1 {
			continue
		}
		if core.ValidUpstreamName(u.Name) != nil {
			skipped++ // 名字含 / 或 : 的旧上游，拼不成前缀
			continue
		}
		rows = append(rows, [][2]string{{
			btnValue(u.Name), fmt.Sprintf("a:md:newu:%d", u.ID)}})
	}
	if len(rows) == 0 {
		msg := "还没有可用的上游。模型名是 <code>&lt;上游名&gt;/&lt;模型ID&gt;</code>，" +
			"新增模型前请先到「🔌 上游渠道」添加并启用一个。"
		if skipped > 0 {
			msg = fmt.Sprintf("有 %d 个上游的名称含 / 或 :，无法作为模型名前缀，请先改名。", skipped)
		}
		b.EditOrSend(chatID, msgID, msg, tg.InlineKB([][2]string{{"◀️ 返回", "a:md"}}))
		return
	}
	msg := "➕ <b>新增模型</b>\n\n先选这个模型走哪个上游（决定模型名前缀）："
	if skipped > 0 {
		msg += fmt.Sprintf("\n\n<i>另有 %d 个上游的名称含 / 或 :，已跳过，请先改名。</i>", skipped)
	}
	rows = append(rows, [][2]string{{"◀️ 返回", "a:md"}})
	b.EditOrSend(chatID, msgID, msg, tg.InlineKB(rows...))
}

func showModelDetail(b *core.Bot, chatID, msgID int64, name string) {
	m := b.Cache.Snap().Models[name]
	if m == nil {
		b.Edit(chatID, msgID, "模型不存在，可能已被删除。",
			tg.InlineKB([][2]string{{"◀️ 返回", "a:md"}}))
		return
	}

	status := "✅启用"
	if !m.Enabled {
		status = "❌停用"
	}

	var sb strings.Builder
	sb.WriteString("🤖 <b>模型：" + html.EscapeString(name) + "</b>  " + status + "\n\n")
	if up, id := upstream.SplitModelName(name); up == "" {
		sb.WriteString("<i>未绑定上游（旧格式）：任选一个支持该端点的上游。" +
			"建议删掉后按 <code>上游名/模型ID</code> 重新添加。</i>\n\n")
	} else {
		sb.WriteString("上游: <code>" + html.EscapeString(up) + "</code>\n")
		sb.WriteString("模型 ID: <code>" + html.EscapeString(id) + "</code>\n\n")
	}
	sb.WriteString("输入      $" + formatPrice(m.PromptPrice) + " /M tokens\n")
	sb.WriteString("补全      $" + formatPrice(m.CompletionPrice) + " /M tokens\n")
	sb.WriteString("缓存读取  $" + formatPrice(m.CacheReadPrice) + " /M tokens\n")
	sb.WriteString("缓存创建  $" + formatPrice(m.CacheWritePrice) + " /M tokens\n\n")
	sb.WriteString("<i>systemone 端点官方输出侧不计费，把补全价配 0 即可。</i>")

	toggleLabel := "❌ 停用"
	if !m.Enabled {
		toggleLabel = "✅ 启用"
	}
	rows := [][][2]string{
		{
			{"📝 输入价 · $" + formatPrice(m.PromptPrice), "a:md:e:pp:" + name},
			{"📝 补全价 · $" + formatPrice(m.CompletionPrice), "a:md:e:cp:" + name},
		},
		{
			{"📝 缓存读取价 · $" + formatPrice(m.CacheReadPrice), "a:md:e:crp:" + name},
			{"📝 缓存创建价 · $" + formatPrice(m.CacheWritePrice), "a:md:e:cwp:" + name},
		},
		{
			{toggleLabel, "a:md:s:" + name},
			{"🗑 删除", "a:md:d:" + name},
		},
		{{"◀️ 返回列表", "a:md"}},
	}
	b.EditOrSend(chatID, msgID, sb.String(), tg.InlineKB(rows...))
}

// handleModelNewInput 串联五步新增：名称 → 输入价 → 补全价 → 缓存读取价 → 缓存创建价。
// 已填内容用 \x00 分隔存在 target 里，最后一步一次性 INSERT。
func handleModelNewInput(b *core.Bot, m *tg.Message, p core.PendingInput, text string) {
	chatID := m.Chat.ID

	if p.Op == "md_new_name" {
		// 先校验一遍名字（与最终落库同一套规则），错了当场重填，
		// 不必等四个价格都填完才知道。
		full, err := core.NewModelName(b.Cache.Snap(), p.Target, text)
		if err != nil {
			b.Send(chatID, "❌ "+html.EscapeString(opText(err, "校验失败"))+"，请重新输入：", nil)
			return
		}
		b.AskInput(chatID, m.From.ID, "md_new_pp", full,
			"模型全名：<code>"+html.EscapeString(full)+"</code>\n\n"+
				"请输入<b>输入价</b>（$ / 1M tokens，非负数，例如 0.15）：")
		return
	}

	if _, err := parsePrice(text); err != nil {
		// 校验失败保留会话，让管理员直接重填
		b.Send(chatID, "价格必须是非负数字（$ / 1M tokens），例如 0.15。请重新输入：", nil)
		return
	}

	// target 形如 name\x00pp\x00cp\x00crp，逐步追加
	acc := p.Target + "\x00" + text

	switch p.Op {
	case "md_new_pp":
		b.AskInput(chatID, m.From.ID, "md_new_cp", acc,
			"请输入<b>补全价</b>（$ / 1M tokens，非负数，例如 0.60）：")

	case "md_new_cp":
		b.AskInput(chatID, m.From.ID, "md_new_crp", acc,
			"请输入<b>缓存读取价</b>（$ / 1M tokens，非负数，通常为输入价的 0.1 倍）：")

	case "md_new_crp":
		b.AskInput(chatID, m.From.ID, "md_new_cwp", acc,
			"请输入<b>缓存创建价</b>（$ / 1M tokens，非负数，没有缓存计费就填 0）：")

	case "md_new_cwp":
		b.DropPending(m.From.ID)
		fields := strings.Split(acc, "\x00")
		if len(fields) != 5 {
			b.Send(chatID, "会话数据异常，请重新点击「新增模型」。", nil)
			return
		}
		var prices [4]float64
		for i := range prices {
			v, err := parsePrice(fields[i+1])
			if err != nil {
				b.Send(chatID, "会话中的价格数据异常，请重新点击「新增模型」。", nil)
				return
			}
			prices[i] = v
		}
		upName, modelID := upstream.SplitModelName(fields[0])
		name, err := b.AddModel(upName, modelID, prices)
		if err != nil {
			b.Send(chatID, "新增失败："+html.EscapeString(opText(err, "内部错误")), nil)
			return
		}
		b.Send(chatID, "✅ 模型 <code>"+html.EscapeString(name)+"</code> 已添加并启用。", nil)
		showModelDetail(b, chatID, 0, name)
	}
}

func handleModelEditInput(b *core.Bot, m *tg.Message, p core.PendingInput, text string) {
	chatID := m.Chat.ID
	name := p.Target

	f, ok := modelPriceFields[strings.TrimPrefix(p.Op, "md_edit_")]
	if !ok {
		b.DropPending(m.From.ID)
		return
	}
	if b.Cache.Snap().Models[name] == nil {
		b.DropPending(m.From.ID)
		b.Send(chatID, "目标模型已不存在，操作取消。", nil)
		return
	}

	price, err := parsePrice(text)
	if err != nil {
		// 保留会话让管理员重填
		b.Send(chatID, "价格必须是非负数字（$ / 1M tokens），例如 0.15。请重新输入：", nil)
		return
	}
	b.DropPending(m.From.ID)

	var patch core.ModelPatch
	patch.Prices[f.idx] = &price
	if err := b.UpdateModel(name, patch); err != nil {
		b.Send(chatID, "更新失败："+html.EscapeString(opText(err, "内部错误")), nil)
		return
	}
	b.Send(chatID, "✅ 已更新"+f.label+"价为 $"+formatPrice(price)+" /M tokens。", nil)
	showModelDetail(b, chatID, 0, name)
}

// legacyModelCount 数一下没有上游前缀的旧格式模型。
func legacyModelCount(snap *store.Snapshot) int {
	n := 0
	for name := range snap.Models {
		if up, _ := upstream.SplitModelName(name); up == "" {
			n++
		}
	}
	return n
}

// parsePrice 解析管理员输入的 $/M tokens 价格。必须非负——负价格会让开销
// 核算变成负数，是配置事故而非合法用法。
func parsePrice(s string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("价格格式错误，需为非负数字")
	}
	return v, core.CheckPrice(v)
}

// formatPrice 去掉尾随 0，让 0.1500 显示为 0.15、3.0000 显示为 3。
func formatPrice(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
