package tg

import "strings"

// URLBtn 把一个链接包装成 InlineKB 能识别的按钮值。
//
// 用前缀约定而非给 InlineKB 增加参数类型：url 按钮仅自助解除一处使用。
const urlBtnPrefix = "\x00url\x00"

func URLBtn(u string) string { return urlBtnPrefix + u }

// KBAppend 在已有的 InlineKeyboardMarkup 末尾追加一行按钮。
func KBAppend(kb map[string]any, row [][2]string) map[string]any {
	if kb == nil {
		return InlineKB(row)
	}
	rows, _ := kb["inline_keyboard"].([][]map[string]string)
	r := make([]map[string]string, 0, len(row))
	for _, btn := range row {
		if u, ok := strings.CutPrefix(btn[1], urlBtnPrefix); ok {
			r = append(r, map[string]string{"text": btn[0], "url": u})
			continue
		}
		r = append(r, map[string]string{"text": btn[0], "callback_data": btn[1]})
	}
	kb["inline_keyboard"] = append(rows, r)
	return kb
}

// InlineKB 构造 InlineKeyboardMarkup。每个 button 是 [显示文本, 值]，
// 值默认是 callback_data；用 URLBtn() 包过的则渲染成链接按钮。
func InlineKB(rows ...[][2]string) map[string]any {
	kb := make([][]map[string]string, 0, len(rows))
	for _, row := range rows {
		r := make([]map[string]string, 0, len(row))
		for _, btn := range row {
			if u, ok := strings.CutPrefix(btn[1], urlBtnPrefix); ok {
				r = append(r, map[string]string{"text": btn[0], "url": u})
				continue
			}
			r = append(r, map[string]string{"text": btn[0], "callback_data": btn[1]})
		}
		kb = append(kb, r)
	}
	return map[string]any{"inline_keyboard": kb}
}
