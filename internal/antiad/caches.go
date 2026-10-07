package antiad

import (
	"sync"
	"time"

	"menshen/internal/core"
)

// caches 是反广告的进程级内存状态，挂在 Shared 上（见 core.Ext），
// 由分钟任务的 GCCaches 统一回收。全部只存内存：重启后丢了无非多查一次。
type caches struct {
	// chatAdmin 缓存某人是否为某群管理员，键 "chatID:uid"。
	// 全量送检时不缓存等于每条群消息一次 getChatMember，
	// 必然撞上 Telegram 的全局速率限制。
	chatAdmin core.TTLMap[string, bool]
	// bio 缓存 TG 个人资料（getChat），键 "botID:uid"。广告号的强特征常写在
	// 简介里，但全量送检下不缓存就是每条群消息多一次 TG 往返。键里带 bot：
	// 查不查得到取决于该 bot 与用户有没有共同会话，共用一个键会让查不到的
	// 空结果盖掉另一个 bot 查得到的结果（见 bioCacheKey）。
	bio core.TTLMap[string, bioEntry]
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
	// joinNotice 把入群服务消息与冷判定结果配对
	// （见 noteJoinNotice）：命中禁言时那条服务消息也要删掉，
	// 而它与判定结果谁先到都有可能。键 "chatID:uid"。
	joinNotice core.TTLMap[string, joinNoticeEntry]
	// tempMutes 记下我们刚上的临时禁言（见 NoteTempMute），键 chat:uid。
	tempMutes core.TTLMap[string, struct{}]
	// justLifted 记下这是我们主动解除的标记（见 NoteLifted），键 chat:uid。
	justLifted core.TTLMap[string, struct{}]
	// reasserts 记重新施加的时间点（限流用），键 chat:uid。
	reasserts core.TTLMap[string, []time.Time]
	// reassertCursor 是周期复查的分页游标（最后核对过的 "chatID:uid"）。
	// join_mutes 行数可能上千，每轮只核对一页，下轮从这里往后接。只被
	// 小时任务那个不重入的 goroutine 读写，无需加锁。
	reassertCursor string
	// joinBackfillDone 记录 (bot, chat) 上次补全入群时间，键 "bot:chat"。
	joinBackfillDone core.TTLMap[string, struct{}]
	// joinLookupMiss 是单人入群时间查不到的负缓存，避免反复起脚本。
	joinLookupMiss core.TTLMap[string, struct{}]
	// residualSwept 记录每个人上次残留限制复查的开始，键 uid。
	residualSwept core.TTLMap[int64, struct{}]
	// prewarmAI 记录前置号复查上次跑账号 AI 的时间，键 "chatID:uid"。
	// 同一人 10 分钟内最多判一次：资料反复改名时冷却期内只推后 next_at，
	// 不重复烧 AI；首次见到该成员不受它限制。
	prewarmAI core.TTLMap[string, time.Time]
	// prewarmInflight 标记某个 (群, 人) 正在被一个处理者占用，键
	// "chatID:uid"：选人抢占的 5 分钟 hold 会在探测/判定排队时过期，
	// 这里兜底挡住重复投递。正常出口都会 defer 释放，TTL 只在进程
	// 崩溃/卡死时兜底。
	prewarmInflight core.TTLMap[string, struct{}]
	// prewarmProbeOnce 保证低优先级探测协程进程内只起一份。
	prewarmProbeOnce sync.Once
	// prewarmProbeStop 让测试能停掉探测协程（生产一直跑到进程退出）。
	prewarmProbeStop chan struct{}
	// prewarmProbeCursor 是轮转游标（最后处理过的 "botID:chatID"）：
	// 每次探测从它的下一个群开始，避免总盯着一两个群。
	prewarmProbeCursor string
	// prewarmNoticeAt 做群内通知的按群限速，键 chatID，值上次通知时刻。
	// 扫描高峰的 429 主要来自成批 sendMessage。
	prewarmNoticeAt core.TTLMap[int64, time.Time]
	// capNonces 记录内置 Cap 已用过的挑战 / 兑换令牌签名，防重放，键为签名十六进制。
	capNonces core.TTLMap[string, struct{}]
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
	c.prewarmAI.GC(now)
	c.prewarmInflight.GC(now)
	c.prewarmNoticeAt.GC(now)
	c.capNonces.GC(now)
}
