package core

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"menshen/internal/config"
	"menshen/internal/ratelimit"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// ---- 待输入会话 ----

// ponytail: 待输入会话仅存内存，进程重启后丢失。重新点一次菜单即可。
type PendingInput struct {
	Op     string // "up_new_url" / "md_new_pp" / "ad_m_so" / ...
	Target string // 上游 id / 模型名 / 设置键
	expire time.Time
}

const pendingTTL = 5 * time.Minute

// ---- 进程级共享资源 ----

// Shared 是所有 bot 实例共用的东西。
//
// 拆出它是因为 webhook 模式下一个进程可以同时服务任意多个 bot（见
// webhook.go）：库、配置快照与几类缓存都是进程级事实，按 bot 复制一份
// 只会换来 N 倍的 TG 往返和 N 份互相不一致的配置。
//
// Bot 以匿名字段嵌入它，因此 b.Store / b.Cache / b.Cfg 的写法与
// 单 bot 时代完全一致。
type Shared struct {
	Cfg   *config.Config
	Store *store.Store
	Cache *store.Cache

	// adLimits 是送检前的滥用护栏（重复文本去重、每群送检上限、
	// 告警节流），窗口长度 1 分钟。
	//
	// 跨 bot 共享是有意的：两个 bot 同在一个群时，护栏该拦的是
	// 「这个群每分钟送检多少条」，各算各的等于把成本翻倍。
	AdLimits *ratelimit.Limiter

	// chatAdminCache 缓存「某人是否为某群管理员」，键 "chatID:uid"。
	// 全量送检时不缓存等于每条群消息一次 getChatMember，
	// 必然撞上 Telegram 的全局速率限制。
	ChatAdminCache sync.Map // "chatID:uid" -> chatAdminEntry
	// bioCache 缓存 TG 个人简介（getChat）。广告号的强特征常写在简介里，
	// 但全量送检下不缓存就是每条群消息多一次 TG 往返。
	BioCache sync.Map // int64(uid) -> bioEntry
	// LinkCache 缓存简介/昵称里挂的频道、群组、bot 查出来的样子，键为小写用户名。
	// 按用户名共享：群里十个人挂同一个频道也只查一次。
	LinkCache sync.Map // string -> linkEntry
	// VisionCache 是识图结果，键为 file_unique_id：刷屏号反复发同一张图、
	// 群友反复用同一个贴纸，不必每次都花钱。
	VisionCache sync.Map // string -> visionEntry
	// DoomedAlbums 记下已判成广告的相册，判定之后才到的那几张照删。
	DoomedAlbums sync.Map // "chatID:albumID" -> time.Time
	// ChatHealthCache 缓存面板的群权限自检结果，键 "botID:chatID"，
	// 值为 panel 包的 chatHealthEntry。面板每次渲染群详情都查一次
	// getChatMember，而它跑在 bot 的串行更新路径上（TG 慢时最坏 40 秒）。
	ChatHealthCache sync.Map

	// aiClient 专供反广告判定调用上游。http.Client 并发安全，
	// 共享还能复用连接池。
	AIClient *http.Client
	// AIHedge 记录各「端点:模型列表」并发模式的截止时刻（见 antiad 的 aiCall）。
	// 进程级而非 bot 级：不稳定的是上游模型本身，与哪个 bot 发起的请求无关。
	AIHedge sync.Map // string -> time.Time

	// AIFailStreak 是连续失败计数，成功一次清零；AIAlertAt 是上一次
	// 「上游可能有问题」告警的时刻（unix 秒）。两者都是进程级：坏上游是
	// 全局资源，同一个上游出问题时不该每个群各告一次。
	AIFailStreak atomic.Int64
	AIAlertAt    atomic.Int64

	// reviewSem 是复判（大模型）的全局并发闸。复判跑在每 bot 的 worker
	// 池上，多 bot 时并发按 bot 数翻倍，而上游看到的只有一份配额 ——
	// 复判又是最慢、最贵、并发放大最猛的一路，所以闸放在 Shared 级。
	// 取不到槽位的 worker 在这里等，初判队列有自己的 worker，不会被拖住。
	reviewSem chan struct{}

	// tgRoundTripper 是 TG 侧的代理 RoundTripper，所有 bot 实例共用一份。
	// nil 表示没配 tg_proxy，此时各 client 的 Transport 保持 nil，
	// 走 DefaultTransport（即读 HTTP_PROXY / HTTPS_PROXY 环境变量）。
	//
	// 共享而不是每个 bot 各建一个：Transport 自带连接池，按 bot 切开会让
	// 空闲连接数乘以 bot 数量，而它们连的是同一个上游。
	tgRoundTripper http.RoundTripper

	// unbanGate 是申诉的重试闸，键为 uid。
	// 不限次数但间隔递增，见 nextUnbanDelay。
	UnbanGate sync.Map // int64(uid) -> unbanAttempt

	// JoinNotice 把「XXX 已加入群组」服务消息与冷判定结果配对
	// （见 antiad 的 noteJoinNotice）：命中禁言时那条服务消息也要删掉，
	// 而它与判定结果谁先到都有可能。键 "chatID:uid"。
	JoinNotice sync.Map

	// transportFor 非空时用它建传输层，供测试整体替换掉真实 HTTP。
	TransportFor func(token string) tg.Transport

	// reg 是 bot 实例总表。联合封禁要遍历「所有 bot 的所有群」，
	// 而那件事不属于任何一个 bot。由 newBotRegistry 回填。
	Reg *Registry

	// shard 是同群多 bot 的发言人认领表（见 shard.go）。
	shard shardTable
}

