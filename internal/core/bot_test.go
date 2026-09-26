package core

import (
	"sync/atomic"
	"testing"
	"time"
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
// 单次判定最坏要几十秒，几条慢请求占满通道后「满了就放行」等于后面的
// 消息全部漏判。
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
