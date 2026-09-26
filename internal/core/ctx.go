package core

// CtxMsg 是送检载荷里的一条历史发言（recent_context / review_history）。
// 字段名即 JSON 键名，模型直接读它。
type CtxMsg struct {
	Name string `json:"user"`
	Text string `json:"text"`
	At   int64  `json:"-"`
}

// TruncateRunes 按 rune 截断，不切碎 UTF-8。
func TruncateRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}
