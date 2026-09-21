package tg

import "strings"

// URLBtn 把一个链接包装成 InlineKB 能识别的按钮值。
//
// 用前缀约定而不是给 InlineKB 换一套参数类型：url 按钮只有自助解除
// 那一处在用，为它把几十个调用点的类型全改一遍不划算。
const urlBtnPrefix = "\x00url\x00"

func URLBtn(u string) string { return urlBtnPrefix + u }

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
