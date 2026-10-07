package core_test

import (
	"testing"
	"time"

	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/tg"
)

const shardChat int64 = -100

// twoBots 建同一 owner 名下、同在 shardChat 的两个 bot，并让两者都收到过
// 这个群的更新——没收到过的 bot 不参与分担（见 TestClaimSenderSkipsUnseenBot）。
func twoBots(t *testing.T, now time.Time) (a, b *core.Bot, reg *core.Registry) {
	t.Helper()
	reg, a = testutil.NewTestRegistry(t, func(*core.Bot, *tg.Update) {})
	b, _ = testutil.AddRegistryBot(t, reg, a.Shared, 4343, a.Owner())
	testutil.EnableAntiad(t, a, shardChat)
	testutil.EnableAntiad(t, b, shardChat)
	a.ClaimSender(shardChat, 1, now)
	b.ClaimSender(shardChat, 1, now)
	return a, b, reg
}

// TestClaimSenderSingleBot：没有 registry（长轮询单 bot）时一律归自己。
func TestClaimSenderSingleBot(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 777)
	if !b.ClaimSender(shardChat, 5, time.Now()) {
		t.Error("只有一个 bot 时发言人必须归它自己")
	}
}

// TestClaimSenderOneOwnerPerUser 是分担的核心断言：同一个人只归一个 bot，
// 两边问到的结论一致且稳定；不同的人摊到两个 bot 上。
func TestClaimSenderOneOwnerPerUser(t *testing.T) {
	now := time.Now()
	a, b, _ := twoBots(t, now)

	perBot := map[bool]int{}
	for uid := int64(100); uid < 160; uid++ {
		first := a.ClaimSender(shardChat, uid, now)
		for range 3 {
			if a.ClaimSender(shardChat, uid, now) != first {
				t.Fatalf("uid %d 的归属在 bot A 上不稳定", uid)
			}
			if b.ClaimSender(shardChat, uid, now) == first {
				t.Fatalf("uid %d 同时归了两个 bot（或都不归）", uid)
			}
		}
		perBot[first]++
	}
	if perBot[true] == 0 || perBot[false] == 0 {
		t.Errorf("60 个人全落在一个 bot 上（A=%d B=%d），没有分担", perBot[true], perBot[false])
	}
}

// TestClaimSenderPrefersIdle：新出现的人分给判定队列更空的那个 bot。
func TestClaimSenderPrefersIdle(t *testing.T) {
	now := time.Now()
	a, b, _ := twoBots(t, now)

	release := make(chan struct{})
	defer close(release)
	for range 5 {
		a.AdSubmit(func() { <-release })
	}
	if a.ClaimSender(shardChat, 500, now) {
		t.Error("A 正忙，新发言人不该分给它")
	}
	if !b.ClaimSender(shardChat, 500, now) {
		t.Error("B 空闲，新发言人应当归它")
	}
}

// TestClaimSenderSkipsUnseenBot：从没收到过该群更新的 bot 不参与分担。
// 它多半不是管理员（隐私模式下收不到普通消息），把人分给它等于静默放过。
func TestClaimSenderSkipsUnseenBot(t *testing.T) {
	reg, a := testutil.NewTestRegistry(t, func(*core.Bot, *tg.Update) {})
	b, _ := testutil.AddRegistryBot(t, reg, a.Shared, 4343, a.Owner())
	testutil.EnableAntiad(t, a, shardChat)
	testutil.EnableAntiad(t, b, shardChat)

	now := time.Now()
	for uid := int64(100); uid < 120; uid++ {
		if !a.ClaimSender(shardChat, uid, now) {
			t.Fatalf("B 从没收到过本群更新，uid %d 却没归 A", uid)
		}
	}
}

// TestClaimSenderReassigns：认领者停用、或太久没收到本群更新时改派，
// 不能让这个人一直挂在一个收不到消息的 bot 名下。
func TestClaimSenderReassigns(t *testing.T) {
	now := time.Now()
	a, b, reg := twoBots(t, now)

	// 先找一个归 B 的人
	var uid int64
	for uid = 100; a.ClaimSender(shardChat, uid, now); uid++ {
	}

	// B 太久没收到本群更新：A 收到此人的新消息时接手
	later := now.Add(10 * time.Minute)
	if !a.ClaimSender(shardChat, uid, later) {
		t.Error("认领者已经很久收不到本群更新，应当改派给正在收的 bot")
	}

	// B 停用
	var uid2 int64
	for uid2 = 1000; !b.ClaimSender(shardChat, uid2, later); uid2++ {
	}
	if err := reg.SetBotEnabled(b.BotID(), false); err != nil {
		t.Fatal(err)
	}
	if !a.ClaimSender(shardChat, uid2, later) {
		t.Error("认领者已停用，应当改派")
	}
}

// TestClaimSenderSeparateOwners：不同 owner 的 bot 不互相分担——各自的
// 演练状态、阈值、处罚方式不同，混用会让 A 的用户按 B 的配置处罚。
func TestClaimSenderSeparateOwners(t *testing.T) {
	reg, a := testutil.NewTestRegistry(t, func(*core.Bot, *tg.Update) {})
	b, _ := testutil.AddRegistryBot(t, reg, a.Shared, 4343, 200)
	testutil.EnableAntiad(t, a, shardChat)
	testutil.EnableAntiad(t, b, shardChat)

	now := time.Now()
	for uid := int64(100); uid < 120; uid++ {
		if !a.ClaimSender(shardChat, uid, now) || !b.ClaimSender(shardChat, uid, now) {
			t.Fatalf("uid %d：不同 owner 的 bot 各管各的，都应当归自己", uid)
		}
	}
}

// TestGCShardDropsIdle：闲置的认领被回收，回收后照常重新分配。
func TestGCShardDropsIdle(t *testing.T) {
	now := time.Now()
	a, _, _ := twoBots(t, now)
	a.ClaimSender(shardChat, 100, now)
	if n := a.GCShard(now.Add(time.Hour)); n == 0 {
		t.Error("闲置一小时的认领应当被回收")
	}
	if n := a.GCShard(now.Add(time.Hour)); n != 0 {
		t.Errorf("已回收干净，第二轮不该再回收（%d）", n)
	}
}
