package logbuf

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestBufferEvictsOldest：缓冲写满后覆盖最旧的一条，序号继续递增。
// 网页版日志页读的就是它，溢出策略直接决定「能看到多久以前」。
func TestBufferEvictsOldest(t *testing.T) {
	b := New(3)
	for i := 1; i <= 5; i++ {
		b.Add(Record{Message: string(rune('a' + i - 1)), Time: time.Unix(int64(i), 0)})
	}
	if b.Len() != 3 {
		t.Fatalf("容量 3 应只留 3 条，得到 %d", b.Len())
	}
	snap := b.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("快照应有 3 条，得到 %d", len(snap))
	}
	// 旧→新：c、d、e，且序号是连续的 3/4/5。
	if snap[0].Message != "c" || snap[2].Message != "e" {
		t.Errorf("应保留最新的 c/d/e，得到 %s..%s", snap[0].Message, snap[2].Message)
	}
	if snap[0].Seq != 3 || snap[2].Seq != 5 {
		t.Errorf("序号应从 3 递增到 5，得到 %d..%d", snap[0].Seq, snap[2].Seq)
	}
}

// TestQueryLevelIsFloor：级别筛选是「不低于」语义 —— 选 WARN 要同时看到
// WARN 与 ERROR，这是运维看日志的默认预期。
func TestQueryLevelIsFloor(t *testing.T) {
	b := New(10)
	b.Add(Record{Level: slog.LevelDebug, Message: "d"})
	b.Add(Record{Level: slog.LevelInfo, Message: "i"})
	b.Add(Record{Level: slog.LevelWarn, Message: "w"})
	b.Add(Record{Level: slog.LevelError, Message: "e"})

	items, total, counts := b.Query(Query{MinLevel: slog.LevelWarn, Limit: 10})
	if total != 2 || len(items) != 2 {
		t.Fatalf("WARN 下限应命中 2 条，得到 total=%d len=%d", total, len(items))
	}
	// 新→旧：ERROR 在 WARN 前。
	if items[0].Message != "e" || items[1].Message != "w" {
		t.Errorf("应按新→旧排列，得到 %v", messages(items))
	}
	// counts 不受级别筛选影响：始终是搜索命中的各档条数。
	if counts.Debug != 1 || counts.Info != 1 || counts.Warn != 1 || counts.Error != 1 {
		t.Errorf("counts 应覆盖全部级别，得到 %+v", counts)
	}

	if _, total, _ := b.Query(Query{MinLevel: slog.LevelDebug, Limit: 10}); total != 4 {
		t.Errorf("下限 DEBUG 应命中全部 4 条，得到 %d", total)
	}
}

// TestQuerySearch：搜索覆盖消息与字段（键与值），大小写不敏感。
func TestQuerySearch(t *testing.T) {
	b := New(10)
	b.Add(Record{Level: slog.LevelInfo, Message: "webhook 已注册",
		Attrs: []Attr{{Key: "bot", Value: "menshen_demo"}}})
	b.Add(Record{Level: slog.LevelError, Message: "判定失败",
		Attrs: []Attr{{Key: "err", Value: "context deadline exceeded"}}})
	b.Add(Record{Level: slog.LevelInfo, Message: "无关"})

	if _, total, _ := b.Query(Query{Search: "WEBHOOK", Limit: 10}); total != 1 {
		t.Errorf("消息搜索应大小写不敏感命中 1 条，得到 %d", total)
	}
	if items, _, _ := b.Query(Query{Search: "deadline", Limit: 10}); len(items) != 1 ||
		items[0].Message != "判定失败" {
		t.Errorf("字段值搜索应命中错误行，得到 %v", messages(items))
	}
	if _, total, counts := b.Query(Query{Search: "bot", Limit: 10}); total != 1 || counts.Info != 1 {
		t.Errorf("字段键搜索应命中 1 条并计入 INFO，得到 total=%d counts=%+v", total, counts)
	}
}

