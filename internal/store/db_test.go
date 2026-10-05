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
		`INSERT INTO group_members (chat_id,user_id,first_seen) VALUES (-100,555,1)`,
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
		// 老形状的 upstreams：没有 kind（migrate 才补），老行要按 openai 处理。
		`CREATE TABLE upstreams (id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL, base_url TEXT NOT NULL, api_key TEXT NOT NULL,
			weight INTEGER NOT NULL DEFAULT 1, status INTEGER NOT NULL DEFAULT 1,
			supports_chat INTEGER NOT NULL DEFAULT 1,
			supports_systemone INTEGER NOT NULL DEFAULT 0)`,
		`INSERT INTO upstreams (name,base_url,api_key) VALUES ('old','http://x','k')`,
		// 老形状的 ad_rules：没有 last_ads_total / last_kinds（migrate 才补）。
		`CREATE TABLE ad_rules (id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL, pattern TEXT NOT NULL,
			category TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT 'ai', enabled INTEGER NOT NULL DEFAULT 0,
			enforce INTEGER NOT NULL DEFAULT 0, hits INTEGER NOT NULL DEFAULT 0,
			last_matched INTEGER NOT NULL DEFAULT 0, last_tp INTEGER NOT NULL DEFAULT 0,
			last_fp INTEGER NOT NULL DEFAULT 0, last_undone INTEGER NOT NULL DEFAULT 0,
			last_scanned INTEGER NOT NULL DEFAULT 0,
			last_tested_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT 0,
			created_by INTEGER NOT NULL DEFAULT 0)`,
		`INSERT INTO ad_rules (name,pattern) VALUES ('old','old')`,
		// 老形状的 join_mutes：没有 kind（migrate 才补），老行要按 profile 处理。
		`CREATE TABLE join_mutes (chat_id INTEGER NOT NULL, user_id INTEGER NOT NULL,
			bot_id INTEGER NOT NULL, reason TEXT NOT NULL DEFAULT '',
			notice_msg INTEGER NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL, PRIMARY KEY (chat_id, user_id))`,
		`INSERT INTO join_mutes (chat_id,user_id,bot_id,created_at) VALUES (-100,555,1,0)`,
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
		{"bots", "is_main"}, {"upstreams", "kind"},
		{"ad_rules", "last_ads_total"}, {"ad_rules", "last_kinds"},
		{"join_mutes", "kind"}, {"group_members", "prewarm_checked_at"},
		{"group_members", "profile_hash"}} {
		if has, err := hasColumn(s.Write, c[0], c[1]); err != nil || !has {
			t.Errorf("%s.%s 没有补上（err=%v）", c[0], c[1], err)
		}
	}
	// 老规则的新列默认值：0 / 空串。
	var adsTotal int64
	var kinds string
	if err := s.Read.QueryRow(`SELECT last_ads_total,last_kinds FROM ad_rules
		WHERE name='old'`).Scan(&adsTotal, &kinds); err != nil {
		t.Fatalf("读老规则新列失败: %v", err)
	}
	if adsTotal != 0 || kinds != "" {
		t.Errorf("老规则新列默认应为 0/空串，得到 %d/%q", adsTotal, kinds)
	}
	// 老成员的资料指纹默认空串（= 还没查过），不能是 NULL 或别的形状。
	var phash string
	if err := s.Read.QueryRow(`SELECT profile_hash FROM group_members
		WHERE chat_id=-100 AND user_id=555`).Scan(&phash); err != nil {
		t.Fatalf("读老成员 profile_hash 失败: %v", err)
	}
	if phash != "" {
		t.Errorf("老成员 profile_hash 默认应为空串，得到 %q", phash)
	}
	// 老上游没有类型：迁移默认 openai，行为与升级前一致。
	var kind string
	if err := s.Read.QueryRow(`SELECT kind FROM upstreams WHERE name='old'`).Scan(&kind); err != nil {
		t.Fatalf("读老上游 kind 失败: %v", err)
	}
	if kind != "openai" {
		t.Errorf("老上游的 kind 应为 openai，得到 %q", kind)
	}
	// 老 join_mutes 行迁移后一律按资料类（profile）处理，申诉口径不变。
	var jmKind string
	if err := s.Read.QueryRow(`SELECT kind FROM join_mutes
		WHERE chat_id=-100 AND user_id=555`).Scan(&jmKind); err != nil {
		t.Fatalf("读老 join_mutes.kind 失败: %v", err)
	}
	if jmKind != "profile" {
		t.Errorf("老 join_mutes.kind 应为 profile，得到 %q", jmKind)
	}
	// 索引必须建在 migrate 补出来的 bot_id 上，且不能因为老库没有这一列
	// 而让启动失败（Open 已经返回成功，这里再确认索引真的在）。
	if has, err := hasIndex(s.Write, "antiad_log", "idx_antiad_bot_id"); err != nil || !has {
		t.Errorf("antiad_log(bot_id,id) 索引没建上（err=%v）", err)
	}
	// 前置号复查的两个查询各自依赖的索引：老库的 group_members 连
	// prewarm_checked_at 都是 migrate 才补的，索引必须建在 migrate 之后。
	for _, name := range []string{"idx_gmember_pwcheck", "idx_gmember_joined"} {
		if has, err := hasIndex(s.Write, "group_members", name); err != nil || !has {
			t.Errorf("group_members 索引 %s 没建上（err=%v）", name, err)
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

// TestAppealOpenUniqueIndexDedupes：老库上同一 (bot,user) 可能有多张未结单
// （重复投递的回调并发建出的孤儿单）。启动时要先收拢（每个组合保留最新一张）
// 再建部分唯一索引——否则索引建不上、服务起不来；而孤儿单会永久占住
// openAppeal 的入口。
func TestAppealOpenUniqueIndexDedupes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dup.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE appeals (id INTEGER PRIMARY KEY AUTOINCREMENT,
			bot_id INTEGER NOT NULL, user_id INTEGER NOT NULL, status TEXT NOT NULL,
			statement TEXT NOT NULL DEFAULT '', ai_result TEXT NOT NULL DEFAULT '',
			ai_conf REAL NOT NULL DEFAULT 0, ai_reason TEXT NOT NULL DEFAULT '',
			ai_model TEXT NOT NULL DEFAULT '', ai_cost INTEGER NOT NULL DEFAULT 0,
			web_attempts INTEGER NOT NULL DEFAULT 0, web_since INTEGER NOT NULL DEFAULT 0,
			code TEXT NOT NULL DEFAULT '', code_expires INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`INSERT INTO appeals (bot_id,user_id,status,created_at,updated_at)
			VALUES (1,555,'web',1,1)`,
		`INSERT INTO appeals (bot_id,user_id,status,created_at,updated_at)
			VALUES (1,555,'noweb',2,2)`,
		`INSERT INTO appeals (bot_id,user_id,status,created_at,updated_at)
			VALUES (1,555,'statement',3,3)`,
		`INSERT INTO appeals (bot_id,user_id,status,created_at,updated_at)
			VALUES (1,556,'web',4,4)`,
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

	var open int
	if err := s.Read.QueryRow(`SELECT COUNT(*) FROM appeals
		WHERE bot_id=1 AND user_id=555 AND status IN (` +
		AppealOpenStatusesSQL + `)`).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 1 {
		t.Errorf("重复的未结单应收拢到 1 张，得到 %d", open)
	}
	var newest int64
	s.Read.QueryRow(`SELECT MAX(id) FROM appeals WHERE bot_id=1 AND user_id=555`).Scan(&newest)
	var st string
	s.Read.QueryRow(`SELECT status FROM appeals WHERE id=?`, newest).Scan(&st)
	if st != "statement" {
		t.Errorf("应保留最新一张未结单，得到 %q", st)
	}

	// 索引真的在：同一组合再建一张未结单会撞唯一约束。
	if _, err := s.Write.Exec(`INSERT INTO appeals
		(bot_id,user_id,status,created_at,updated_at) VALUES (1,555,'web',0,0)`); err == nil {
		t.Error("第二张未结单应撞未结唯一索引")
	}
	// 已结案的单不受限制。
	if _, err := s.Write.Exec(`INSERT INTO appeals
		(bot_id,user_id,status,created_at,updated_at) VALUES (1,555,'lifted',0,0)`); err != nil {
		t.Errorf("已结案的单不该被索引挡住: %v", err)
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

// TestMigrateMuteUnit：老库把禁言时长按**小时**存（antiad_mute_hours），启动
// 时要换算成**分钟**（antiad_mute_minutes ×60）；0（永久禁言）换算后仍是 0，
// 脏值不动（回落默认），旧键一律删掉——迁移只生效一次。
func TestMigrateMuteUnit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unit.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE settings (k TEXT PRIMARY KEY, v TEXT NOT NULL)`,
		`INSERT INTO settings (k,v) VALUES ('antiad_mute_hours','24')`,
		`CREATE TABLE bot_settings (bot_id INTEGER NOT NULL, k TEXT NOT NULL,
			v TEXT NOT NULL, PRIMARY KEY (bot_id,k))`,
		`INSERT INTO bot_settings (bot_id,k,v) VALUES (42,'antiad_mute_hours','0')`,
		`INSERT INTO bot_settings (bot_id,k,v) VALUES (43,'antiad_mute_hours','abc')`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatalf("造老库失败: %v", err)
		}
	}
	old.Close()

	for round := 0; round < 2; round++ { // 第二次打开不该重复换算
		s, err := Open(path)
		if err != nil {
			t.Fatalf("第 %d 次启动失败: %v", round+1, err)
		}
		var v string
		if err := s.Read.QueryRow(`SELECT v FROM settings
			WHERE k='antiad_mute_minutes'`).Scan(&v); err != nil || v != "1440" {
			t.Fatalf("24 小时应换算成 1440 分钟，得到 %q（err=%v）", v, err)
		}
		if err := s.Read.QueryRow(`SELECT v FROM bot_settings
			WHERE bot_id=42 AND k='antiad_mute_minutes'`).Scan(&v); err != nil || v != "0" {
			t.Errorf("永久禁言（0）换算后应仍是 0，得到 %q（err=%v）", v, err)
		}
		var n int
		s.Read.QueryRow(`SELECT COUNT(*) FROM bot_settings WHERE bot_id=43`).Scan(&n)
		if n != 0 {
			t.Errorf("非数字的脏值不写新键，得到 %d 行", n)
		}
		s.Read.QueryRow(`SELECT COUNT(*) FROM settings WHERE k='antiad_mute_hours'`).Scan(&n)
		if n != 0 {
			t.Errorf("旧键应删掉，剩 %d 行", n)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
