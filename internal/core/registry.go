package core

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"menshen/internal/config"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// Registry 管理所有接入的 bot 实例，是 webhook 分发与面板操作的共同入口。
//
// 接入是**注册制**：token 必须先由某个管理员在面板上登记（写进 bots 表、
// 绑定 owner），webhook 才认它。早先那种「任何 getMe 通过的 token 都能接
// 进来」在多租户下站不住 —— 没有归属就谈不上谁能管它、谁为它的开销负责。
type Registry struct {
	sh   *Shared
	stop <-chan struct{}

	// dispatch 透传给每个新建的 Bot 实例。
	dispatch Dispatcher

	mu      sync.Mutex
	byToken map[string]*Bot
	byID    map[int64]*Bot
}

func NewRegistry(sh *Shared, stop <-chan struct{}, d Dispatcher) *Registry {
	r := &Registry{sh: sh, stop: stop, dispatch: d,
		byToken: map[string]*Bot{}, byID: map[int64]*Bot{}}
	// 回填：联合封禁那类「跨全部 bot」的操作要从 Shared 走到这里。
	sh.Reg = r
	return r
}

func (r *Registry) LookupToken(token string) (*Bot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.byToken[token]
	return b, ok
}

func (r *Registry) LookupID(botID int64) (*Bot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.byID[botID]
	return b, ok
}

// each 遍历当前全部实例，供定时任务与联合封禁使用。
func (r *Registry) Each(fn func(*Bot)) {
	r.mu.Lock()
	list := make([]*Bot, 0, len(r.byID))
	for _, b := range r.byID {
		list = append(list, b)
	}
	r.mu.Unlock()
	for _, b := range list {
		fn(b)
	}
}

func (r *Registry) Size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byID)
}

// newTransport 按 token 建一个指向 Telegram 的传输层。
// 抽出来是为了让测试能整体替换（见 transportFor）。
func (sh *Shared) newTransport(token string) tg.Transport {
	if sh.TransportFor != nil {
		return sh.TransportFor(token)
	}
	return tg.NewHTTP(sh.Cfg.TGAPIBase, token)

}

// spawn 建实例、登记、启 worker。调用方须持有 r.mu。
func (r *Registry) spawn(rec *store.BotRec) *Bot {
	b := NewBot(r.sh.newTransport(rec.Token), r.sh, rec.Token, r.dispatch)
	b.SelfID.Store(rec.BotID)
	b.OwnerID.Store(rec.OwnerID)
	b.Username = rec.Username
	r.byToken[rec.Token] = b
	r.byID[rec.BotID] = b
	go b.RunQueueWorker(r.stop)
	return b
}

// loadAll 从 bots 表把所有已启用的 bot 拉起来。启动时调用一次。
//
// 已停用的 bot 不建实例：webhook 打过来会找不到实例而被拒，
// 这正是「停用」应有的表现 —— 比留着实例再逐条丢弃更省也更明确。
func (r *Registry) LoadAll() {
	snap := r.sh.Cache.Snap()
	polling := !r.sh.Cfg.UseWebhook()

	r.mu.Lock()
	defer r.mu.Unlock()
	skipped := 0
	for _, rec := range snap.Bots {
		if !rec.Enabled {
			continue
		}
		if _, ok := r.byID[rec.BotID]; ok {
			continue
		}
		// 长轮询模式下只拉起主 bot。给子 bot 建实例也不会有人去轮询
		// 它们，只是让面板显示它们在运行，而它们一条更新都收不到。
		// 记录留在表里不动：切回 webhook 模式后重启即自动恢复。
		if polling && rec.Token != r.sh.Cfg.BotToken {
			skipped++
			continue
		}
		r.spawn(rec)
	}
	if skipped > 0 {
		slog.Warn("长轮询模式：子 bot 未启动，它们收不到任何更新",
			"数量", skipped, "解决办法", "配置 public_url 切到 webhook 模式")
	}
	slog.Info("已加载 bot 实例", "数量", len(r.byID))
}

// probeBot 用 getMe 验真并取回身份。
//
// 这是注册路径上唯一一次网络校验，必须做：token 的格式校验挡得住随手敲
// 的垃圾，挡不住照着格式编的假串，而一个假 token 注册进去之后，它名下的
// 群会永远收不到任何更新，面板上却显示一切正常。
func (sh *Shared) ProbeBot(token string) (int64, string, error) {
	raw, err := sh.newTransport(token).Call("getMe", nil)
	if err != nil {
		return 0, "", fmt.Errorf("连不上 Telegram: %w", err)
	}
	var resp struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &resp) != nil {
		return 0, "", fmt.Errorf("Telegram 响应无法解析")
	}
	if !resp.OK || resp.Result.ID == 0 {
		desc := resp.Description
		if desc == "" {
			desc = "token 无效"
		}
		return 0, "", fmt.Errorf("%s", desc)
	}
	return resp.Result.ID, resp.Result.Username, nil
}

