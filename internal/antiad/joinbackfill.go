package antiad

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"menshen/internal/core"
)

// ---- 历史成员入群时间补全 ----
//
// Bot API 没有「入群时间」字段：getChatMember 只给 status/权限/until_date。
// 只有 MTProto 的 channels.getParticipants 带 ChannelParticipant.date（官方
// 定义就是 Date joined）。好在这一步不需要用户账号 —— 用 bot 自己的 token
// 登录 MTProto（auth.importBotAuthorization）就行，所以仍是纯 bot。
//
// 触发点：bot 在某个群里**刚拿到管理员权限**时补一次。此后新入群由
// chat_member 更新实时记录（那是 Bot API 有的），补全只负责「入群之前就在
// 群里、入群时间未知」的那批历史成员 —— 年龄轴（新人/老人分档）靠它。
//
// 需要宿主上有 python3 + telethon，以及配置里的 tg_api_id / tg_api_hash
// （my.telegram.org 申请）。缺任何一样都只记一条日志、不影响判定。

//go:embed scripts/tgjoin_backfill.py
var joinBackfillScript string

// joinBackfillTimeout 是一次补全的整体超时。万人大群要翻很多页，给足；
// 但子进程不能无限期挂着。
const joinBackfillTimeout = 10 * time.Minute

// joinBackfillCooldown 是同一个群两次补全之间的间隔：管理员权限变更会推
// 好几次 my_chat_member（改权限、置顶…），不该每次都跑一遍。
const joinBackfillCooldown = 24 * time.Hour

var (
	// joinBackfillDone 记录 (bot, chat) 上次补全的时间。
	joinBackfillDone sync.Map // "bot:chat" -> time.Time
	// joinBackfillRunning 是全局单飞：同一个进程同时只跑一个补全。
	joinBackfillRunning sync.Mutex
	// joinBackfillScriptOnce 保证嵌入脚本只落盘一次。
	joinBackfillScriptOnce sync.Once
	joinBackfillScriptPath string
	joinBackfillScriptErr  error
)

// backfillRow 是补全脚本输出的一行。
type backfillRow struct {
	ChatID int64
	UserID int64
	Joined int64
}

// backfillRunner 是「跑一次补全脚本」的实现，测试里替换掉。
var backfillRunner = runJoinBackfillScript

// maybeJoinBackfill 在 bot 刚拿到管理员权限时补全该群的历史成员入群时间。
// 只做安全检查并起一个后台任务，不阻塞更新路径。
func maybeJoinBackfill(b *core.Bot, chatID int64, username string) {
	if b == nil || b.IsMainBot() || chatID == 0 {
		return
	}
	if b.Cfg.TGAPIID == 0 || b.Cfg.TGAPIHash == "" {
		slog.Info("入群时间补全：未配置 tg_api_id/tg_api_hash，跳过",
			"chat", chatID, "说明", "到 my.telegram.org 申请后填进配置文件")
		return
	}
	key := backfillKey(b.BotID(), chatID)
	if v, ok := joinBackfillDone.Load(key); ok {
		if t, ok := v.(time.Time); ok && time.Since(t) < joinBackfillCooldown {
			return
		}
	}
	joinBackfillDone.Store(key, time.Now())

	// 群名取一下：私有群（没有公开用户名）要靠会话缓存里出现过才能解析，
	// 有用户名时最稳。
	if username == "" {
		if raw, err := b.TG.Call("getChat", map[string]any{"chat_id": chatID}); err == nil {
			var resp struct {
				OK     bool `json:"ok"`
				Result struct {
					Username string `json:"username"`
				} `json:"result"`
			}
			if json.Unmarshal(raw, &resp) == nil && resp.OK {
				username = resp.Result.Username
			}
		}
	}

	go func() {
		if !joinBackfillRunning.TryLock() {
			slog.Info("入群时间补全：已有任务在跑，跳过这一轮", "chat", chatID)
			return
		}
		defer joinBackfillRunning.Unlock()

		rows, err := backfillRunner(b, chatID, username)
		if err != nil {
			slog.Warn("入群时间补全失败", "chat", chatID, "err", err)
			return
		}
		filled := applyJoinBackfill(b, chatID, rows)
		slog.Info("入群时间补全完成", "chat", chatID, "拿到", len(rows), "补上", filled)
		if filled == 0 {
			return
		}
		for _, admin := range b.AlertTargets() {
			b.Send(admin, "🗂 <b>历史成员入群时间已补全</b>\n\n群 <code>"+
				strconv.FormatInt(chatID, 10)+"</code>：补上 <b>"+
				strconv.Itoa(filled)+"</b> 条。\n<i>年龄轴（新人/老人分档）对这些人现在生效了。</i>",
				nil)
		}
	}()
}