func NewShared(cfg *config.Config, s *store.Store, c *store.Cache) *Shared {
	return &Shared{Cfg: cfg, Store: s, Cache: c,
		AdLimits:  ratelimit.New(),
		reviewSem: make(chan struct{}, adReviewConcurrency),
		// 两类出网请求各用各的代理：TG 常被墙，而 AI 上游往往是国内
		// 可达的中转，把它也绕一圈只是白多一跳。两者都可留空。
		AIClient: &http.Client{Timeout: aiClientTimeout,
			Transport: config.ProxyTransport(cfg.AIProxy)},
		tgRoundTripper: config.ProxyTransport(cfg.TGProxy)}
}

// aiClientTimeout 是判定调用 AI 上游的整体超时。
//
// 必须大于 antiad 的单次尝试上限（aiAttemptCap，45 秒）：复判是流式，出了
// 首字之后还要把话说完，客户端超时先到的话，一个还在预算内的正常复判会被
// 当成失败重试 —— 那时日志里只剩一句 context deadline exceeded，看不出是
// 哪一段出的问题（每次尝试自己带的 context 会先切）。systemone 不受影响 ——
// 它有自己的看门狗（2 秒）先掐。
const aiClientTimeout = 100 * time.Second

// ---- Bot ----

type Bot struct {
	*Shared

	TG tg.Transport
	// token 只用于 setWebhook 与日志脱敏，不参与任何业务判断。
	Token string
	// username 是 bot 自己的 @名字，拼自助解除的 deep link 要用它。
	Username string

	// ownerID 是这个 bot 的归属人，告警投递与权限判断的依据。
	// 存成原子量是因为主管理员可以在面板上改派归属，而实例是长活的。
	OwnerID atomic.Int64

	// done 让单个 bot 能被独立停掉（面板上的停用/删除），
	// 不必牵动整个进程的 stop。
	done     chan struct{}
	doneOnce sync.Once

	pending sync.Map // int64 -> PendingInput
	// upstreamNewDraft 暂存新增上游的 name\x00url\x00key，直到类型选择完毕。
	UpstreamNewDraft sync.Map // int64(uid) -> string
	// adminCmdsDone 记录哪些管理员的 chat scope 命令菜单已注册成功。
	// 启动时若管理员尚未与 bot 建立会话，setMyCommands 会失败，
	// 需要在其首次交互时补注册。
	adminCmdsDone sync.Map // int64 -> bool
	offset        int64

	// selfID 缓存 bot 自身的 user_id（getMe 结果），供权限自检使用。
	// 进程生命周期内不会变，查一次就够。
	SelfID atomic.Int64

	// adJobs 是反广告判定的任务队列，由 adWorkers 个常驻 worker 消费。
	// 判定已经挪出更新处理的同步段，但不设上限的话 goroutine 数就等于
	// 群消息速率，上游一慢就会堆成几千个在飞的请求——把「卡住更新处理」
	// 换成「打爆内存和上游」而已。
	adJobs chan func()
	// adReviews 是大模型复判的队列（先删后判的后半段），与 adJobs 分开：
	// 大模型慢，排在一起的话初判会堵在它后面。
	adReviews chan func()
	// adBusy 是排队中与执行中的任务总数，测试靠它等判定跑完。
	adBusy atomic.Int64

	// updates 是 webhook 模式下的串行投递队列。
	//
	// 必须串行：同一个人的更新有先后依赖——资历累计、留底，以及
	// recent_context 取的是「这条之前」的留底。每个 HTTP 请求各起一个
	// goroutine 会让它们随调度乱序，模型就可能把此人之后说的话当成上下文。
	// 队列把轮询的「单 goroutine 依次处理」语义原样搬了过来。
	updates chan *tg.Update

	// dispatch 是 Update 的业务分发入口，由 main 在构造时注入。
	//
	// core 不能 import antiad/panel（它们都要回调 b.Send），所以分发
	// 只能反向注入。做成 NewBot 的必填参数而不是可选字段：漏传直接
	// 编译不过，不会变成又一个「配置漏了但一切看起来正常」的失效点。
	dispatch Dispatcher

	// SummaryAt 是上一次发出私聊汇总的时刻（unix 秒），节流用。
	SummaryAt atomic.Int64

	// MinuteBusy 是分钟任务的单飞标记（撤回群内告警 + 私聊汇总）。
	// 这两件事要发 TG，TG 慢时不该让下一轮叠加，也不该拖住其他 bot。
	MinuteBusy atomic.Bool

	// seenUpdates 是最近处理过的 update_id，用来挡住 Telegram 的重推。
	//
	// webhook 模式下 TG 没收到 200（或网络中断）时会重推同一条更新；
	// 重复处理会让 msg_count 双计（把新人刷成老人）、让「切换」类按钮
	// 反转回原状态、再花一次 AI 的钱。长轮询靠 offset 天然去重，不走这里。
	seenMu    sync.Mutex
	seenOrder []int64
	seenSet   map[int64]struct{}
}

