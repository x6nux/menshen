package panel

import "testing"

// TestActionLabel 覆盖完整词汇表。漏掉任何一个值，面板会直接显示原始字符串。
func TestActionLabel(t *testing.T) {
	cases := map[string]string{
		"none": "未处置", "alerted": "仅告警", "deleted": "已删除",
		"muted": "已禁言", "deleted_muted": "已删除+禁言",
		"banned": "已封禁", "undone": "已标记误判",
		"dryrun:deleted_muted": "演练（本应已删除+禁言）",
	}
	for in, want := range cases {
		if got := actionLabel(in); got != want {
			t.Errorf("actionLabel(%q) = %q, 期望 %q", in, got, want)
		}
	}
}
