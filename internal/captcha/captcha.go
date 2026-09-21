package captcha

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"sync"
	"time"
)

// captchaTTL 是一道题的有效期。
const captchaTTL = 3 * time.Minute

// Options 是每题给出的选项数。
const Options = 4

// Challenge 是一道待答的算术题。
type Challenge struct {
	Question string
	Answer   int
	Options  []int
	expire   time.Time
}

// Store 是进程内的题库。
//
// ponytail: 只存内存。重启后未答完的题作废，用户重新点一次按钮即可 ——
// 为一道三分钟有效期的算术题落库不划算。
//
// 它的作用不是挡住人，而是挡住**脚本**：自助解除每成功一次就要跑一轮
// AI 重判，没有这道门槛，一个循环就能把判定开销刷上去。
type Store struct {
	mu sync.Mutex
	m  map[int64]Challenge
}

func NewStore() *Store {
	return &Store{m: map[int64]Challenge{}}
}

// randInt 返回 [0, n) 的随机数。
// 用 crypto/rand 而不是 math/rand：题目可预测的话，脚本可以直接算出答案，
// 这道门就等于不存在。
func randInt(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic(err) // crypto/rand 失败说明系统熵源坏了，继续运行是危险的
	}
	return int(v.Int64())
}

// Issue 为某人出一道新题，覆盖他之前未答完的那道。
func (s *Store) Issue(uid int64) Challenge {
	a, b := randInt(9)+1, randInt(9)+1
	c := Challenge{
		Question: fmt.Sprintf("%d + %d = ?", a, b),
		Answer:   a + b,
		expire:   time.Now().Add(captchaTTL),
	}

	// 干扰项与正确答案不重复，否则会出现两个按钮都对。
	used := map[int]bool{c.Answer: true}
	opts := []int{c.Answer}
	for len(opts) < Options {
		// 在正确答案附近取值：差太远的选项一眼就能排除，等于白给。
		v := c.Answer - 4 + randInt(9)
		if v <= 0 || used[v] {
			continue
		}
		used[v] = true
		opts = append(opts, v)
	}
	// 洗牌，否则正确答案永远在第一个。
	for i := len(opts) - 1; i > 0; i-- {
		j := randInt(i + 1)
		opts[i], opts[j] = opts[j], opts[i]
	}
	c.Options = opts

	s.mu.Lock()
	s.m[uid] = c
	s.mu.Unlock()
	return c
}

// Check 校验答案。无论对错都消费掉这道题 —— 留着的话，答错之后可以
// 用同一道题继续试，四个选项试一遍必中。
func (s *Store) Check(uid int64, ans int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.m[uid]
	if !ok || time.Now().After(c.expire) {
		delete(s.m, uid)
		return false
	}
	delete(s.m, uid)
	return ans == c.Answer
}

// Pending 报告此人是否有一道未过期的题。
func (s *Store) Pending(uid int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.m[uid]
	return ok && time.Now().Before(c.expire)
}

// GC 清理过期题目，防止 map 无限增长。
func (s *Store) GC() {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for uid, c := range s.m {
		if now.After(c.expire) {
			delete(s.m, uid)
		}
	}
}

func (s *Store) Size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}
