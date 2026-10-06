package core

import (
	"sync"
	"time"
)

// TTLMap 是带过期时间的并发 map，进程内的各类缓存与节流记录共用。
//
// 过期即视为不存在（Get / Take 自己判断，不必等 GC）；GC 只负责回收内存，
// 由分钟任务统一触发。零值可用。
type TTLMap[K comparable, V any] struct{ m sync.Map }

type ttlItem[V any] struct {
	v   V
	exp time.Time
}

func (t *TTLMap[K, V]) Get(k K) (V, bool) {
	if raw, ok := t.m.Load(k); ok {
		if it := raw.(ttlItem[V]); time.Now().Before(it.exp) {
			return it.v, true
		}
	}
	var zero V
	return zero, false
}

func (t *TTLMap[K, V]) Set(k K, v V, ttl time.Duration) {
	t.m.Store(k, ttlItem[V]{v: v, exp: time.Now().Add(ttl)})
}

// SetNX 只在键不存在（或已过期）时写入，返回是否写成功。
//
// 用于「用掉一次」这类一次性凭据（Cap 的验证令牌防重放）：Get 再 Set
// 不是原子的，两个并发请求会双双通过。过期条目按不存在处理并就地替换。
func (t *TTLMap[K, V]) SetNX(k K, v V, ttl time.Duration) bool {
	item := ttlItem[V]{v: v, exp: time.Now().Add(ttl)}
	actual, loaded := t.m.LoadOrStore(k, item)
	if !loaded {
		return true
	}
	if it, ok := actual.(ttlItem[V]); ok && !time.Now().Before(it.exp) {
		t.m.Store(k, item)
		return true
	}
	return false
}

// Take 取出并删除；过期的条目同样删掉，但报告不存在。
func (t *TTLMap[K, V]) Take(k K) (V, bool) {
	if raw, ok := t.m.LoadAndDelete(k); ok {
		if it := raw.(ttlItem[V]); time.Now().Before(it.exp) {
			return it.v, true
		}
	}
	var zero V
	return zero, false
}

func (t *TTLMap[K, V]) Delete(k K) { t.m.Delete(k) }

// GC 删掉 now 时已过期的条目，防止 map 无限增长。
func (t *TTLMap[K, V]) GC(now time.Time) {
	t.m.Range(func(k, raw any) bool {
		if !now.Before(raw.(ttlItem[V]).exp) {
			t.m.Delete(k)
		}
		return true
	})
}

// Ext 取上层包挂在 Shared 上的进程级状态，第一次取时用 init 建。
//
// core 被 antiad 与 panel 依赖，不认识它们的类型；各包用自己的未导出
// key 类型挂一份带类型的状态，彼此隔离，也不必在 Shared 上逐个加字段。
func Ext[T any](sh *Shared, key any, init func() *T) *T {
	if v, ok := sh.ext.Load(key); ok {
		return v.(*T)
	}
	v, _ := sh.ext.LoadOrStore(key, init())
	return v.(*T)
}
