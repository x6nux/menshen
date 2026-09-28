package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"menshen/internal/config"
	"menshen/internal/core"
	"menshen/internal/store"
)

var (
	version   = "2.2.0"
	buildTime = "unknown" // 由构建脚本通过 ldflags 注入
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "配置文件路径")
	showVer := flag.Bool("version", false, "打印版本信息并退出")
	flag.Parse()

	if *showVer {
		slog.Info("menshen", "version", version, "built", buildTime)
		return
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("加载配置失败", "err", err)
		os.Exit(1)
	}

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		slog.Error("打开数据库失败", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	cache, err := store.NewCache(db)
	if err != nil {
		slog.Error("构建缓存失败", "err", err)
		os.Exit(1)
	}

	sh := core.NewShared(cfg, db, cache)
	stop := make(chan struct{})
	reg := core.NewRegistry(sh, stop, dispatch)

	// 配置里那个 bot 自动注册给第一个主管理员，省掉「部署完还要先去
	// 面板里把自己加一遍」这一步。其余 bot 由管理员在面板上接入。
	if cfg.BotToken != "" {
		if err := ensureMainBot(cfg, sh, reg); err != nil {
			slog.Error("配置里的 bot 无法接入", "err", err)
			if !cfg.UseWebhook() {
				// 轮询模式下它是唯一的输入通道，没有它就没有服务。
				os.Exit(1)
			}
		}
	}
	reg.LoadAll()
	migrateLegacyChats(sh, reg)

	var srv *http.Server
	if cfg.UseWebhook() {
		srv = startWebhook(cfg, reg, sh)
	} else {
		startPolling(cfg, reg, stop)
	}

	go runBackgroundTasks(stop, sh, reg)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	slog.Info("正在关闭")
	close(stop)
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}
}

