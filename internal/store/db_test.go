package store

import (
	"database/sql"
	"path/filepath"
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
