package ratelimit

import (
	"sync"
	"time"
)

const windowLen = time.Minute

// entry 记录一次事件：时间 + 计数
type entry struct {
	at time.Time
	n  int64
}

type window struct {
	items []entry
	last  time.Time
}

// trim 丢弃滑出窗口的记录，返回窗口内总量
func (w *window) trim(now time.Time) int64 {
	cut := now.Add(-windowLen)
	i := 0
	for i < len(w.items) && !w.items[i].at.After(cut) {
		i++
	}
	if i > 0 {
		w.items = append(w.items[:0], w.items[i:]...)
	}
	var sum int64
	for _, e := range w.items {
		sum += e.n
	}
	return sum
}

// add 追加一条记录。调用方须已持有 Limiter 的锁。
func (w *window) add(now time.Time, n int64) {
	w.items = append(w.items, entry{at: now, n: n})
	w.last = now
}

// Limiter 是单进程内存限流器。多实例部署时整体替换为 Redis 实现，接口不变。
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*window
}

func New() *Limiter {
	return &Limiter{buckets: map[string]*window{}}
}

func (l *Limiter) Allow(key string, limit int64) bool {
	return l.AllowAt(key, limit, time.Now())
}

// AllowAt 检查并在放行时记入一次。limit <= 0 表示不限（不是全禁），
// 并且不留下任何窗口记录——否则限流从关到开的那一刻会立即触顶。
func (l *Limiter) AllowAt(key string, limit int64, now time.Time) bool {
	if limit <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	w := l.buckets[key]
	if w == nil {
		w = &window{}
		l.buckets[key] = w
	}
	if w.trim(now) >= limit {
		return false
	}
	w.add(now, 1)
	return true
}

// GCAt 回收长期未使用的窗口。阈值取 5 个窗口长度，远大于窗口本身，
// 避免把一个稀疏使用的 key 反复创建与删除。
func (l *Limiter) GCAt(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := now.Add(-5 * windowLen)
	for k, w := range l.buckets {
		if w.last.Before(cut) {
			delete(l.buckets, k)
		}
	}
}

func (l *Limiter) GC() { l.GCAt(time.Now()) }

func (l *Limiter) Size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