// seenUpdatesCap 是重推去重的窗口大小。TG 的重推发生在秒级，1024 条
// 足够覆盖；再早的重复即使漏掉，也早被护栏的 1 分钟窗口挡住了。
const seenUpdatesCap = 1024

// MarkUpdateSeen 报告这条 update 是否是第一次见到。update_id 为 0
// （测试或异常投递）不参与去重，一律按首次处理。
func (b *Bot) MarkUpdateSeen(updateID int64) bool {
	if updateID == 0 {
		return true
	}
	b.seenMu.Lock()
	defer b.seenMu.Unlock()
	if b.seenSet == nil {
		b.seenSet = make(map[int64]struct{}, seenUpdatesCap)
	}
	if _, dup := b.seenSet[updateID]; dup {
		return false
	}
	b.seenSet[updateID] = struct{}{}
	b.seenOrder = append(b.seenOrder, updateID)
	if len(b.seenOrder) > seenUpdatesCap {
		old := b.seenOrder[0]
		b.seenOrder = b.seenOrder[1:]
		delete(b.seenSet, old)
	}
	return true
}

const (
	// adWorkers 是每个 bot 的并发判定数。8 路在活跃群里排不开：单次判定
	// 最坏要几十秒（AI 重试预算 45 秒），几条慢请求就能把通道占满。
	//
	// ponytail: 每个 bot 独立一个池，初判叠加到上游的总并发上限仍是
	// adWorkers × bot 数；复判已经改成 Shared 级总闸（reviewSem）。
	adWorkers = 32
	// adQueueCap 是排队上限。worker 全忙时先排队而不是直接放行；
	// 队列也满说明上游整体慢了，此时再放行，防止积压无限增长。
	adQueueCap = 1024
	// adReviewConcurrency 是全局同时在跑的复判数上限。
	// 复判最慢、最贵，还会触发并发扇出，多 bot 叠加时上游最先被打爆的
	// 就是它；上游给的是同一份配额，所以闸不能按 bot 各开一份。
	adReviewConcurrency = 50
)

// UpdateQueueCap 是 webhook 队列容量。
// 满了就丢弃而不是阻塞：阻塞会让 TG 的投递超时并重推，重推又落到同一个
// 满队列上，雪崩只会更快。丢弃与「判定队列已满放行」是同一个失败方向。
const UpdateQueueCap = 256