// precheck 是接入前不出网就能做完的全部校验。
//
// 单独抽出来是为了让调用方先挡掉绝大多数失败：格式敲错、重复接入、
// 超配额这三类占了失败的大头，先花一次 getMe 的往返再告诉他「你已经
// 加过了」既慢又蠢，何况那一次往返本身就可能卡上几十秒。
//
// 返回的错误是给管理员看的人话，可以直接贴进 TG 消息。
func (r *Registry) Precheck(token string, ownerID int64) error {
	if !config.ValidToken(token) {
		return fmt.Errorf("token 格式不对，应形如 <code>123456789:AA...</code>")
	}

	// 子 bot **只能**走 webhook。长轮询要为每个 bot 各开一条 getUpdates
	// 长连接，而 Telegram 的限速同时按 bot 和按出口 IP 算 —— 几个 bot
	// 一起轮询会互相挤掉配额，表现是所有 bot 一起变慢、一起丢更新，
	// 且没有任何一条日志指向真正的原因。
	//
	// 配置里那个主 bot 例外：它就是长轮询模式下唯一的那条通道。
	if !r.sh.Cfg.UseWebhook() && token != r.sh.Cfg.BotToken {
		return fmt.Errorf("本服务当前是<b>长轮询模式</b>，只能服务配置里那一个 bot。\n\n" +
			"长轮询要为每个 bot 各开一条轮询连接，几个 bot 一起轮询会撞上 " +
			"Telegram 的限速、互相挤掉配额。接入更多 bot 需要主管理员配置 " +
			"<code>public_url</code> 切到 webhook 模式。")
	}

	snap := r.sh.Cache.Snap()
	if _, dup := snap.BotTokens[token]; dup {
		return fmt.Errorf("这个 bot 已经接入过了")
	}
	// 配额按人算，主管理员不受限。
	if !r.sh.IsMain(ownerID) {
		limit := snap.SettingInt("max_bots_per_admin", 5)
		if limit > 0 && int64(len(snap.BotsOwnedBy(ownerID, false))) >= limit {
			return fmt.Errorf("你名下的 bot 已达上限（%d 个）", limit)
		}
	}
	return nil
}

// register 登记一个新 bot 并立即拉起来。
//
// 它会打一次 getMe，可能卡上几十秒，所以调用方应当在独立 goroutine 里
// 调它 —— 更新处理是串行的，同步等在这里会让整个 bot 停摆。
//
// 注册完还要 finishSetup（命令菜单 + webhook）才算真的能收消息，
// 那一步分开是因为它的失败不该让接入整体回滚：webhook 没设上可以重试，
// 而把已经落库的 bot 再删掉只会让人更糊涂。
func (r *Registry) Register(token string, ownerID int64) (*store.BotRec, error) {
	if err := r.Precheck(token, ownerID); err != nil {
		return nil, err
	}

	botID, username, err := r.sh.ProbeBot(token)
	if err != nil {
		return nil, fmt.Errorf("验证失败：%v", err)
	}
	snap := r.sh.Cache.Snap()
	// 同一个 bot 换了 token 重新注册：bots.bot_id 上有唯一索引，直接
	// INSERT 会撞约束。明确拒绝比丢一条 SQL 错误给管理员强。
	if old, ok := snap.Bots[botID]; ok {
		return nil, fmt.Errorf("该 bot（%s）已由他人接入，请先在原处删除", old.Label())
	}

	if _, err := r.sh.Store.Write.Exec(`INSERT INTO bots
		(token,bot_id,username,owner_id,so_model,llm_model,enabled,created_at)
		VALUES (?,?,?,?,'','',1,?)`,
		token, botID, username, ownerID, time.Now().Unix()); err != nil {
		slog.Error("注册 bot 失败", "bot_id", botID, "err", err)
		return nil, fmt.Errorf("落库失败")
	}
	if err := r.sh.Cache.Reload(); err != nil {
		slog.Error("注册 bot 后 reload 失败", "bot_id", botID, "err", err)
	}

	rec := r.sh.Cache.Snap().Bots[botID]
	if rec == nil {
		return nil, fmt.Errorf("落库后读不回记录")
	}

	r.mu.Lock()
	r.spawn(rec)
	r.mu.Unlock()

	slog.Info("bot 已接入", "bot_id", botID, "username", username, "owner", ownerID)
	return rec, nil
}

