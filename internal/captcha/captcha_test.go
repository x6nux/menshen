package captcha

import (
	"testing"
	"time"
)

// TestCaptchaSingleUse 确认一道题只能用一次。
// 留着的话答错之后可以拿同一道题继续试，四个选项试一遍必中，
// 这道门就等于不存在。
func TestCaptchaSingleUse(t *testing.T) {
	s := NewStore()
	c := s.Issue(7)

	if s.Check(7, c.Answer+1) {
		t.Fatal("错误答案不该通过")
	}
	if s.Check(7, c.Answer) {
		t.Error("答错之后同一道题还能继续试 —— 四个选项试一遍必中")
	}
	if s.Pending(7) {
		t.Error("题目应当已被消费")
	}
}

// TestCaptchaOptions 确认选项里恰好有一个正确答案。
// 两个按钮都对，脚本随便点一个就过；一个都不对，正常人永远解除不了。
func TestCaptchaOptions(t *testing.T) {
	s := NewStore()
	for range 50 {
		c := s.Issue(1)
		if len(c.Options) != Options {
			t.Fatalf("选项数 = %d, 期望 %d", len(c.Options), Options)
		}
		hits, seen := 0, map[int]bool{}
		for _, o := range c.Options {
			if seen[o] {
				t.Fatalf("选项重复: %v", c.Options)
			}
			seen[o] = true
			if o == c.Answer {
				hits++
			}
		}
		if hits != 1 {
			t.Fatalf("正确答案在选项里出现 %d 次: %v (答案 %d)",
				hits, c.Options, c.Answer)
		}
	}
}

// TestCaptchaExpires 确认过期的题不认。
func TestCaptchaExpires(t *testing.T) {
	s := NewStore()
	c := s.Issue(9)

	s.mu.Lock()
	e := s.m[9]
	e.expire = time.Now().Add(-time.Second)
	s.m[9] = e
	s.mu.Unlock()

	if s.Check(9, c.Answer) {
		t.Error("过期的题不该通过")
	}
	s.Issue(9)
	s.mu.Lock()
	e = s.m[9]
	e.expire = time.Now().Add(-time.Second)
	s.m[9] = e
	s.mu.Unlock()
	s.GC()
	if s.Size() != 0 {
		t.Errorf("gc 后仍剩 %d 道过期的题", s.Size())
	}
}