func NewBot(t tg.Transport, sh *Shared, token string, d Dispatcher) *Bot {
	b := &Bot{Shared: sh, TG: t, Token: token, dispatch: d,
		adJobs:    make(chan func(), adQueueCap),
		adReviews: make(chan func(), adQueueCap),
		updates:   make(chan *tg.Update, UpdateQueueCap),
		done:      make(chan struct{})}
	for range adWorkers {
		go b.adWorker(b.adJobs)
		go b.adWorker(b.adReviews)
	}
	return b
}

// adWorker 消费一条判定队列，bot 停用时退出。队列里还没跑的任务随之丢弃：
// 与进程退出同策略，它们只会写自己的流水。
func (b *Bot) adWorker(jobs chan func()) {
	for {
		select {
		case <-b.done:
			return
		case job := <-jobs:
			job()
			b.adBusy.Add(-1)
		}
	}
}

// AdSubmit 把判定任务放进队列，从不阻塞调用方（更新处理的同步段）。
// 队列已满返回 false，由调用方决定如何放行。
func (b *Bot) AdSubmit(job func()) bool { return b.adEnqueue(b.adJobs, job) }

// AdReview 把大模型复判放进复判队列，满了返回 false（调用方按初判定案）。
//
// 执行前还要过 Shared 级的并发闸：闸取在 worker 里而不是入队时 —— 入队
// 必须立即返回（调用方还在更新的同步段上），排队中的任务也不该占着槽位。
// 等槽位的是复判 worker，初判队列有自己的 worker，不会被拖住。
func (b *Bot) AdReview(job func()) bool {
	return b.adEnqueue(b.adReviews, func() {
		b.reviewSem <- struct{}{}
		defer func() { <-b.reviewSem }()
		job()
	})
}

func (b *Bot) adEnqueue(jobs chan func(), job func()) bool {
	b.adBusy.Add(1)
	select {
	case jobs <- job:
		return true
	default:
		b.adBusy.Add(-1)
		return false
	}
}

// AdBusy 返回排队中与执行中的判定数，测试靠它等判定跑完。
func (b *Bot) AdBusy() int64 { return b.adBusy.Load() }

// shutdown 停掉这一个 bot 的 worker（更新队列与判定池）。可重复调用。
//
// 在飞的判定不等：它们最长几十秒，且并发有上限，全部只会写自己的流水。
// 与进程退出同策略。
func (b *Bot) Shutdown() {
	b.doneOnce.Do(func() { close(b.done) })
}

// owner 返回这个 bot 的归属人。
func (b *Bot) Owner() int64 { return b.OwnerID.Load() }

// IsMainBot 报告本实例是不是配置里的主 bot。
//
// 主 bot 只做配置管理与接入其他 bot：不入群、不判定广告，被拉进群自动退出。
// 每次从快照读而不是在实例上缓存一份：ensureMainBot 会在启动时纠正标记，
// 热重载之后要立刻生效。
func (b *Bot) IsMainBot() bool {
	rec := b.Cache.Snap().Bots[b.BotID()]
	return rec != nil && rec.IsMain
}