func backfillKey(botID, chatID int64) string {
	return strconv.FormatInt(botID, 10) + ":" + strconv.FormatInt(chatID, 10)
}

// applyJoinBackfill 把补全结果写库：**只填 joined_at=0 的行**，已有值不动
// （实时机制记的比脚本新）。返回实际填了多少行。
func applyJoinBackfill(b *core.Bot, chatID int64, rows []backfillRow) int {
	filled := 0
	for i, r := range rows {
		if r.ChatID != 0 && r.ChatID != chatID {
			continue
		}
		res, err := b.Store.Write.Exec(`UPDATE group_members SET joined_at=?
			WHERE chat_id=? AND user_id=? AND joined_at=0`, r.Joined, chatID, r.UserID)
		if err != nil {
			slog.Warn("入群时间补全：写入失败", "uid", r.UserID, "err", err)
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			filled++
		}
		// 分批让出写连接：一次几千行的大事务会把每条群消息的画像/留底写卡住。
		if i%200 == 199 {
			time.Sleep(20 * time.Millisecond)
		}
	}
	return filled
}

// runJoinBackfillScript 把嵌入的脚本落到数据目录下再执行，读回 TSV。
func runJoinBackfillScript(b *core.Bot, chatID int64, username string) ([]backfillRow, error) {
	script, err := ensureJoinBackfillScript(b)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), joinBackfillTimeout)
	defer cancel()

	args := []string{script, "--chat", strconv.FormatInt(chatID, 10)}
	if username != "" {
		args = append(args, "--username", username)
	}
	cmd := exec.CommandContext(ctx, "python3", args...)
	cmd.Env = append(os.Environ(),
		"TG_BOT_TOKEN="+b.Token,
		"TG_API_ID="+strconv.Itoa(b.Cfg.TGAPIID),
		"TG_API_HASH="+b.Cfg.TGAPIHash,
		"TG_SESSION_DIR="+sessionsDir(b),
		"PYTHONUNBUFFERED=1",
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var rows []backfillRow
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		parts := strings.Split(strings.TrimSpace(sc.Text()), "\t")
		if len(parts) != 3 {
			continue
		}
		chat, err1 := strconv.ParseInt(parts[0], 10, 64)
		uid, err2 := strconv.ParseInt(parts[1], 10, 64)
		ts, err3 := strconv.ParseInt(parts[2], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || uid == 0 || ts <= 0 {
			continue
		}
		rows = append(rows, backfillRow{ChatID: chat, UserID: uid, Joined: ts})
	}
	if err := cmd.Wait(); err != nil {
		return rows, errWithStderr(err, stderr.String())
	}
	return rows, nil
}

// errWithStderr 把脚本的 stderr 末尾附进错误里：python/telethon 的报错只有
// 这里看得到。
func errWithStderr(err error, stderr string) error {
	s := strings.TrimSpace(stderr)
	if s == "" {
		return err
	}
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	return fmt.Errorf("%w：%s", err, s)
}

// ensureJoinBackfillScript 把嵌入脚本写到数据目录（只写一次），返回路径。
func ensureJoinBackfillScript(b *core.Bot) (string, error) {
	joinBackfillScriptOnce.Do(func() {
		dir := filepath.Join(filepath.Dir(b.Cfg.DBPath), "join_backfill")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			joinBackfillScriptErr = err
			return
		}
		p := filepath.Join(dir, "tgjoin_backfill.py")
		if err := os.WriteFile(p, []byte(joinBackfillScript), 0o700); err != nil {
			joinBackfillScriptErr = err
			return
		}
		joinBackfillScriptPath = p
	})
	return joinBackfillScriptPath, joinBackfillScriptErr
}

// sessionsDir 是 MTProto 会话文件的目录（与数据同处，重启后复用）。
func sessionsDir(b *core.Bot) string {
	dir := filepath.Join(filepath.Dir(b.Cfg.DBPath), "join_backfill", "sessions")
	_ = os.MkdirAll(dir, 0o700)
	return dir
}
