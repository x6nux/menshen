package core

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"menshen/internal/config"
	"menshen/internal/store"
)

// waitAdIdle 等判定队列排空。
func waitAdIdle(t *testing.T, b *Bot) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for b.AdBusy() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("判定队列 5 秒内没排空，还剩 %d 个", b.AdBusy())
		}
		time.Sleep(time.Millisecond)
	}
}

// TestAdSubmitQueuesWhenWorkersBusy：worker 全忙时新任务排队而不是丢弃。
// 单次判定最坏要几十秒，几条慢请求占满通道后满了就放行等于后面的消息
// 全部漏判。
func TestAdSubmitQueuesWhenWorkersBusy(t *testing.T) {
	b := NewBot(nil, nil, "", nil)
	defer b.Shutdown()

	release := make(chan struct{})
	var ran atomic.Int64
	for i := range adWorkers + 10 {
		if !b.AdSubmit(func() { <-release; ran.Add(1) }) {
			t.Fatalf("第 %d 个任务被丢弃，worker 全忙时应当排队", i+1)
		}
	}
	close(release)
	waitAdIdle(t, b)
	if n := ran.Load(); n != adWorkers+10 {
		t.Errorf("执行了 %d 个任务，期望 %d 个", n, adWorkers+10)
	}
}

// TestAdSubmitRejectsWhenQueueFull：队列也满说明上游整体慢了，此时放行
// 而不是阻塞——阻塞的是更新处理的同步段，整个 bot 会跟着停摆。
func TestAdSubmitRejectsWhenQueueFull(t *testing.T) {
	b := NewBot(nil, nil, "", nil)
	release := make(chan struct{})
	defer b.Shutdown()
	defer close(release)

	started := make(chan struct{}, adWorkers)
	for range adWorkers {
		b.AdSubmit(func() { started <- struct{}{}; <-release })
	}
	for range adWorkers {
		<-started // 全部 worker 都已占住
	}
	for i := range adQueueCap {
		if !b.AdSubmit(func() {}) {
			t.Fatalf("队列未满时第 %d 个任务被拒", i+1)
		}
	}
	if b.AdSubmit(func() {}) {
		t.Error("队列已满时应返回 false")
	}
}

// TestAdReviewConcurrencyCappedGlobally：复判执行前要过 Shared 级的全局闸。
// 每个 bot 各有一池 32 路复判 worker，多 bot 叠加会远超上游配额（复判是
// 最慢、最贵、并发放大最猛的一路），所以同时在跑的复判被压在
// adReviewConcurrency。
func TestAdReviewConcurrencyCappedGlobally(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cache, err := store.NewCache(db)
	if err != nil {
		t.Fatalf("store.NewCache: %v", err)
	}
	sh := NewShared(&config.Config{}, db, cache)

	// 两个 bot 共用一份 Shared：闸在 Shared 级，跨 bot 生效。
	b1 := NewBot(nil, sh, "token1", nil)
	b2 := NewBot(nil, sh, "token2", nil)
	defer b1.Shutdown()
	defer b2.Shutdown()

	var running, peak atomic.Int64
	release := make(chan struct{})
	job := func() {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		running.Add(-1)
	}
	// 两个 bot 各占满 32 路复判 worker：64 个任务同时要跑，闸门只放 50。
	for range adWorkers {
		if !b1.AdReview(job) {
			t.Fatal("入队失败")
		}
		if !b2.AdReview(job) {
			t.Fatal("入队失败")
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && running.Load() < int64(adReviewConcurrency) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := peak.Load(); got != int64(adReviewConcurrency) {
		t.Errorf("复判并发峰值应为 %d，得到 %d", adReviewConcurrency, got)
	}
	close(release)
}