// alertTargets 返回这次告警该私聊谁。
//
// 归属人一定收到（bot 是他的，群也是他管的）；主管理员默认**不**收 ——
// 服务分发出去之后，每个次管的群都往主管私聊里灌等于把它变成日志流。
// 想收的话打开 alert_copy_main。
func (b *Bot) AlertTargets() []int64 {
	out := []int64{}
	if o := b.Owner(); o != 0 {
		out = append(out, o)
	}
	if b.Cache.Snap().SettingInt("alert_copy_main", 0) == 1 {
		for _, id := range b.Cfg.AdminIDs {
			if !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	return out
}

// fileTimeout 是下载 TG 文件（识图用）的超时。图片不大，拖过它多半是卡住了。
const fileTimeout = 20 * time.Second

// fileMaxBytes 是单个文件的下载上限，识图只要看清图里的字。
const fileMaxBytes = 5 << 20

// DownloadFile 下载 getFile 给出的文件路径。
//
// 地址里有 bot token，出错一律过 tg.CallError 剥掉 URL，否则 token 会进日志
// 与流水。代理与 Bot API 调用同一份（tgRoundTripper）。
func (b *Bot) DownloadFile(path string) ([]byte, error) {
	c := &http.Client{Timeout: fileTimeout, Transport: b.tgRoundTripper}
	resp, err := c.Get(strings.TrimRight(b.Cfg.TGAPIBase, "/") +
		"/file/bot" + b.Token + "/" + path)
	if err != nil {
		return nil, tg.CallError("download", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, fileMaxBytes))
}

// ---- 发送与编辑 ----

// messagePayload 组装 sendMessage 的公共字段。
//
// noPreview 为真时关掉链接预览：群内提示里有 deep link，客户端会挂一张
// 预览卡片（bot 链接常带 START 按钮），把一行提示撑成好几行，而群里
// 要的只是「谁、因为什么、点哪里申诉」。
func (b *Bot) messagePayload(chatID int64, text string, kb map[string]any,
	noPreview bool) map[string]any {
	p := map[string]any{"chat_id": chatID, "text": text, "parse_mode": "HTML"}
	if kb != nil {
		p["reply_markup"] = kb
	}
	if noPreview {
		p["link_preview_options"] = map[string]any{"is_disabled": true}
	}
	return p
}

// kb 用 map 而不是 any：把 nil map 存进 any 之后接口值并不等于 nil，
// 直接塞进 payload 会序列化成 null，TG 回「object expected as reply markup」。
// 类型是 map 时 nil 判断才可靠。
func (b *Bot) Send(chatID int64, text string, kb map[string]any) {
	if _, err := b.TG.Call("sendMessage",
		b.messagePayload(chatID, text, kb, false)); err != nil {
		slog.Error("sendMessage 失败", "err", err)
	}
}

// sendGetID 与 send 相同，但返回新消息的 message_id（失败返回 0）。
// 自动撤回要靠它：没有 message_id 就无从撤回。
func (b *Bot) SendGetID(chatID int64, text string, kb map[string]any) int64 {
	return b.sendMessageID(b.messagePayload(chatID, text, kb, false))
}

// SendGetIDNoPreview 与 SendGetID 相同，但关掉链接预览（群内提示用）。
func (b *Bot) SendGetIDNoPreview(chatID int64, text string, kb map[string]any) int64 {
	return b.sendMessageID(b.messagePayload(chatID, text, kb, true))
}

func (b *Bot) sendMessageID(p map[string]any) int64 {
	raw, err := b.TG.Call("sendMessage", p)
	if err != nil {
		slog.Error("sendMessage 失败", "err", err)
		return 0
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &resp) != nil || !resp.OK {
		return 0
	}
	return resp.Result.MessageID
}

func (b *Bot) Edit(chatID, msgID int64, text string, kb map[string]any) {
	p := map[string]any{"chat_id": chatID, "message_id": msgID,
		"text": text, "parse_mode": "HTML"}
	if kb != nil {
		p["reply_markup"] = kb
	}
	if _, err := b.TG.Call("editMessageText", p); err != nil {
		slog.Error("editMessageText 失败", "err", err)
	}
}

// editOrSend 统一处理「回调里编辑原消息 / 文本输入后新发一条」两种入口。
func (b *Bot) EditOrSend(chatID, msgID int64, text string, kb map[string]any) {
	if msgID == 0 {
		b.Send(chatID, text, kb)
		return
	}
	b.Edit(chatID, msgID, text, kb)
}

func (b *Bot) AnswerCallback(id, text string) {
	b.TG.Call("answerCallbackQuery", map[string]any{
		"callback_query_id": id, "text": text})
}

// callOK 发起调用并解析 TG 的 ok 字段。
// TG 的错误是 HTTP 200 + {"ok":false}，只看 err 会把业务失败当成功。
func (b *Bot) CallOK(method string, payload any) (bool, string) {
	raw, err := b.TG.Call(method, payload)
	if err != nil {
		return false, err.Error()
	}
	var r struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return false, "响应无法解析"
	}
	return r.OK, r.Description
}

// ---- 待输入会话 ----

// askInput 登记待输入会话并弹出输入框。
// keepInput 续上一次输入会话但不另发提示 —— 提示已经写在别的消息里了。
//
// 不能只是「不删 pending」：会话有 TTL，输错一次之后隔几分钟才重发的人
// 会撞上一个已经过期的会话，而他看到的提示明明写着「请重新发送」。
func (b *Bot) KeepInput(uid int64, op, target string) {
	b.pending.Store(uid, PendingInput{Op: op, Target: target,
		expire: time.Now().Add(pendingTTL)})
}

func (b *Bot) AskInput(chatID, uid int64, op, target, prompt string) {
	b.KeepInput(uid, op, target)
	b.TG.Call("sendMessage", map[string]any{
		"chat_id": chatID, "text": prompt, "parse_mode": "HTML",
		"reply_markup": map[string]any{"force_reply": true},
	})
}

// takePending 取出未过期的会话。过期条目顺手删除。
func (b *Bot) TakePending(uid int64) (PendingInput, bool) {
	v, ok := b.pending.Load(uid)
	if !ok {
		return PendingInput{}, false
	}
	p := v.(PendingInput)
	if time.Now().After(p.expire) {
		b.pending.Delete(uid)
		return PendingInput{}, false
	}
	return p, true
}

// DropPending 丢弃某人的待输入会话。面板在「权限不足」「输入无法解析」
// 这类分支里要主动清掉它，否则下一条无关消息会被当成这次的输入。
func (b *Bot) DropPending(uid int64) { b.pending.Delete(uid) }

// GCPending 清理过期的待输入会话。
func (b *Bot) GCPending() {
	now := time.Now()
	b.pending.Range(func(k, v any) bool {
		if p, ok := v.(PendingInput); ok && now.After(p.expire) {
			b.pending.Delete(k)
		}
		return true
	})
}

// Dispatcher 把一条 Update 交给业务层处理。
type Dispatcher func(*Bot, *tg.Update)

// ---- 命令菜单 ----

// adGroupCmds 是群聊里可见的命令。
//
// /check 对所有人开放是有意的：它不直接封禁，走的是与自动判定同一套处置
// 矩阵，而群成员往往比管理员更早发现广告。花钱入口由 antiad_cmd_rpm 把关。
//
// /white 与 /uad 也是群管理员及以上能用（前者永久放行、后者一次性解封）。
// /ban 只有群管理员及以上能用，但命令菜单是全群可见的 —— TG 的
// setMyCommands 没有「只给管理员看」的 scope。非授权者用了会被静默
// 忽略，这比藏起来更好：藏不住，还不如让群管一眼看见自己有这个工具。
var adGroupCmds = []map[string]string{
	{"command": "check", "description": "复查某人是否在发广告（回复消息或 /check user_id）"},
	{"command": "ban", "description": "标记为广告并处置（群管理员：回复消息或 /ban user_id）"},
	{"command": "white", "description": "加入本群反广告白名单（群管理员，回复对方的消息）"},
	{"command": "uad", "description": "解除某人在本群的限制（群管理员，回复对方的消息）"},
	{"command": "jtime", "description": "查看入群时间（回复消息或 /jtime user_id；直接发 /jtime 查自己）"},
}

func (b *Bot) RegisterCommands() {
	// 私聊默认 scope：只有 /start。管理面板的存在不对外暴露。
	if ok, desc := b.CallOK("setMyCommands", map[string]any{
		"commands": []map[string]string{{"command": "start", "description": "开始使用"}},
		"scope":    map[string]any{"type": "default"},
	}); !ok {
		slog.Error("注册默认命令菜单失败", "tg_error", desc)
	}
	// 群聊 scope：/check /ban /white /uad（见 adGroupCmds）。与默认 scope
	// 分开设置，否则私聊里也会冒出一堆在私聊中毫无意义的群命令。
	//
	// 主 bot 不入群，这份菜单永远不会被看到；升级前注册过的还要清掉，
	// 否则它短暂停留在某个群里时，群成员会看到一组点不动的命令。
	if b.IsMainBot() {
		if ok, desc := b.CallOK("deleteMyCommands", map[string]any{
			"scope": map[string]any{"type": "all_group_chats"},
		}); !ok {
			slog.Warn("清理主 bot 的群命令菜单失败", "tg_error", desc)
		}
	} else if ok, desc := b.CallOK("setMyCommands", map[string]any{
		"commands": adGroupCmds,
		"scope":    map[string]any{"type": "all_group_chats"},
	}); !ok {
		slog.Warn("注册群命令菜单失败", "tg_error", desc)
	}

	for _, id := range b.Cfg.AdminIDs {
		b.registerAdminCommands(id)
	}
	b.registerMiniAppButton()
}

// RegisterMiniAppButtonFor 给某个管理员补挂 Mini App 菜单按钮。
//
// 按钮是 per-chat 的，必须由 bot 主动设置。新增次级管理员时立刻补一次，
// 否则他要等到下次重启才收到按钮——点菜单也打不开配置台。
func (b *Bot) RegisterMiniAppButtonFor(uid int64) {
	if b.Cfg.PublicURL == "" {
		return
	}
	url := strings.TrimRight(b.Cfg.PublicURL, "/") + "/miniapp?bot=" +
		strconv.FormatInt(b.BotID(), 10)
	if ok, desc := b.CallOK("setChatMenuButton", map[string]any{
		"chat_id": uid,
		"menu_button": map[string]any{
			"type": "web_app", "text": "配置",
			"web_app": map[string]any{"url": url},
		},
	}); !ok {
		// 管理员还没和 bot 建立会话时必然失败，会在下次交互时随
		// EnsureAdminCommands 一起补；不打扰日志以外的地方。
		slog.Warn("注册 Mini App 菜单按钮失败", "admin", uid, "tg_error", desc)
	}
}

// registerMiniAppButton 给所有管理员挂上 Mini App 的菜单按钮。
//
// 只挂在管理员的私聊里：服务端还会再验一遍 initData 与权限，但按钮本身
// 不该出现在普通用户的界面上。需要 public_url（Mini App 只在 webhook 模式存在）。
func (b *Bot) registerMiniAppButton() {
	if b.Cfg.PublicURL == "" {
		return
	}
	ids := append([]int64{}, b.Cfg.AdminIDs...)
	for id := range b.Cache.Snap().Admins {
		ids = append(ids, id)
	}
	for _, id := range ids {
		b.RegisterMiniAppButtonFor(id)
	}
}

var adminCmds = []map[string]string{
	{"command": "start", "description": "打开管理面板"},
	{"command": "log", "description": "查看某条判定记录（/log 记录号）"},
	{"command": "user", "description": "查看某人的资料与处置记录（/user user_id）"},
	{"command": "white", "description": "把某人加入本 bot 的豁免名单"},
}

// registerAdminCommands 为单个管理员设置 chat scope 的命令菜单。
//
// 这一步在服务启动时很可能失败：setMyCommands 的 chat scope 要求会话已经
// 存在，而全新部署时管理员往往还没和 bot 说过话，TG 会返回 chat not found。
// 因此失败要记日志，并在该管理员首次发消息时由 ensureAdminCommands 补上
// ——否则命令菜单会一直是空的，且无人知晓。
func (b *Bot) registerAdminCommands(id int64) {
	ok, desc := b.CallOK("setMyCommands", map[string]any{
		"commands": adminCmds,
		"scope":    map[string]any{"type": "chat", "chat_id": id},
	})
	if ok {
		b.adminCmdsDone.Store(id, true)
		return
	}
	slog.Warn("管理员命令菜单注册失败，将在其首次交互时重试",
		"admin", id, "tg_error", desc)
}

// ensureAdminCommands 在管理员交互时补注册，只成功一次。
func (b *Bot) EnsureAdminCommands(id int64) {
	if !b.Cfg.IsAdmin(id) {
		return
	}
	if _, done := b.adminCmdsDone.Load(id); done {
		return
	}
	b.registerAdminCommands(id)
}

// ---- 两种接入模式 ----

// pollAllowedUpdates 是 allowed_updates 清单，轮询与 setWebhook 共用。
//
// 必须显式声明：Telegram 的默认清单不含 chat_member，不声明就一条进群
// 事件都收不到，反广告的「新人 / 老人」分档会全部退化成「年龄未知」。
// 而一旦显式声明，默认清单就整体失效，因此 message 与 callback_query
// 也要原样列出，漏写哪一个哪一个就彻底收不到，且 TG 不会因此报任何错。
// edited_message 同理：不列就收不到编辑，「先发正常、再编辑成广告」畅通无阻。
func PollAllowedUpdates() []string {
	return []string{"message", "edited_message", "callback_query", "chat_member", "my_chat_member"}
}

// enqueue 把一条 update 投进串行队列，队列已满时返回 false。
//
// 重推的更新在这里被挡下：TG 没收到 200 时会重发同一条 update_id，
// 重复处理等于把同一条消息再判一次（msg_count 双计、按钮反转、重复花钱）。
// 重推返回 true（当作已接收），否则 TG 会继续重推同一条。
func (b *Bot) Enqueue(u *tg.Update) bool {
	if u == nil {
		return true
	}
	if !b.MarkUpdateSeen(u.UpdateID) {
		slog.Info("webhook：重复投递的更新已忽略", "update_id", u.UpdateID)
		return true
	}
	select {
	case b.updates <- u:
		return true
	default:
		return false
	}
}

// runQueueWorker 串行消费 webhook 投递进来的 update。
//
// 只有这一个 goroutine 在跑 handleUpdate，因此语义与长轮询完全一致：
// 反广告的上下文入环顺序、待输入会话的先后都得到保证。
func (b *Bot) RunQueueWorker(stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case <-b.done: // 这一个 bot 被停用或删除
			return
		case u := <-b.updates:
			b.dispatch(b, u)
		}
	}
}