// TestQueryPagination：分页按新→旧切，total 是过滤后的总数（不含分页）。
func TestQueryPagination(t *testing.T) {
	b := New(10)
	for i := 1; i <= 5; i++ {
		b.Add(Record{Level: slog.LevelInfo, Message: string(rune('0' + i))})
	}
	page1, total, _ := b.Query(Query{Limit: 2, Offset: 0})
	page2, _, _ := b.Query(Query{Limit: 2, Offset: 2})
	if total != 5 {
		t.Fatalf("total 应为 5，得到 %d", total)
	}
	if len(page1) != 2 || page1[0].Message != "5" || page1[1].Message != "4" {
		t.Errorf("第一页应是最新的 5、4，得到 %v", messages(page1))
	}
	if len(page2) != 2 || page2[0].Message != "3" || page2[1].Message != "2" {
		t.Errorf("第二页应是 3、2，得到 %v", messages(page2))
	}
}

// TestLevelNameAndParse：级别名与解析互为逆（all/未知值都落到 DEBUG 下限）。
func TestLevelNameAndParse(t *testing.T) {
	cases := []struct {
		level slog.Level
		name  string
	}{
		{slog.LevelDebug, "DEBUG"},
		{slog.LevelInfo, "INFO"},
		{slog.LevelWarn, "WARN"},
		{slog.LevelError, "ERROR"},
	}
	for _, c := range cases {
		if got := LevelName(c.level); got != c.name {
			t.Errorf("LevelName(%v) = %q，期望 %q", c.level, got, c.name)
		}
		if got := ParseLevel(strings.ToLower(c.name)); got != c.level {
			t.Errorf("ParseLevel(%q) = %v，期望 %v", c.name, got, c.level)
		}
	}
	if ParseLevel("") != slog.LevelDebug || ParseLevel("all") != slog.LevelDebug ||
		ParseLevel("nonsense") != slog.LevelDebug {
		t.Error("空/全部/未知值都应按 DEBUG 下限（即不过滤）")
	}
	if ParseLevel("warning") != slog.LevelWarn {
		t.Error("warning 应等价 warn")
	}
}

// TestHandlerCapturesAttrsAndGroups：Handler 把消息、字段与 WithGroup 前缀
// 都写进缓冲，并原样透传给下游 handler（控制台输出不受影响）。
func TestHandlerCapturesAttrsAndGroups(t *testing.T) {
	b := New(10)
	var console strings.Builder
	next := slog.NewTextHandler(&console, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(NewHandler(b, next, slog.LevelDebug))

	logger.With("bot", "demo").WithGroup("ai").Info("判定完成", "model", "gpt-x", "ok", true)
	// DEBUG 低于控制台级别：进缓冲、不进控制台。
	logger.Debug("被控制台忽略", "n", 1)

	snap := b.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("缓冲应有 2 条（含 DEBUG），得到 %d", len(snap))
	}
	first := snap[0]
	if first.Message != "判定完成" {
		t.Fatalf("首条消息不对：%q", first.Message)
	}
	got := map[string]string{}
	for _, a := range first.Attrs {
		got[a.Key] = a.Value
	}
	if got["bot"] != "demo" || got["ai.model"] != "gpt-x" || got["ai.ok"] != "true" {
		t.Errorf("字段应带上组前缀，得到 %+v", got)
	}
	if snap[1].Level != slog.LevelDebug {
		t.Errorf("DEBUG 行应被缓冲，得到 %v", snap[1].Level)
	}
	if out := console.String(); strings.Contains(out, "被控制台忽略") {
		t.Errorf("DEBUG 行不应写进控制台，得到 %q", out)
	} else if !strings.Contains(out, "判定完成") {
		t.Errorf("INFO 行应写进控制台，得到 %q", out)
	}
}

// TestHandlerEnabledRespectsCaptureFloor：低于采集下限的级别直接不进缓冲。
func TestHandlerEnabledRespectsCaptureFloor(t *testing.T) {
	h := NewHandler(New(4), nil, slog.LevelInfo)
	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("采集下限 INFO 时 DEBUG 不应启用")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("ERROR 应启用")
	}
}

func messages(rs []Record) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Message
	}
	return out
}
