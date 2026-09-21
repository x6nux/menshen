package core

import "sync"

// ---- 上下文环 + 画像组装（送检 state）----

const (
	// ctxRingCap 是每群保留的上下文条数上限。取 20 而非
	// antiad_ctx_msgs 本身：后者可被管理员调大，缓冲区要留余量。
	ctxRingCap = 20
	// ctxTextLimit 是单条上下文的字符上限。上下文用来让模型理解
	// 群里在聊什么，不需要全文；不截断的话一条长消息就能把每次
	// 判定的 input token 撑爆。
	ctxTextLimit = 200
)

type CtxMsg struct {
	Name string `json:"user"`
	Text string `json:"text"`
	At   int64  `json:"-"`
}

// CtxRing 是每群一份的定长上下文缓冲。
// ponytail: 只存内存。重启后前几条判定少几条上下文，不值得落库。
type CtxRing struct {
	mu  sync.Mutex
	buf map[int64][]CtxMsg
}

func (r *CtxRing) Push(chatID int64, name, text string, at int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.buf == nil {
		r.buf = map[int64][]CtxMsg{}
	}
	list := append(r.buf[chatID], CtxMsg{
		Name: name, Text: TruncateRunes(text, ctxTextLimit), At: at})
	if len(list) > ctxRingCap {
		list = list[len(list)-ctxRingCap:]
	}
	r.buf[chatID] = list
}

// recent 返回最近 n 条，由旧到新。
func (r *CtxRing) Recent(chatID int64, n int) []CtxMsg {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.buf[chatID]
	if n <= 0 || len(list) == 0 {
		return nil
	}
	if len(list) > n {
		list = list[len(list)-n:]
	}
	return append([]CtxMsg(nil), list...)
}

// TruncateRunes 按 rune 截断，不切碎 UTF-8。
func TruncateRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}