// setWebhook 把回调地址注册到 Telegram。
//
// allowed_updates 与轮询共用同一份清单——这里漏一项的后果与轮询侧
// 完全相同，且同样不会报错。
func (b *Bot) SetWebhook(url string) error {
	ok, desc := b.CallOK("setWebhook", map[string]any{
		"url":             url,
		"allowed_updates": PollAllowedUpdates(),
		// 已有的 webhook 指向别处时直接覆盖，不必先 delete。
		"max_connections": 40,
	})
	if !ok {
		return fmt.Errorf("setWebhook 失败: %s", desc)
	}
	return nil
}

// dropWebhook 撤掉已注册的 webhook。
//
// 轮询模式启动前必须调用：getUpdates 与 webhook 互斥，库里还留着上一次
// 的 webhook 时，getUpdates 会一直拿 409 Conflict，而 bot 表现为
// 「一条消息都收不到」，日志里只有一行看不出所以然的 409。
func (b *Bot) DropWebhook() {
	if ok, desc := b.CallOK("deleteWebhook", map[string]any{}); !ok {
		slog.Warn("撤销 webhook 失败，若此前设过 webhook，长轮询可能收不到更新",
			"tg_error", desc)
	}
}

func (b *Bot) RunPolling(stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
		}
		raw, err := b.TG.Call("getUpdates", map[string]any{
			"offset": b.offset, "timeout": 25, "limit": 50,
			"allowed_updates": PollAllowedUpdates(),
		})
		if err != nil {
			slog.Error("getUpdates 失败", "err", err)
			if sleepOrStop(stop, 3*time.Second) {
				return
			}
			continue
		}
		var resp struct {
			OK          bool         `json:"ok"`
			Description string       `json:"description"`
			Result      []*tg.Update `json:"result"`
		}
		if json.Unmarshal(raw, &resp) != nil || !resp.OK {
			// 常见于 bot_token 无效或 webhook 未撤销（409）：
			// 如实打出 TG 的 description，否则运维无从判断
			slog.Warn("getUpdates 返回异常，稍后重试", "tg_error", resp.Description)
			if sleepOrStop(stop, time.Second) {
				return
			}
			continue
		}
		for _, u := range resp.Result {
			if u.UpdateID >= b.offset {
				b.offset = u.UpdateID + 1
			}
			b.dispatch(b, u)
		}
	}
}

// sleepOrStop 等待 d，或在收到停止信号时提前返回 true。
// 直接 time.Sleep 会让退避期间的关闭信号被拖延最多 3 秒。
func sleepOrStop(stop <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stop:
		return true
	case <-t.C:
		return false
	}
}

// BotID 取 bot 自身的 user_id。结果缓存在内存里 ——
// getMe 的结果在进程生命周期内不会变。
//
// 返回 0 表示 token 无效或 TG 不可达，webhook 侧据此拒绝接入。
func (b *Bot) BotID() int64 {
	if id := b.SelfID.Load(); id != 0 {
		return id
	}
	raw, err := b.TG.Call("getMe", nil)
	if err != nil {
		return 0
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			ID int64 `json:"id"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &resp) != nil || !resp.OK {
		return 0
	}
	b.SelfID.Store(resp.Result.ID)
	return resp.Result.ID
}
