package core

import (
	"testing"
	"time"
)

func TestTTLMap(t *testing.T) {
	var m TTLMap[string, int]
	m.Set("live", 1, time.Hour)
	m.Set("dead", 2, -time.Second)

	if v, ok := m.Get("live"); !ok || v != 1 {
		t.Fatalf("live = %v, %v", v, ok)
	}
	// 过期即视为不存在，不必等 GC。
	if _, ok := m.Get("dead"); ok {
		t.Fatal("过期条目仍可读")
	}
	if _, ok := m.Take("dead"); ok {
		t.Fatal("过期条目仍可取出")
	}

	m.Set("dead", 2, -time.Second)
	m.GC(time.Now())
	n := 0
	m.m.Range(func(_, _ any) bool { n++; return true })
	if n != 1 {
		t.Fatalf("GC 后剩 %d 条，应只剩 live", n)
	}

	if v, ok := m.Take("live"); !ok || v != 1 {
		t.Fatalf("Take = %v, %v", v, ok)
	}
	if _, ok := m.Get("live"); ok {
		t.Fatal("Take 之后仍可读")
	}
	m.Set("x", 3, time.Hour)
	m.Delete("x")
	if _, ok := m.Get("x"); ok {
		t.Fatal("Delete 之后仍可读")
	}
}

func TestSharedExt(t *testing.T) {
	type key struct{}
	a, b := &Shared{}, &Shared{}
	pa := Ext(a, key{}, func() *int { return new(int) })
	*pa = 7
	if got := *Ext(a, key{}, func() *int { return new(int) }); got != 7 {
		t.Fatalf("同一个 Shared 取到的不是同一份：%d", got)
	}
	if got := *Ext(b, key{}, func() *int { return new(int) }); got != 0 {
		t.Fatalf("不同 Shared 之间串了状态：%d", got)
	}
}

// TestTTLMapSetNX：只在键不存在时写入；已过期视为不存在。
func TestTTLMapSetNX(t *testing.T) {
	var m TTLMap[string, int]
	if !m.SetNX("k", 1, time.Hour) {
		t.Fatal("首次 SetNX 应成功")
	}
	if m.SetNX("k", 2, time.Hour) {
		t.Fatal("键还在时 SetNX 应失败")
	}
	if v, _ := m.Get("k"); v != 1 {
		t.Fatalf("失败的 SetNX 不该改写值：%d", v)
	}
	// 过期后可以重新占用。
	m.Set("k", 9, time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if !m.SetNX("k", 3, time.Hour) {
		t.Fatal("过期后 SetNX 应成功")
	}
	if v, _ := m.Get("k"); v != 3 {
		t.Fatalf("过期后 SetNX 应写入新值：%d", v)
	}
}