// finishSetup 把一个刚接入（或刚重新启用）的 bot 准备到能干活的状态：
// 注册命令菜单，并在 webhook 模式下挂上回调地址。
//
// 轮询模式下**不设 webhook**：getUpdates 与 webhook 互斥，设了会让
// getUpdates 一直拿 409，而表现是「一条消息都收不到」。
func (b *Bot) FinishSetup() error {
	// 命令菜单失败无关紧要（多半是对方还没和这个 bot 说过话），
	// 不该盖掉真正要紧的 webhook 结果。
	b.RegisterCommands()

	if !b.Cfg.UseWebhook() {
		return nil
	}
	if err := b.SetWebhook(b.Cfg.WebhookURLFor(b.Token)); err != nil {
		slog.Error("注册 webhook 失败", "bot", config.MaskToken(b.Token), "err", err)
		return err
	}
	return nil
}

// unregister 摘掉一个 bot：撤 webhook、删配置、停实例。
//
// 它名下的 bot_chats / bot_settings 一并删除 —— 留着只会在下次有人用
// 同一个 bot_id 接入时，让他莫名其妙地继承一批陌生的群配置。
// 判定流水（antiad_log）保留：那是账本，不因为 bot 走了就该抹掉。
func (r *Registry) Unregister(botID int64) error {
	rec := r.sh.Cache.Snap().Bots[botID]
	if rec == nil {
		return fmt.Errorf("该 bot 不存在")
	}

	r.mu.Lock()
	b := r.byID[botID]
	delete(r.byID, botID)
	delete(r.byToken, rec.Token)
	r.mu.Unlock()

	if b != nil {
		b.Shutdown()
		// 撤 webhook 放异步：对方可能已经吊销了 token，这一步会一直等到超时。
		go b.DropWebhook()
	}

	tx, err := r.sh.Store.Write.Begin()
	if err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM bot_chats WHERE bot_id=?`,
		`DELETE FROM bot_settings WHERE bot_id=?`,
		`DELETE FROM join_mutes WHERE bot_id=?`,
		`DELETE FROM bots WHERE bot_id=?`,
	} {
		if _, err := tx.Exec(q, botID); err != nil {
			tx.Rollback()
			slog.Error("删除 bot 失败", "bot_id", botID, "err", err)
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := r.sh.Cache.Reload(); err != nil {
		slog.Error("删除 bot 后 reload 失败", "bot_id", botID, "err", err)
	}
	slog.Info("bot 已移除", "bot_id", botID)
	return nil
}

// setBotEnabled 启停一个 bot。停用即摘掉实例，webhook 打过来会被拒；
// 重新启用时再拉起来并补设一次 webhook。
func (r *Registry) SetBotEnabled(botID int64, on bool) error {
	v := 0
	if on {
		v = 1
	}
	if _, err := r.sh.Store.Write.Exec(
		`UPDATE bots SET enabled=? WHERE bot_id=?`, v, botID); err != nil {
		return err
	}
	if err := r.sh.Cache.Reload(); err != nil {
		return err
	}

	rec := r.sh.Cache.Snap().Bots[botID]
	if rec == nil {
		return fmt.Errorf("该 bot 不存在")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if on {
		// 长轮询模式下子 bot 不建实例，理由同 loadAll。标记照样翻过来，
		// 切到 webhook 模式重启后它就跟着起来了。
		if polling := !r.sh.Cfg.UseWebhook(); polling && rec.Token != r.sh.Cfg.BotToken {
			return nil
		}
		if _, ok := r.byID[botID]; !ok {
			b := r.spawn(rec)
			// 放异步：调用方正卡在一条 TG 回调的响应路径上，
			// 而 setWebhook 最坏要等到传输层超时。
			go b.FinishSetup()
		}
		return nil
	}
	if b, ok := r.byID[botID]; ok {
		delete(r.byID, botID)
		delete(r.byToken, rec.Token)
		b.Shutdown()
	}
	return nil
}

// setBotOwner 改派归属。只有主管理员能调。
func (sh *Shared) SetBotOwner(botID, ownerID int64) error {
	if _, err := sh.Store.Write.Exec(
		`UPDATE bots SET owner_id=? WHERE bot_id=?`, ownerID, botID); err != nil {
		return err
	}
	if err := sh.Cache.Reload(); err != nil {
		return err
	}
	// 实例上缓存了一份 owner，用于告警投递，要同步。
	return nil
}

// setBotModel 为单个 bot 覆盖模型。空串表示回到全局默认。
// which 取 "so" 或 "llm"。
func (sh *Shared) SetBotModel(botID int64, which, model string) error {
	col := "so_model"
	if which == "llm" {
		col = "llm_model"
	}
	// col 只来自上面两个字面量，不存在注入面。
	if _, err := sh.Store.Write.Exec(
		`UPDATE bots SET `+col+`=? WHERE bot_id=?`, model, botID); err != nil {
		return err
	}
	return sh.Cache.Reload()
}
