package core

import (
	"math/rand/v2"
	"sync"
	"time"
)

// 同群多 bot 的分担。
//
// 同一个群里挂着几个 bot 时，每条更新每个 bot 都会收到一份。广告号成批涌进来
// 的时候，单个 bot 的 TG 限速与判定池都会被打满，所以按**发言人**把活摊开：
// 一个人只归一个 bot，删消息、禁言、告警、画像计数都只由它做。
//
// 按人而不是按消息分：同一个人的几条消息落到不同 bot 上，就会有两个 bot
// 同时对他删、禁言、发告警；而且 recent_context 取的是「这条之前」的留底，
// 只有同一条串行队列才保证得了这个顺序。
//
// 只在同一 owner 名下的 bot 之间分担：不同租户的演练状态、阈值、处罚方式
// 各不相同，混着用会让 A 的用户按 B 的配置处罚。
//
// ponytail: 认领表在内存里。子 bot 只能走 webhook，全都在同一个进程；
// 真要多进程部署时得挪进库里。

const (
	// shardSeenTTL 内收到过本群更新的 bot 才参与分担。没收到过的多半不是
	// 管理员（隐私模式下收不到普通消息），把人分给它等于静默放过。
	shardSeenTTL = 5 * time.Minute
	// shardIdleTTL 是认领的闲置回收时限。
	shardIdleTTL = 30 * time.Minute
)

type shardClaim struct {
	botID int64
	at    time.Time
}

type shardTable struct {
	mu     sync.Mutex
	seen   map[[2]int64]time.Time  // {chatID, botID} -> 最近一次收到该群更新
	claims map[[3]int64]shardClaim // {owner, chatID, uid}
}

// ClaimSender 报告本 bot 是否负责处理这个群里的这个人，调用方收到 false
// 就整条跳过——归属的那个 bot 自己也会收到这条更新。
//
// 已有认领且认领者仍在正常收本群更新时沿用它；否则在候选里挑判定队列
// 最空的，并列随机。改派只转移归属，从不让两个 bot 同时认为自己负责。
func (b *Bot) ClaimSender(chatID, uid int64, now time.Time) bool {
	if b.Reg == nil {
		return true // 长轮询只有一个 bot
	}
	t := &b.shard
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.claims == nil {
		t.seen = map[[2]int64]time.Time{}
		t.claims = map[[3]int64]shardClaim{}
	}
	self := b.BotID()
	// 此刻正在处理本群的更新，就是它收得到的证据。
	t.seen[[2]int64{chatID, self}] = now

	owner := b.Owner()
	key := [3]int64{owner, chatID, uid}
	if c, ok := t.claims[key]; ok && (c.botID == self || b.shardLive(c.botID, chatID, owner, now)) {
		t.claims[key] = shardClaim{c.botID, now}
		return c.botID == self
	}

	best := []*Bot{b}
	b.Reg.Each(func(o *Bot) {
		if o.BotID() == self || !b.shardLive(o.BotID(), chatID, owner, now) {
			return
		}
		switch n, m := o.AdBusy(), best[0].AdBusy(); {
		case n < m:
			best = []*Bot{o}
		case n == m:
			best = append(best, o)
		}
	})
	pick := best[rand.IntN(len(best))].BotID()
	t.claims[key] = shardClaim{pick, now}
	return pick == self
}

// shardLive 报告 botID 此刻能否分担 chatID 的活。调用方须持有 shard.mu。
func (b *Bot) shardLive(botID, chatID, owner int64, now time.Time) bool {
	o, ok := b.Reg.LookupID(botID)
	if !ok || o.Owner() != owner {
		return false
	}
	if c, ok := b.Cache.Snap().ChatConf(botID, chatID); !ok || !c.Enabled {
		return false
	}
	return now.Sub(b.shard.seen[[2]int64{chatID, botID}]) < shardSeenTTL
}

// GCShard 回收闲置的认领与过期的「收到过」记录，返回回收的认领数。
func (sh *Shared) GCShard(now time.Time) int {
	t := &sh.shard
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for k, c := range t.claims {
		if now.Sub(c.at) > shardIdleTTL {
			delete(t.claims, k)
			n++
		}
	}
	for k, at := range t.seen {
		if now.Sub(at) > shardSeenTTL {
			delete(t.seen, k)
		}
	}
	return n
}
