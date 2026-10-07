// Package logbuf 是进程内运行日志的定长环形缓冲。
//
// 它捕获 slog 输出供网页版面板的运行日志页读取：标准输出在容器化部署中
// 不易查看，而排障需要尽快看到最近的错误与上下文。缓冲位于内存，容量固定、
// 写满覆盖最旧；进程重启即清空（更早的日志仍以标准输出为准）。
//
// 依赖只有标准库：main 装 Handler，panel 读 Buffer，两边都只认这一个包。
package logbuf

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// DefaultCapacity 是缓冲的默认容量（条）。2000 条覆盖一次排查窗口，
// 常驻内存量级在 1MB 上下。
const DefaultCapacity = 2000

// Attr 是一条日志的单个结构化字段，Value 已渲染成字符串。
type Attr struct {
	Key   string
	Value string
}

// Record 是缓冲里的一条日志。Seq 由 Buffer 分配，单调递增，供前端做稳定的
// 列表 key（同一条日志的 Seq 在分页之间不变）。
type Record struct {
	Seq     int64
	Time    time.Time
	Level   slog.Level
	Message string
	Attrs   []Attr
}

// Buffer 是固定容量的环形缓冲：写满后新记录覆盖最旧的一条。
type Buffer struct {
	mu    sync.RWMutex
	cap   int
	recs  []Record
	start int // 满时指向最旧一条的下标
	seq   int64
}

// New 建一个容量为 capacity 的缓冲；capacity <= 0 时用 DefaultCapacity。
func New(capacity int) *Buffer {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Buffer{cap: capacity, recs: make([]Record, 0, capacity)}
}

// Add 写入一条记录并分配序号；调用方不用填 Seq。
func (b *Buffer) Add(r Record) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	r.Seq = b.seq
	if len(b.recs) < b.cap {
		b.recs = append(b.recs, r)
		return
	}
	b.recs[b.start] = r
	b.start = (b.start + 1) % b.cap
}

// Len 返回当前条数。
func (b *Buffer) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.recs)
}

// Snapshot 按时间顺序（旧→新）复制一份记录。
func (b *Buffer) Snapshot() []Record {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Record, 0, len(b.recs))
	for i := 0; i < len(b.recs); i++ {
		out = append(out, b.recs[(b.start+i)%b.cap])
	}
	return out
}

// LevelCounts 是四档级别的条数（slog 只有这四档在用）。
type LevelCounts struct {
	Debug, Info, Warn, Error int
}

func (c *LevelCounts) add(l slog.Level) {
	switch {
	case l < slog.LevelInfo:
		c.Debug++
	case l < slog.LevelWarn:
		c.Info++
	case l < slog.LevelError:
		c.Warn++
	default:
		c.Error++
	}
}

// Query 是日志页的过滤与分页参数。
type Query struct {
	// MinLevel 是级别下限（含）：选 WARN 同时看到 WARN 与 ERROR。
	MinLevel slog.Level
	// Search 是大小写不敏感的子串，匹配消息与字段的键和值。
	Search string
	Offset int
	Limit  int
}

// Query 返回过滤后的一页（新→旧）与过滤总数。
//
// counts 只受 Search 影响、不受 MinLevel 影响：界面用它显示本次搜索中各档
// 的条数，据此决定是否收窄级别。
func (b *Buffer) Query(q Query) (items []Record, total int, counts LevelCounts) {
	snap := b.Snapshot()
	needle := strings.ToLower(strings.TrimSpace(q.Search))
	if q.Limit <= 0 {
		q.Limit = len(snap)
	}
	items = make([]Record, 0, min(q.Limit, len(snap)))
	idx := 0
	for i := len(snap) - 1; i >= 0; i-- {
		r := snap[i]
		if needle != "" && !recordMatches(r, needle) {
			continue
		}
		counts.add(r.Level)
		if r.Level < q.MinLevel {
			continue
		}
		if idx >= q.Offset && len(items) < q.Limit {
			items = append(items, r)
		}
		idx++
	}
	return items, idx, counts
}

