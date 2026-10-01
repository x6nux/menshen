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

// BackfillRow 是补全结果的一行（导出版，供测试与其他包使用）。
type BackfillRow = backfillRow

// SwapBackfillRunner 替换补全脚本的执行器，返回原实现。仅供测试。
func SwapBackfillRunner(fn func(*core.Bot, int64, string) ([]BackfillRow, error)) func(*core.Bot, int64, string) ([]BackfillRow, error) {
	old := backfillRunner
	if fn != nil {
		backfillRunner = fn
	}
	return old
}

// StartJoinBackfill 起一个补全任务，返回是否真的起了与不能起的原因。
//
// force 为真时绕过 24 小时冷却 —— 配置台上的「补全历史入群时间」是管理员
// 的明确动作，不该被自动触发的冷却挡住。任务跑在后台：万人大群要翻很多页，
// 结果由私聊通知给出。
func StartJoinBackfill(b *core.Bot, chatID int64, username string, force bool) (bool, string) {
	if b == nil || b.IsMainBot() || chatID == 0 {
		return false, "该群不可用"
	}
	if b.Cfg.TGAPIID == 0 || b.Cfg.TGAPIHash == "" {
		slog.Info("入群时间补全：回查已关闭（tg_api_id=0/空），跳过", "chat", chatID)
		return false, "入群时间回查已关闭（配置里 tg_api_id 为 0 或空）"
	}
	key := backfillKey(b.BotID(), chatID)
	if !force {
		if v, ok := joinBackfillDone.Load(key); ok {
			if t, ok := v.(time.Time); ok && time.Since(t) < joinBackfillCooldown {
				return false, ""
			}
		}
	}
	// 先同步拿锁再答应：全局同时只允许一个补全任务（同一个 bot 的 MTProto
	// 会话文件不能并发使用）。拿不到锁就如实回「有任务在跑」——以前是先进
	// 协程再 TryLock，抢不到就悄悄丢掉，而接口已经回了「已开始」。
	if !joinBackfillRunning.TryLock() {
		if !force {
			slog.Info("入群时间补全：已有任务在跑，跳过这一轮", "chat", chatID)
		}
		return false, "已有补全任务在跑，等它跑完再试"
	}
	joinBackfillDone.Store(key, time.Now())

	// 群名取一下：私有群（没有公开用户名）要靠会话缓存里出现过才能解析，
	// 有用户名时最稳。
	if username == "" {
		username = chatUsername(b, chatID)
	}

	go func() {
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
	return true, ""
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

// chatUsername 取群的公开用户名（私有群取不到，返回空串）。
func chatUsername(b *core.Bot, chatID int64) string {
	raw, err := b.TG.Call("getChat", map[string]any{"chat_id": chatID})
	if err != nil {
		return ""
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &resp) != nil || !resp.OK {
		return ""
	}
	return resp.Result.Username
}

// ---- 按需实时查询（单个人） ----
//
// 批量列表会漏人（大群服务端截断在一万条左右，小群也会漏受限账号），而
// 绝大部分成员一辈子也不会被用到。所以不再预先全查：/jtime、判定与复查
// 真正需要年龄轴时，实时查这一个人，查到就写进 group_members.joined_at
// —— 库就是缓存，查过一次之后都是秒回。

// joinLookupTimeout 是单人查询的等待上限。脚本要起 python、连 MTProto、
// 发一两次请求，正常几秒；超时按查不到处理，别把调用方拖太久。
const joinLookupTimeout = 20 * time.Second

const (
	// joinLookupMissTTL 是「确实查不到」（已退群/被踢）的负缓存时长。
	joinLookupMissTTL = 6 * time.Hour
	// joinLookupErrTTL 是查询本身失败（网络、FLOOD_WAIT、超时）的负缓存，
	// 短一些：过一会儿值得再试。
	joinLookupErrTTL = 15 * time.Minute
)

// joinLookupMissRec 是负缓存的一条。
type joinLookupMissRec struct {
	at  time.Time
	ttl time.Duration
}

var (
	// joinLookupMu 让单人查询排队：同一个 bot 的 MTProto 会话文件不能并发。
	joinLookupMu sync.Mutex
	// joinLookupMiss 是查不到的人的负缓存，避免反复起脚本。
	joinLookupMiss sync.Map // key -> joinLookupMissRec
	// joinLookupRunner 是单人查询的实现，测试里替换。
	joinLookupRunner = runJoinLookupScript
)

// ResolveJoinTime 实时查一个人在某群的入群时间，查到就写回 group_members。
//
// 返回 (时间戳, 是否拿到)。入群时间在库里就是缓存：joined_at>0 直接返回；
// 查不到的进负缓存，短时间内不再重试。批量补全正在跑时直接放弃 ——
// 会话文件在用，而且判定路径不能为此等上十分钟。
func ResolveJoinTime(b *core.Bot, chatID, uid int64) (int64, bool) {
	if b == nil || b.IsMainBot() || chatID == 0 || uid <= 0 {
		return 0, false
	}
	if gm, ok := loadMember(b.Store, chatID, uid); ok && gm.JoinedAt > 0 {
		return gm.JoinedAt, true
	}
	if b.Cfg.TGAPIID == 0 || b.Cfg.TGAPIHash == "" {
		return 0, false
	}
	key := backfillKey(b.BotID(), chatID) + ":" + strconv.FormatInt(uid, 10)
	missed := func() bool {
		if v, ok := joinLookupMiss.Load(key); ok {
			if r, ok := v.(joinLookupMissRec); ok && time.Since(r.at) < r.ttl {
				return true
			}
		}
		return false
	}
	if missed() {
		return 0, false
	}

	joinLookupMu.Lock()
	defer joinLookupMu.Unlock()
	// 等锁期间别人可能已经查到了、或者刚失败过。
	if gm, ok := loadMember(b.Store, chatID, uid); ok && gm.JoinedAt > 0 {
		return gm.JoinedAt, true
	}
	if missed() {
		return 0, false
	}
	if !joinBackfillRunning.TryLock() {
		return 0, false
	}
	defer joinBackfillRunning.Unlock()

	ts, err := joinLookupRunner(b, chatID, uid)
	if err != nil {
		slog.Info("入群时间实时查询：失败", "chat", chatID, "uid", uid, "err", err)
		joinLookupMiss.Store(key, joinLookupMissRec{at: time.Now(), ttl: joinLookupErrTTL})
		return 0, false
	}
	if ts <= 0 {
		// 脚本正常跑完但没数据：已退群/被踢，短时间内不用再查。
		joinLookupMiss.Store(key, joinLookupMissRec{at: time.Now(), ttl: joinLookupMissTTL})
		return 0, false
	}
	if _, err := b.Store.Write.Exec(`UPDATE group_members SET joined_at=?
		WHERE chat_id=? AND user_id=? AND joined_at=0`, ts, chatID, uid); err != nil {
		slog.Warn("入群时间实时查询：写库失败", "chat", chatID, "uid", uid, "err", err)
	}
	return ts, true
}

// ensureJoinAge 在画像还没拿到入群时间时补一次实时查询，失败就维持未知。
func ensureJoinAge(b *core.Bot, chatID int64, p *senderProfile) {
	if p == nil || p.AgeKnown || p.UserID <= 0 || p.IsChannel {
		return
	}
	ts, ok := ResolveJoinTime(b, chatID, p.UserID)
	if !ok {
		return
	}
	p.AgeKnown = true
	if now := time.Now().Unix(); now > ts {
		p.AgeHours = (now - ts) / 3600
	}
}

// runJoinLookupScript 起一次单人查询，返回入群时间（0 表示查不到）。
func runJoinLookupScript(b *core.Bot, chatID, uid int64) (int64, error) {
	script, err := ensureJoinBackfillScript(b)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), joinLookupTimeout)
	defer cancel()

	args := []string{script, "--chat", strconv.FormatInt(chatID, 10),
		"--lookup", strconv.FormatInt(uid, 10)}
	if username := chatUsername(b, chatID); username != "" {
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
	out, err := cmd.Output()
	if err != nil {
		return 0, errWithStderr(err, stderr.String())
	}
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Split(strings.TrimSpace(line), "\t")
		if len(parts) != 3 {
			continue
		}
		if ts, e := strconv.ParseInt(parts[2], 10, 64); e == nil && ts > 0 {
			return ts, nil
		}
	}
	return 0, nil
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
