package antiad

import (
	"time"

	"menshen/internal/core"
)

// caches 是反广告的进程级内存状态，挂在 Shared 上（见 core.Ext），
// 由分钟任务的 GCCaches 统一回收。全部只存内存：重启后丢了无非多查一次。
type caches struct {
	// chatAdmin 缓存「某人是否为某群管理员」，键 "chatID:uid"。
	// 全量送检时不缓存等于每条群消息一次 getChatMember，
	// 必然撞上 Telegram 的全局速率限制。
	chatAdmin core.TTLMap[string, bool]
	// bio 缓存 TG 个人资料（getChat）。广告号的强特征常写在简介里，
	// 但全量送检下不缓存就是每条群消息多一次 TG 往返。
	bio core.TTLMap[int64, bioEntry]
	// photo 缓存 getUserProfilePhotos 的结果（头像张数），键 uid。
	// 前置号/申诉路径才查；头像数很少变，查一次管一天。
	photo core.TTLMap[int64, photoEntry]
	// link 缓存简介/昵称里挂的频道、群组、bot 查出来的样子，键为小写用户名。
	// 按用户名共享：群里十个人挂同一个频道也只查一次。
	link core.TTLMap[string, linkInfo]
	// vision 是识图结果，键为 file_unique_id：刷屏号反复发同一张图、
	// 群友反复用同一个贴纸，不必每次都花钱。
	vision core.TTLMap[string, string]
	// doomedAlbums 记下已判成广告的相册，判定之后才到的那几张照删。
	doomedAlbums core.TTLMap[string, struct{}]
	// unbanGate 是申诉的重试闸，键为 uid。
	// 不限次数但间隔递增，见 nextUnbanDelay。
	unbanGate core.TTLMap[int64, unbanAttempt]
	// joinNotice 把「XXX 已加入群组」服务消息与冷判定结果配对
	// （见 noteJoinNotice）：命中禁言时那条服务消息也要删掉，
	// 而它与判定结果谁先到都有可能。键 "chatID:uid"。
	joinNotice core.TTLMap[string, joinNoticeEntry]
	// tempMutes 记下我们刚上的临时禁言（见 NoteTempMute），键 chat:uid。
	tempMutes core.TTLMap[string, struct{}]
	// justLifted 记下「这是我们主动解除的」（见 NoteLifted），键 chat:uid。
	justLifted core.TTLMap[string, struct{}]
	// reasserts 记重新施加的时间点（限流用），键 chat:uid。
	reasserts core.TTLMap[string, []time.Time]
	// joinBackfillDone 记录 (bot, chat) 上次补全入群时间，键 "bot:chat"。
	joinBackfillDone core.TTLMap[string, struct{}]
	// joinLookupMiss 是单人入群时间查不到的负缓存，避免反复起脚本。
	joinLookupMiss core.TTLMap[string, struct{}]
	// residualSwept 记录每个人上次残留限制复查的开始，键 uid。
	residualSwept core.TTLMap[int64, struct{}]
}

type cachesKey struct{}

func cachesOf(sh *core.Shared) *caches {
	return core.Ext(sh, cachesKey{}, func() *caches { return &caches{} })
}

// GCCaches 回收全部过期条目，防止 map 无限增长。
func GCCaches(sh *core.Shared) {
	now := time.Now()
	c := cachesOf(sh)
	c.chatAdmin.GC(now)
	c.bio.GC(now)
	c.photo.GC(now)
	c.link.GC(now)
	c.vision.GC(now)
	c.doomedAlbums.GC(now)
	c.unbanGate.GC(now)
	c.joinNotice.GC(now)
	c.tempMutes.GC(now)
	c.justLifted.GC(now)
	c.reasserts.GC(now)
	c.joinBackfillDone.GC(now)
	c.joinLookupMiss.GC(now)
	c.residualSwept.GC(now)
}
