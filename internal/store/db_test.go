package store

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMigrateOldDB 在一个「老形状」的库上启动：schemaSQL 全是
// CREATE TABLE IF NOT EXISTS，对已存在的表完全无效，新列只能靠 migrate 补。
// 只在新库上验证过的迁移，恰好会在唯一有数据的那些部署上炸。
func TestMigrateOldDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE bot_chats (bot_id INTEGER NOT NULL, chat_id INTEGER NOT NULL,
			title TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1,
			dryrun INTEGER NOT NULL DEFAULT 1, group_alert INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (bot_id, chat_id))`,
		`INSERT INTO bot_chats (bot_id,chat_id) VALUES (1,-100)`,
		`CREATE TABLE group_members (chat_id INTEGER NOT NULL, user_id INTEGER NOT NULL,
			joined_at INTEGER NOT NULL DEFAULT 0, first_seen INTEGER NOT NULL,
			msg_count INTEGER NOT NULL DEFAULT 0, last_msg_at INTEGER NOT NULL DEFAULT 0,
			ad_hits INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (chat_id, user_id))`,
		`CREATE TABLE group_messages (chat_id INTEGER NOT NULL, message_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL, text TEXT NOT NULL, at INTEGER NOT NULL,
			PRIMARY KEY (chat_id, message_id))`,
		`CREATE TABLE bots (token TEXT PRIMARY KEY, bot_id INTEGER NOT NULL,
			username TEXT NOT NULL DEFAULT '', owner_id INTEGER NOT NULL,
			so_model TEXT NOT NULL DEFAULT '', llm_model TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL)`,
		`INSERT INTO bots (token,bot_id,owner_id,created_at) VALUES ('1:old',1,777,0)`,
		// 老形状的 antiad_log：没有 bot_id（migrate 才补），因此 bot_id 上的
		// 索引也只能在 migrate 之后建——这正是 ensureIndexes 存在的理由。
		`CREATE TABLE antiad_log (id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER NOT NULL, user_id INTEGER NOT NULL,
			message_id INTEGER NOT NULL, text TEXT NOT NULL, verdict TEXT NOT NULL,
			confidence REAL NOT NULL, decider TEXT NOT NULL,
			ad_kind TEXT NOT NULL DEFAULT '', action TEXT NOT NULL,
			reason TEXT NOT NULL, prompt_tokens INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0,
			quota_cost INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL)`,
		`CREATE INDEX idx_antiad_time ON antiad_log(created_at)`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatalf("造老库失败: %v", err)
		}
	}
	old.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("老库启动失败: %v", err)
	}
	defer s.Close()
	for _, c := range [][2]string{{"bot_chats", "punish"},
		{"group_members", "whitelisted"}, {"group_messages", "media_group"},
		{"bots", "is_main"}} {
		if has, err := hasColumn(s.Write, c[0], c[1]); err != nil || !has {
			t.Errorf("%s.%s 没有补上（err=%v）", c[0], c[1], err)
		}
	}
	// 索引必须建在 migrate 补出来的 bot_id 上，且不能因为老库没有这一列
	// 而让启动失败（Open 已经返回成功，这里再确认索引真的在）。
	if has, err := hasIndex(s.Write, "antiad_log", "idx_antiad_bot_id"); err != nil || !has {
		t.Errorf("antiad_log(bot_id,id) 索引没建上（err=%v）", err)
	}
	c, err := NewCache(s)
	if err != nil {
		t.Fatalf("老库加载快照失败: %v", err)
	}
	if bc, ok := c.Snap().ChatConf(1, -100); !ok || bc.Punish != -1 {
		t.Errorf("老群的处罚方式应默认跟随 bot（-1），得到 %+v", bc)
	}
	// 老库没有主 bot 标记：升级后由 ensureMainBot 置位，迁移本身只补列。
	if rec := c.Snap().Bots[1]; rec == nil || rec.IsMain {
		t.Errorf("老库的 bot 不该被迁移直接标成主 bot，得到 %+v", rec)
	}
}

// hasIndex 报告表上是否存在某个索引。
func hasIndex(db *sql.DB, table, name string) (bool, error) {
	rows, err := db.Query("PRAGMA index_list(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var seq, unique, partial int
		var idx, origin string
		if err := rows.Scan(&seq, &idx, &unique, &origin, &partial); err != nil {
			return false, err
		}
		if idx == name {
			return true, nil
		}
	}
	return false, rows.Err()
}

// TestReloadWithConcurrentWrites：写连接与 Reload 的读事务并发时不互锁。
// WAL 下读者不阻塞写者、写者也不阻塞读者；Reload 现在整轮跑在一个读事务里，
// 这里守的是「加事务之后仍然不会互相饿死」。
func TestReloadWithConcurrentWrites(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "rw.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := NewCache(s)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			key := "k" + strconv.Itoa(i%8)
			if _, err := s.Write.Exec(`INSERT INTO settings (k,v) VALUES (?,?)
				ON CONFLICT(k) DO UPDATE SET v=excluded.v`, key, "v"); err != nil {
				t.Errorf("并发写失败: %v", err)
				return
			}
		}
	}()
	for i := 0; i < 30; i++ {
		if err := c.Reload(); err != nil {
			t.Fatalf("并发 Reload 失败: %v", err)
		}
	}
	close(stop)
	wg.Wait()
}

// TestOpenPragmas 确认每条连接都带上了 WAL 与 synchronous=NORMAL。
// pragma 是按连接生效的：只在某一条连接上 Exec 一次，连接池新开的
// 连接就悄悄回到默认的 FULL，每次写都要多等一次 fsync。
func TestOpenPragmas(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for name, db := range map[string]*sql.DB{"write": s.Write, "read": s.Read} {
		var mode string
		var sync int
		db.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
		db.QueryRow(`PRAGMA synchronous`).Scan(&sync)
		if mode != "wal" || sync != 1 {
			t.Errorf("%s 连接 journal_mode=%s synchronous=%d，期望 wal / 1（NORMAL）",
				name, mode, sync)
		}
	}
}
