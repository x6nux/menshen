package ratelimit

import (
	"sync"
	"testing"
	"time"
)

var base = time.Unix(1_700_000_000, 0)

// TestAllowAtSlidingWindow 验证窗口是滑动的而非固定分钟桶：固定桶会在
// 桶边界放过两倍的请求量。
func TestAllowAtSlidingWindow(t *testing.T) {
	l := New()
	if !l.AllowAt("k", 2, base) {
		t.Fatal("第一次应放行")
	}
	if !l.AllowAt("k", 2, base.Add(10*time.Second)) {
		t.Fatal("第二次应放行")
	}
	if l.AllowAt("k", 2, base.Add(20*time.Second)) {
		t.Error("窗口内第三次应拒绝")
	}
	// 第一条滑出窗口后恢复放行。
	if !l.AllowAt("k", 2, base.Add(61*time.Second)) {
		t.Error("窗口滑动后应恢复放行")
	}
	// 不同 key 互不影响。
	if !l.AllowAt("other", 2, base) {
		t.Error("不同 key 应独立计数")
	}
}

// TestAllowAtLimitZeroMeansUnlimited 验证 0 表示不限且不留下窗口记录，
// 否则限流从关到开时会带着已有计数立刻触顶。
func TestAllowAtLimitZeroMeansUnlimited(t *testing.T) {
	l := New()
	for i := range 100 {
		if !l.AllowAt("k", 0, base.Add(time.Duration(i)*time.Second)) {
			t.Fatal("0 应表示不限")
		}
	}
	if n := l.Size(); n != 0 {
		t.Errorf("不限的 key 不该留下窗口记录，得到 %d", n)
	}
}

// TestGCAtReclaimsIdleBuckets 验证闲置超过 5 个窗口的 key 被回收，
// 避免不断变化的 key 留下永不释放的桶。
func TestGCAtReclaimsIdleBuckets(t *testing.T) {
	l := New()
	l.AllowAt("old", 1, base)
	l.AllowAt("new", 1, base.Add(10*time.Minute))

	l.GCAt(base.Add(10 * time.Minute))

	if n := l.Size(); n != 1 {
		t.Fatalf("闲置窗口应被回收，剩 %d", n)
	}
	// 回收之后同一个 key 仍能正常计数。
	if !l.AllowAt("old", 1, base.Add(11*time.Minute)) {
		t.Error("回收后应可重新计数")
	}
}

// TestLimiterConcurrent：Allow 与 GC 会被多个 bot 的 worker 并发调用。
func TestLimiterConcurrent(t *testing.T) {
	l := New()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := range 100 {
				l.Allow("k", 1000)
				l.Allow("k"+string(rune('0'+i)), 1000)
				if j%10 == 0 {
					l.GC()
				}
			}
		}(i)
	}
	wg.Wait()
}