// recordMatches 报告一条记录是否命中搜索词（needle 必须已小写）。
func recordMatches(r Record, needle string) bool {
	if strings.Contains(strings.ToLower(r.Message), needle) {
		return true
	}
	for _, a := range r.Attrs {
		if strings.Contains(strings.ToLower(a.Key), needle) ||
			strings.Contains(strings.ToLower(a.Value), needle) {
			return true
		}
	}
	return false
}

// LevelName 把 slog 级别渲染成人读的名字；slog 只用这四档。
func LevelName(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "DEBUG"
	case l < slog.LevelWarn:
		return "INFO"
	case l < slog.LevelError:
		return "WARN"
	default:
		return "ERROR"
	}
}

// ParseLevel 解析界面传来的级别名，作为级别下限。空、all 与未知值一律
// 落到 DEBUG（即不过滤）：界面已有全部这一档，无法识别的值按最宽处理，
// 避免出现空列表。
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelDebug
	}
}

// Handler 是 slog.Handler：把记录写进 Buffer，同时透传给下游 handler
// （控制台）。两层各自持有自己的级别：采集下限低于控制台级别时，调试行会
// 进缓冲但不打印 —— 网页上能看到标准输出里没有的上下文。
type Handler struct {
	buf     *Buffer
	next    slog.Handler
	capture slog.Level
	// attrs 是 WithAttrs 累积的字段，建时就按当时的组前缀渲染好 ——
	// 组只作用于其之后新增的字段，不回溯施加于先前累积的字段。
	attrs  []Attr
	prefix string // 当前组前缀，作用于此后新增的记录字段
}

// NewHandler 建一个 Handler；next 可为 nil（只进缓冲），capture 是采集下限。
func NewHandler(buf *Buffer, next slog.Handler, capture slog.Level) *Handler {
	return &Handler{buf: buf, next: next, capture: capture}
}

// Enabled 只看采集下限：控制台是否打印由 next 在自己的 Handle 里再判一次。
func (h *Handler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.capture
}

// Handle 组装记录（含 WithAttrs/WithGroup 累积的字段）写进缓冲，再透传。
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	rec := Record{Time: r.Time, Level: r.Level, Message: r.Message}
	rec.Attrs = append(rec.Attrs, h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		rec.Attrs = appendAttr(rec.Attrs, h.prefix, a)
		return true
	})
	h.buf.Add(rec)
	if h.next != nil && h.next.Enabled(ctx, r.Level) {
		return h.next.Handle(ctx, r)
	}
	return nil
}

// WithAttrs 累积字段：缓冲侧按当前前缀立即渲染，透传侧交给下游 handler。
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	nh := *h
	nh.attrs = append([]Attr{}, h.attrs...)
	for _, a := range attrs {
		nh.attrs = appendAttr(nh.attrs, h.prefix, a)
	}
	if h.next != nil {
		nh.next = h.next.WithAttrs(attrs)
	}
	return &nh
}

// WithGroup 推进组前缀：只作用于此后新增的字段，与 slog.TextHandler 一致。
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	nh := *h
	if h.prefix != "" {
		nh.prefix = h.prefix + "." + name
	} else {
		nh.prefix = name
	}
	if h.next != nil {
		nh.next = h.next.WithGroup(name)
	}
	return &nh
}

// appendAttr 把一个字段展平进 dst：组递归展开、键带前缀；空字段忽略。
func appendAttr(dst []Attr, prefix string, a slog.Attr) []Attr {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return dst
	}
	key := a.Key
	if prefix != "" {
		if key != "" {
			key = prefix + "." + key
		} else {
			key = prefix
		}
	}
	if a.Value.Kind() == slog.KindGroup {
		for _, ga := range a.Value.Group() {
			dst = appendAttr(dst, key, ga)
		}
		return dst
	}
	return append(dst, Attr{Key: key, Value: a.Value.String()})
}
