package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	// 时区名解析（tz_name）要查 IANA 数据库；静态编译（CGO_ENABLED=0）
	// 的二进制不保证宿主机装了 tzdata，把数据库嵌进二进制里即可不依赖它。
	_ "time/tzdata"

	"menshen/internal/antiad"
	"menshen/internal/config"
	"menshen/internal/core"
	"menshen/internal/logbuf"
	"menshen/internal/panel"
	"menshen/internal/store"
)

var (
	version   = "2.4.2"
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

	// 内置必封规则在构建缓存之前补种，否则这一轮判定拿不到它。
	if err := store.SeedBuiltinRules(db); err != nil {
		slog.Error("补种内置规则失败", "err", err)
		os.Exit(1)
	}

	cache, err := store.NewCache(db)
	if err != nil {
		slog.Error("构建缓存失败", "err", err)
		os.Exit(1)
	}

	sh := core.NewShared(cfg, db, cache)

	// 日志三路输出：标准输出、日志文件与进程内环形缓冲。上限从设置现读，
	// 面板上改小立刻生效；文件写不进去只丢这一行并退避重试，绝不打断业务。
	fileSink := installLogging(sh.Logs, cfg.DBPath, func() (int64, int64) {
		snap := cache.Snap()
		return snap.SettingInt("log_file_max_mb", 10) << 20,
			snap.SettingInt("log_file_total_mb", 50) << 20
	})
	defer fileSink.Close()

	stop := make(chan struct{})
	reg := core.NewRegistry(sh, stop, dispatch)

	// 配置里那个 bot 自动注册给第一个主管理员，无需先在面板里手动添加。
	// 其余 bot 由管理员在面板上接入。
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
	bindLegacyModels(sh)

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

// installLogging 把 slog 默认 logger 接到三路输出上：标准输出、与数据库
// 同目录的滚动日志文件（data.db → data.log，备份 .1/.2/…）与进程内环形
// 缓冲（网页版运行日志页读它）。控制台与文件保持 INFO，缓冲从 DEBUG
// 起采 —— 网页上能看到两边都没有的调试上下文。装完之后启动流程的日志
// （迁移、注册 webhook、后台任务）三处都有。
//
// 单文件与总大小上限经 limits 每次写入时现读（main 传的是全局设置
// log_file_max_mb / log_file_total_mb，MB）：面板上改小立刻生效并清理
// 旧备份，任一为 0 关闭文件日志。返回文件写入器，退出时 Close。
func installLogging(buf *logbuf.Buffer, dbPath string,
	limits func() (maxFileBytes, maxTotalBytes int64)) *logbuf.RotatingFile {
	fileSink := logbuf.NewRotatingFile(logbuf.LogPathFor(dbPath), limits)
	slog.SetDefault(slog.New(logbuf.NewHandler(buf,
		slog.NewTextHandler(io.MultiWriter(os.Stderr, fileSink),
			&slog.HandlerOptions{Level: slog.LevelInfo}),
		slog.LevelDebug)))
	return fileSink
}

// ensureMainBot 把配置里的 bot_token 登记进 bots 表并标记为主 bot。
//
// 主 bot 只做配置管理与接入其他 bot：不入群、不判定广告，被拉进群自动
// 退出。标记落库（bots.is_main）而不是每次比对 cfg.BotToken —— webhook
// 模式下该字段可为空，配置一改就会漂移。
//
// 顺带做两件纠偏：
//   - 除配置里的 bot 外，其余 bot 的主 bot 标记一律摘掉
//   - 删掉主 bot 名下的群配置：主 bot 不入群，留着会让面板显示
//     与行为对不上
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

// migrateLegacyChats 把全局 antiad_chats 列表搬进 bot_chats。
//
// 仅在旧列表非空且新表为空时执行一次，把这些群挂到最早接入的那个 bot
// 名下，避免升级后群配置丢失。
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

// bindLegacyModels 把旧格式（无上游前缀）模型绑到唯一的上游名下。
//
// 模型名带前缀才能区分多上游。只有一个启用的上游时绑定是无歧义的，
// 启动时直接完成；有多个上游时不动数据，交给面板上的提示。
func bindLegacyModels(sh *core.Shared) {
	only := ""
	for _, u := range sh.Cache.Snap().Upstreams {
		if u.Status != 1 {
			continue
		}
		if only != "" {
			return // 多于一个上游，无法推断该绑谁
		}
		only = u.Name
	}
	if only == "" {
		return
	}
	n, err := sh.BindLegacyModels(only)
	if err != nil {
		slog.Error("绑定旧格式模型失败", "上游", only, "err", err)
		return
	}
	if n > 0 {
		slog.Info("已把旧格式模型绑定到唯一上游", "上游", only, "模型数", n)
	}
}

// startWebhook 起 HTTP 服务，并为每个已接入的 bot 注册回调地址。
func startWebhook(cfg *config.Config, reg *core.Registry, sh *core.Shared) *http.Server {
	// 签名密钥要在开始服务之前就位：并发下两次生成会互相覆盖，
	// 先签出去的链接随之失效。
	if err := antiad.EnsureWebSecret(sh); err != nil {
		slog.Error("生成网页签名密钥失败，申诉网页将不可用", "err", err)
	}

	// 每个 bot 各自的回调地址都要注册一遍：它们的 token 不同，
	// 路径也就不同。失败不退出——反代可能还没接上，管理员稍后可以
	// 在面板上重试。FinishSetup 顺带重注册命令菜单与 Mini App 按钮。
	go reg.Each(func(b *core.Bot) {
		if err := b.FinishSetup(); err != nil {
			slog.Error("注册 webhook 失败", "bot", config.MaskToken(b.Token), "err", err)
			return
		}
		slog.Info("webhook 已注册", "bot", config.MaskToken(b.Token))
	})

	srv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: webRouter{reg: reg, web: antiad.WebHandler(sh), mini: panel.MiniAppHandler(sh), public: panel.PublicShellHandler(sh), admin: panel.AdminPanelHandler(sh), cap: antiad.CapHandler(sh)},
		// 回调是小 JSON，握手后迟迟不发数据的连接没有留着的理由。
		// 只设 ReadHeaderTimeout 挡不住慢速发 body 的连接：它会一直占着
		// goroutine 与内存（暴露到回环之外时就是廉价的 slowloris）。
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		slog.Info("webhook 模式启动", "addr", cfg.ListenAddr,
			"public_url", cfg.PublicURL, "version", version,
			"tg_api", cfg.TGAPIBase, "已接入", reg.Size())
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			// webhook 模式没有别的输入通道：监听失败就是全停，必须退出让
			// 运维（或容器编排）看见，而不是留着一个收不到更新的进程。
			slog.Error("HTTP 服务退出，进程退出", "err", err)
			os.Exit(1)
		}
	}()
	return srv
}