// ensureMainBot 把配置里的 bot_token 登记进 bots 表并标记为主 bot。
//
// 主 bot 只做配置管理与接入其他 bot：不入群、不判定广告，被拉进群自动
// 退出。标记落库（bots.is_main）而不是每次比对 cfg.BotToken —— webhook
// 模式下该字段可为空，配置一改就会漂移。
//
// 顺带做两件纠偏：
//   - 配置换过 bot 时，旧主 bot 的标记摘掉（它降级为普通工作 bot）
//   - 主 bot 名下遗留的群配置删掉：升级前它可能在群里判过，新语义下
//     留着只会让面板显示与行为对不上
func ensureMainBot(cfg *config.Config, sh *core.Shared, reg *core.Registry) error {
	snap := sh.Cache.Snap()
	rec, ok := snap.BotTokens[cfg.BotToken]
	if !ok {
		if _, err := reg.Register(cfg.BotToken, cfg.AdminIDs[0], true); err != nil {
			return err
		}
		if rec = sh.Cache.Snap().BotTokens[cfg.BotToken]; rec == nil {
			return fmt.Errorf("主 bot 登记后读不回记录")
		}
		slog.Info("配置里的 bot 已自动接入",
			"bot", rec.Label(), "owner", cfg.AdminIDs[0])
	} else if !rec.IsMain {
		if _, err := sh.Store.Write.Exec(
			`UPDATE bots SET is_main=1 WHERE bot_id=?`, rec.BotID); err != nil {
			return err
		}
		slog.Info("已把配置里的 bot 标记为主 bot", "bot", rec.Label())
	}

	// 主 bot 只有一个：其余一律是工作 bot。
	if _, err := sh.Store.Write.Exec(
		`UPDATE bots SET is_main=0 WHERE bot_id<>?`, rec.BotID); err != nil {
		return err
	}
	if res, err := sh.Store.Write.Exec(
		`DELETE FROM bot_chats WHERE bot_id=?`, rec.BotID); err != nil {
		slog.Error("清理主 bot 的遗留群配置失败", "bot_id", rec.BotID, "err", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("已清理主 bot 的遗留群配置", "bot_id", rec.BotID, "群数", n)
	}
	return sh.Cache.Reload()
}

// migrateLegacyChats 把早先那个全局的 antiad_chats 列表搬进 bot_chats。
//
// 只在「旧列表非空且新表为空」时跑一次，把这些群挂到最早接入的那个 bot
// 名下。不做这一步的话，从单租户版本升上来的部署会在重启后突然对所有群
// 失去防护，而面板上看不出任何异常。
func migrateLegacyChats(sh *core.Shared, reg *core.Registry) {
	snap := sh.Cache.Snap()
	legacy := snap.SettingInt64List("antiad_chats")
	if len(legacy) == 0 {
		return
	}
	for _, m := range snap.BotChats {
		if len(m) > 0 {
			return // 新表已经在用了，不碰
		}
	}
	bots := snap.BotsOwnedBy(0, true)
	if len(bots) == 0 {
		return
	}
	target := bots[0].BotID

	now := time.Now().Unix()
	for _, chatID := range legacy {
		if _, err := sh.Store.Write.Exec(`INSERT INTO bot_chats
			(bot_id,chat_id,title,enabled,dryrun,group_alert,created_at)
			VALUES (?,?,'',1,?,?,?) ON CONFLICT(bot_id,chat_id) DO NOTHING`,
			target, chatID,
			// 旧的全局开关是每群配置的合理初值。
			snap.SettingInt("antiad_dryrun", 1),
			snap.SettingInt("antiad_group_alert", 0), now); err != nil {
			slog.Error("迁移生效群失败", "chat", chatID, "err", err)
		}
	}
	// 清掉旧键，避免下次启动重复迁移（管理员在新面板上移除的群
	// 会因为旧列表还在而被搬回来）。
	if err := sh.PutSetting("antiad_chats", "[]"); err != nil {
		slog.Error("清理旧生效群列表失败", "err", err)
	}
	slog.Info("已把全局生效群迁移到 bot 名下",
		"bot_id", target, "群数", len(legacy))
}

// startWebhook 起 HTTP 服务，并为每个已接入的 bot 注册回调地址。
func startWebhook(cfg *config.Config, reg *core.Registry, sh *core.Shared) *http.Server {
	// 每个 bot 各自的回调地址都要注册一遍：它们的 token 不同，
	// 路径也就不同。失败不退出——反代可能还没接上，管理员稍后可以
	// 在面板上重试。
	go reg.Each(func(b *core.Bot) {
		if err := b.SetWebhook(cfg.WebhookURLFor(b.Token)); err != nil {
			slog.Error("注册 webhook 失败", "bot", config.MaskToken(b.Token), "err", err)
			return
		}
		slog.Info("webhook 已注册", "bot", config.MaskToken(b.Token))
	})

	srv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: reg,
		// 回调是小 JSON，握手后迟迟不发数据的连接没有留着的理由。
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		slog.Info("webhook 模式启动", "addr", cfg.ListenAddr,
			"public_url", cfg.PublicURL, "version", version,
			"tg_api", cfg.TGAPIBase, "已接入", reg.Size())
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP 服务退出", "err", err)
		}
	}()
	return srv
}

// startPolling 起长轮询。只有配置里那个 bot 能走这条路 ——
// 轮询是本进程主动发起的，而其余 bot 是别人把 webhook 指过来的。
func startPolling(cfg *config.Config, reg *core.Registry, stop <-chan struct{}) {
	b, ok := reg.LookupToken(cfg.BotToken)
	if !ok {
		slog.Error("配置里的 bot 未能建立实例，无法启动长轮询")
		os.Exit(1)
	}
	// 先撤 webhook：getUpdates 与 webhook 互斥，库里还留着上一次的
	// webhook 时 getUpdates 会一直拿 409，而表现为「一条消息都收不到」。
	b.DropWebhook()
	b.RegisterCommands()
	go b.RunPolling(stop)

	// 表里其余 bot 没有实例（loadAll 已经跳过并警告过），面板上也会
	// 标出「没有在运行」。这里不再重复。
	slog.Info("长轮询模式启动", "version", version, "tg_api", cfg.TGAPIBase)
}
