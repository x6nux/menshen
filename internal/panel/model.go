package panel

import (
	"fmt"
	"html"
	"log/slog"
	"math"
	"strconv"
	"strings"

	"menshen/internal/core"
	"menshen/internal/tg"
)

// 列表分页大小。TG 单条消息容得下 10 条模型的四项价格。
const modelPageSize = 10

// modelPriceFields 把 callback 里的短码映射到 DB 列与展示名。
// 短码刻意用 2-3 字符，为 callback_data 里的模型名腾出预算。
var modelPriceFields = map[string]struct {
	col   string
	label string
}{
	"pp":  {"prompt_price", "输入"},
	"cp":  {"completion_price", "补全"},
	"crp": {"cache_read_price", "缓存读取"},
	"cwp": {"cache_write_price", "缓存创建"},
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
	case "new": // a:md:new - 开始五步输入
		b.AnswerCallback(q.ID, "")
		b.AskInput(chatID, q.From.ID, "md_new_name", "",
			"请输入模型名（需与上游一致，例如 gpt-5-mini）：")

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
		if err := toggleModelEnabled(b, arg); err != nil {
			b.AnswerCallback(q.ID, "切换失败")
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
		if _, err := b.Store.Write.Exec(`DELETE FROM models WHERE name=?`, arg); err != nil {
			slog.Error("删除模型失败", "name", arg, "err", err)
			b.AnswerCallback(q.ID, "删除失败")
			return
		}
		if err := b.Cache.Reload(); err != nil {
			slog.Error("删除模型后 reload 失败", "name", arg, "err", err)
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
		sb.WriteString("暂无模型。\n\n模型表只用于把判定开销折算成钱——" +
			"名称必须与上游一致，反广告面板选模型时会按它校验。")
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
		if text == "" {
			b.Send(chatID, "模型名不能为空，请重新输入：", nil)
			return
		}
		// callback_data 上限 64 字节，"a:md:e:crp:" + name 是最长形式
		if len(text)+len("a:md:e:crp:") > 64 {
			b.DropPending(m.From.ID)
			b.Send(chatID, fmt.Sprintf(
				"模型名过长（%d 字节）。受 Telegram callback_data 64 字节限制，"+
					"模型名不得超过 %d 字节。", len(text), 64-len("a:md:e:crp:")), nil)
			return
		}
		if b.Cache.Snap().Models[text] != nil {
			b.DropPending(m.From.ID)
			b.Send(chatID, "该模型已存在，请到列表中编辑它。", nil)
			return
		}
		b.AskInput(chatID, m.From.ID, "md_new_pp", text,
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
		name := fields[0]
		prices := make([]float64, 4)
		for i := range prices {
			v, err := parsePrice(fields[i+1])
			if err != nil {
				b.Send(chatID, "会话中的价格数据异常，请重新点击「新增模型」。", nil)
				return
			}
			prices[i] = v
		}

		_, err := b.Store.Write.Exec(
			`INSERT INTO models (name,prompt_price,completion_price,
			 cache_read_price,cache_write_price,enabled) VALUES (?,?,?,?,?,1)`,
			name, prices[0], prices[1], prices[2], prices[3])
		if err != nil {
			slog.Error("新增模型失败", "name", name, "err", err)
			b.Send(chatID, "新增失败："+html.EscapeString(err.Error()), nil)
			return
		}
		if err := b.Cache.Reload(); err != nil {
			slog.Error("新增模型后 reload 失败", "name", name, "err", err)
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

	// f.col 取自 modelPriceFields 的字面量，不存在注入面。
	if _, err := b.Store.Write.Exec(
		`UPDATE models SET `+f.col+`=? WHERE name=?`, price, name); err != nil {
		slog.Error("更新模型价格失败", "name", name, "col", f.col, "err", err)
		b.Send(chatID, "更新失败："+html.EscapeString(err.Error()), nil)
		return
	}
	if err := b.Cache.Reload(); err != nil {
		slog.Error("改模型价格后 reload 失败", "name", name, "err", err)
	}
	b.Send(chatID, "✅ 已更新"+f.label+"价为 $"+formatPrice(price)+" /M tokens。", nil)
	showModelDetail(b, chatID, 0, name)
}

func toggleModelEnabled(b *core.Bot, name string) error {
	var cur int64
	if err := b.Store.Read.QueryRow(
		`SELECT enabled FROM models WHERE name=?`, name).Scan(&cur); err != nil {
		return err
	}
	next := int64(0)
	if cur == 0 {
		next = 1
	}
	if _, err := b.Store.Write.Exec(
		`UPDATE models SET enabled=? WHERE name=?`, next, name); err != nil {
		return err
	}
	if err := b.Cache.Reload(); err != nil {
		slog.Error("切换模型启用状态后 reload 失败", "name", name, "err", err)
		return err
	}
	return nil
}

// parsePrice 解析管理员输入的 $/M tokens 价格。必须非负——负价格会让开销
// 核算变成负数，是配置事故而非合法用法。
func parsePrice(s string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("价格格式错误，需为非负数字")
	}
	if v < 0 {
		return 0, fmt.Errorf("价格必须为非负数")
	}
	return v, nil
}

// formatPrice 去掉尾随 0，让 0.1500 显示为 0.15、3.0000 显示为 3。
func formatPrice(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