// webRouter 组装 HTTP 处理器：路径中任一段为 "_w" 的请求进网页处理器，
// 其余交给 webhook。识别方式与 core.TokenFromPath 一样不依赖前缀，
// 反代再套几层子路径都认得出来。
type webRouter struct {
	reg    *core.Registry
	web    http.Handler
	mini   http.Handler
	public http.Handler
	admin  http.Handler
	cap    http.Handler
}

func (h webRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if hasWebSegment(r.URL.Path) {
		// 页面壳（GET/HEAD、无 json=1）先交给公开网页 SPA；数据与提交
		// 仍走 antiad.WebHandler。壳不含数据，签名校验在接口里。
		if h.public != nil &&
			(r.Method == http.MethodGet || r.Method == http.MethodHead) &&
			r.URL.Query().Get("json") != "1" &&
			antiad.IsWebPagePath(r.URL.Path) {
			h.public.ServeHTTP(w, r)
			return
		}
		h.web.ServeHTTP(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin") && h.admin != nil {
		h.admin.ServeHTTP(w, r)
		return
	}
	// 内置 Cap 服务的入口：Cap 无需外部实例，challenge/redeem 由本进程处理。
	if strings.HasPrefix(r.URL.Path, "/cap/") && h.cap != nil {
		h.cap.ServeHTTP(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/miniapp") {
		h.mini.ServeHTTP(w, r)
		return
	}
	h.reg.ServeHTTP(w, r)
}

// hasWebSegment 报告路径中是否有 "_w" 段。
func hasWebSegment(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == "_w" {
			return true
		}
	}
	return false
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
	// webhook 时 getUpdates 会一直拿 409，表现为一条消息都收不到。
	b.DropWebhook()
	b.RegisterCommands()
	go b.RunPolling(stop)

	// 表里其余 bot 没有实例（loadAll 已经跳过并警告过），面板上也会
	// 标出没有在运行。这里不再重复。
	slog.Info("长轮询模式启动", "version", version, "tg_api", cfg.TGAPIBase)
}
