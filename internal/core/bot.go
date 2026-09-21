package core

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"menshen/internal/captcha"
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

	// aiClient 专供反广告判定调用上游。http.Client 并发安全，
	// 共享还能复用连接池。
	AIClient *http.Client

	// captcha 是自助解除限制时的人机验证题库。跨 bot 共享：
	// 同一个人在两个群被限制时，不该因为换了个 bot 就绕过答题次数。
	Captcha *captcha.Store

	// unbanGate 是自助解除的重试闸，键为 uid。
	// 不限次数但间隔递增，见 nextUnbanDelay。
	UnbanGate sync.Map // int64(uid) -> unbanAttempt

	// transportFor 非空时用它建传输层，供测试整体替换掉真实 HTTP。
	TransportFor func(token string) tg.Transport

	// reg 是 bot 实例总表。联合封禁要遍历「所有 bot 的所有群」，
	// 而那件事不属于任何一个 bot。由 newBotRegistry 回填。
	Reg *Registry
}

func NewShared(cfg *config.Config, s *store.Store, c *store.Cache) *Shared {
	return &Shared{Cfg: cfg, Store: s, Cache: c,
		AdLimits: ratelimit.New(),
		Captcha:  captcha.NewStore(),
		AIClient: &http.Client{Timeout: aiClientTimeout}}
}

// aiClientTimeout 是判定调用 AI 上游的整体超时。
// 一条群消息的同步判定，拖过 20 秒结果已经没有意义。
const aiClientTimeout = 20 * time.Second

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
	AdCtx   CtxRing  // 各群最近消息，供判定时作上下文
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

	// adSem 限制同时在飞的反广告判定数。判定已经挪出轮询 goroutine，
	// 但不设上限的话 goroutine 数就等于群消息速率，上游一慢就会堆成
	// 几千个在飞的请求——把「卡住轮询」换成「打爆内存和上游」而已。
	AdSem chan struct{}

	// updates 是 webhook 模式下的串行投递队列。
	//
	// 必须串行：反广告的上下文环按消息到达顺序入环，每个 HTTP 请求
	// 各起一个 goroutine 会让 recent_context 的顺序随调度乱掉，
	// 模型就会看到乱序的对话、甚至看到自己正要判的那一条。
	// 队列把轮询的「单 goroutine 依次处理」语义原样搬了过来。
	updates chan *tg.Update

	// dispatch 是 Update 的业务分发入口，由 main 在构造时注入。
	//
	// core 不能 import antiad/panel（它们都要回调 b.Send），所以分发
	// 只能反向注入。做成 NewBot 的必填参数而不是可选字段：漏传直接
	// 编译不过，不会变成又一个「配置漏了但一切看起来正常」的失效点。
	dispatch Dispatcher
}

// adMaxConcurrent 是并发判定上限。取 8：足够让一个活跃群不至于因为
// 单条慢请求就排队，又不会在上游整体变慢时堆积过多在飞请求。
const adMaxConcurrent = 8

// UpdateQueueCap 是 webhook 队列容量。
// 满了就丢弃而不是阻塞：阻塞会让 TG 的投递超时并重推，重推又落到同一个
// 满队列上，雪崩只会更快。丢弃与「判定并发已满放行」是同一个失败方向。
const UpdateQueueCap = 256

func NewBot(t tg.Transport, sh *Shared, token string, d Dispatcher) *Bot {
	return &Bot{Shared: sh, TG: t, Token: token, dispatch: d,
		AdSem:   make(chan struct{}, adMaxConcurrent),
		updates: make(chan *tg.Update, UpdateQueueCap),
		done:    make(chan struct{})}
}

// shutdown 停掉这一个 bot 的 worker。可重复调用。
//
// 在飞的判定 goroutine 不等：它们最长几十秒，且并发有上限，全部只会写
// 自己的流水。与进程退出同策略。
func (b *Bot) Shutdown() {
	b.doneOnce.Do(func() { close(b.done) })
}

// owner 返回这个 bot 的归属人。
func (b *Bot) Owner() int64 { return b.OwnerID.Load() }

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

// ---- 发送与编辑 ----

func (b *Bot) Send(chatID int64, text string, kb any) {
	p := map[string]any{"chat_id": chatID, "text": text, "parse_mode": "HTML"}
	if kb != nil {
		p["reply_markup"] = kb
	}
	if _, err := b.TG.Call("sendMessage", p); err != nil {
		slog.Error("sendMessage 失败", "err", err)
	}
}

// sendGetID 与 send 相同，但返回新消息的 message_id（失败返回 0）。
// 自动撤回要靠它：没有 message_id 就无从撤回。
func (b *Bot) SendGetID(chatID int64, text string, kb any) int64 {
	p := map[string]any{"chat_id": chatID, "text": text, "parse_mode": "HTML"}
	if kb != nil {
		p["reply_markup"] = kb
	}
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

func (b *Bot) Edit(chatID, msgID int64, text string, kb any) {
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
func (b *Bot) EditOrSend(chatID, msgID int64, text string, kb any) {
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
// /ad 对所有人开放是有意的：它不直接封禁，走的是与自动判定同一套处置
// 矩阵，而群成员往往比管理员更早发现广告。花钱入口由 antiad_cmd_rpm 把关。
//
// /adb 只有群管理员及以上能用，但命令菜单是全群可见的 —— TG 的
// setMyCommands 没有「只给管理员看」的 scope。非授权者用了会被静默
// 忽略，这比藏起来更好：藏不住，还不如让群管一眼看见自己有这个工具。
var adGroupCmds = []map[string]string{
	{"command": "ad", "description": "复查某人是否在发广告（回复对方的消息）"},
	{"command": "adb", "description": "标记为广告并处置（群管理员，回复对方的消息）"},
}

func (b *Bot) RegisterCommands() {
	// 私聊默认 scope：只有 /start。管理面板的存在不对外暴露。
	if ok, desc := b.CallOK("setMyCommands", map[string]any{
		"commands": []map[string]string{{"command": "start", "description": "开始使用"}},
		"scope":    map[string]any{"type": "default"},
	}); !ok {
		slog.Error("注册默认命令菜单失败", "tg_error", desc)
	}
	// 群聊 scope：/ad。与默认 scope 分开设置，否则私聊里也会冒出
	// 一个在私聊中毫无意义的复查命令。
	if ok, desc := b.CallOK("setMyCommands", map[string]any{
		"commands": adGroupCmds,
		"scope":    map[string]any{"type": "all_group_chats"},
	}); !ok {
		slog.Warn("注册群命令菜单失败", "tg_error", desc)
	}

	for _, id := range b.Cfg.AdminIDs {
		b.registerAdminCommands(id)
	}
}

var adminCmds = []map[string]string{
	{"command": "start", "description": "打开管理面板"},
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
func PollAllowedUpdates() []string {
	return []string{"message", "callback_query", "chat_member", "my_chat_member"}
}

// enqueue 把一条 update 投进串行队列，队列已满时返回 false。
func (b *Bot) Enqueue(u *tg.Update) bool {
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
