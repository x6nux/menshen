package store

import (
	"database/sql"

	_ "modernc.org/sqlite"
)

type Store struct {
	Read  *sql.DB
	Write *sql.DB
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS settings (k TEXT PRIMARY KEY, v TEXT NOT NULL);

-- 次级管理员。主管理员在 config.admin_ids 里，不进这张表——
-- 配置文件是唯一不可能被面板误操作删掉的地方，主管的权限必须扎在那里。
CREATE TABLE IF NOT EXISTS admins (
  user_id    INTEGER PRIMARY KEY,
  note       TEXT    NOT NULL DEFAULT '',
  added_by   INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);

-- 接入的 bot。token 是主键也是凭证，面板上一律只露 maskToken 的结果。
-- so_model / llm_model 为空表示沿用全局默认，由主管理员按 bot 覆盖。
CREATE TABLE IF NOT EXISTS bots (
  token      TEXT    PRIMARY KEY,
  bot_id     INTEGER NOT NULL,
  username   TEXT    NOT NULL DEFAULT '',
  owner_id   INTEGER NOT NULL,
  so_model   TEXT    NOT NULL DEFAULT '',
  llm_model  TEXT    NOT NULL DEFAULT '',
  enabled    INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_bots_owner ON bots(owner_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_bots_botid ON bots(bot_id);

-- bot 在某群的生效配置。取代了早先那个全局的 antiad_chats 列表。
-- dryrun 下沉到每群是刻意的：新加的群先演练、老群已转正式，是最常见的形态，
-- 一个全局开关做不到这件事。
CREATE TABLE IF NOT EXISTS bot_chats (
  bot_id      INTEGER NOT NULL,
  chat_id     INTEGER NOT NULL,
  title       TEXT    NOT NULL DEFAULT '',
  enabled     INTEGER NOT NULL DEFAULT 1,
  dryrun      INTEGER NOT NULL DEFAULT 1,
  group_alert INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL DEFAULT 0,
  punish      INTEGER NOT NULL DEFAULT -1,
  PRIMARY KEY (bot_id, chat_id)
);

-- per-bot 的设置覆盖。用 KV 而非定宽列：阈值项会随特性增减，
-- 加一项不该动 schema，而 schemaSQL 对已存在的表完全无效。
CREATE TABLE IF NOT EXISTS bot_settings (
  bot_id INTEGER NOT NULL,
  k      TEXT    NOT NULL,
  v      TEXT    NOT NULL,
  PRIMARY KEY (bot_id, k)
);

-- 联合封禁名单。全平台共享：任一接入群判定的广告号，在所有 bot 的
-- 所有生效群一起封，新号进群时也拦在门口。
CREATE TABLE IF NOT EXISTS gban (
  user_id    INTEGER PRIMARY KEY,
  reason     TEXT    NOT NULL DEFAULT '',
  src_chat   INTEGER NOT NULL DEFAULT 0,
  by_bot     INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);

-- 进群冷判定后被限制发言的人，以及他自助解除所需的全部上下文。
--
-- 必须落库而不是只存内存：这条限制是**无限期**的（要等本人改正，不是等
-- 时间到），进程重启后如果丢了 reason 与 notice_msg，被禁的人就永远
-- 卡在「不知道为什么、也点不动按钮」的状态里。
--
-- attempts 只增不减，是成本账而非处罚：重判一次花一次 AI 的钱，
-- 面板要看得见谁在反复刷。
CREATE TABLE IF NOT EXISTS join_mutes (
  chat_id    INTEGER NOT NULL,
  user_id    INTEGER NOT NULL,
  bot_id     INTEGER NOT NULL,
  reason     TEXT    NOT NULL DEFAULT '',
  notice_msg INTEGER NOT NULL DEFAULT 0,
  attempts   INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (chat_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_jmute_user ON join_mutes(user_id);

-- 判定用的 AI 上游。反广告只发两种请求：systemone 决策端点做主判，
-- chat/completions 做复判与形态总结，因此只有这两个开关。
CREATE TABLE IF NOT EXISTS upstreams (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  name               TEXT    NOT NULL,
  base_url           TEXT    NOT NULL,
  api_key            TEXT    NOT NULL,
  weight             INTEGER NOT NULL DEFAULT 1,
  status             INTEGER NOT NULL DEFAULT 1,
  supports_chat      INTEGER NOT NULL DEFAULT 1,
  supports_systemone INTEGER NOT NULL DEFAULT 0
);

-- 模型单价，仅用于把判定开销折算成钱摆到面板上。
-- 判定本身不依赖它——价格缺失只会让开销显示为 0，不影响拦截。
CREATE TABLE IF NOT EXISTS models (
  name              TEXT PRIMARY KEY,
  prompt_price      REAL    NOT NULL,
  completion_price  REAL    NOT NULL,
  cache_read_price  REAL    NOT NULL DEFAULT 0,
  cache_write_price REAL    NOT NULL DEFAULT 0,
  enabled           INTEGER NOT NULL DEFAULT 1
);

-- 群成员画像。反广告的「新人 / 老人」分档全部由它派生。
-- joined_at = 0 表示 bot 部署前此人已在群里，年龄未知，一律按老用户处理。
CREATE TABLE IF NOT EXISTS group_members (
  chat_id     INTEGER NOT NULL,
  user_id     INTEGER NOT NULL,
  joined_at   INTEGER NOT NULL DEFAULT 0,
  first_seen  INTEGER NOT NULL,
  msg_count   INTEGER NOT NULL DEFAULT 0,
  last_msg_at INTEGER NOT NULL DEFAULT 0,
  ad_hits     INTEGER NOT NULL DEFAULT 0,
  whitelisted INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (chat_id, user_id)
);

-- 反广告判定流水：带置信度、处置与成本的账本，只记**判过的**消息。
CREATE TABLE IF NOT EXISTS antiad_log (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  chat_id           INTEGER NOT NULL,
  user_id           INTEGER NOT NULL,
  message_id        INTEGER NOT NULL,
  text              TEXT    NOT NULL,
  verdict           TEXT    NOT NULL,
  confidence        REAL    NOT NULL,
  decider           TEXT    NOT NULL,
  ad_kind           TEXT    NOT NULL DEFAULT '',
  action            TEXT    NOT NULL,
  reason            TEXT    NOT NULL,
  prompt_tokens     INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  quota_cost        INTEGER NOT NULL DEFAULT 0,
  created_at        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_antiad_time ON antiad_log(created_at);
CREATE INDEX IF NOT EXISTS idx_antiad_user ON antiad_log(chat_id, user_id);

-- group_messages 是群消息的全量留底，供 /ad 事后复查。
-- 与 antiad_log 分开：后者只记判过的，被护栏去重/限流拦下的、豁免者发的
-- 都不在里面——而那恰恰是刷屏号最可能藏东西的地方。主键用
-- (chat_id, message_id)：TG 重推同一条 update 时不得留两份。
-- 没有文字的消息（图片、贴纸）也记，text 为空：判成广告号时要连带删掉
-- 此人近期的全部消息，靠的就是这里的 message_id。media_group 是相册 ID。
CREATE TABLE IF NOT EXISTS group_messages (
  chat_id     INTEGER NOT NULL,
  message_id  INTEGER NOT NULL,
  user_id     INTEGER NOT NULL,
  text        TEXT    NOT NULL,
  at          INTEGER NOT NULL,
  media_group TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (chat_id, message_id)
);
CREATE INDEX IF NOT EXISTS idx_gmsg_user ON group_messages(chat_id, user_id, at);
CREATE INDEX IF NOT EXISTS idx_gmsg_at ON group_messages(at);

-- alert_cleanup 是待撤回的群内告警。记在库里而不是内存计时器上：
-- 进程一重启计时器就丢了，还没到点的告警会永久留在群里。
-- bot_id 决定由哪个 bot 去撤：只有发消息的那个 bot 删得掉它。
CREATE TABLE IF NOT EXISTS alert_cleanup (
  bot_id     INTEGER NOT NULL,
  chat_id    INTEGER NOT NULL,
  message_id INTEGER NOT NULL,
  due_at     INTEGER NOT NULL,
  PRIMARY KEY (chat_id, message_id)
);

-- ad_hashes 是「消息本身就是广告」的内容指纹。同样的内容再出现时不再送检、
-- 直接删除，禁言交给复判模型。只记消息级广告：账号级广告的正文可能只是
-- 一句「你好」，记下来会误删所有人的你好。按 bot 隔离：一个租户的误判
-- 不该删到别人的群里。
CREATE TABLE IF NOT EXISTS ad_hashes (
  bot_id      INTEGER NOT NULL,
  hash        TEXT    NOT NULL,
  log_id      INTEGER NOT NULL,
  kind        TEXT    NOT NULL DEFAULT '',
  confidence  REAL    NOT NULL DEFAULT 0,
  hits        INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  last_hit_at INTEGER NOT NULL,
  PRIMARY KEY (bot_id, hash)
);
`

func Open(path string) (*Store, error) {
	// synchronous(NORMAL)：WAL 下它不会损坏库，只是断电时可能丢最后几笔写入；
	// 默认的 FULL 让每次写都等一次 fsync，而每条群消息要写两次（画像 + 留底），
	// 广告洪峰时这就是瓶颈。pragma 按连接生效，所以必须写在 DSN 里。
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)" +
		"&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"

	write, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// ponytail: 写连接固定为 1。SQLite 本就单写者，放开只会换来 SQLITE_BUSY。
	write.SetMaxOpenConns(1)

	if _, err := write.Exec(schemaSQL); err != nil {
		write.Close()
		return nil, err
	}
	if err := migrate(write); err != nil {
		write.Close()
		return nil, err
	}

	read, err := sql.Open("sqlite", dsn)
	if err != nil {
		write.Close()
		return nil, err
	}
	read.SetMaxOpenConns(4)

	return &Store{Read: read, Write: write}, nil
}

// migrate 补齐老库缺失的列。
//
// schemaSQL 全是 CREATE TABLE IF NOT EXISTS，对已存在的表完全无效——
// 新增字段必须靠 ALTER。SQLite 没有 ADD COLUMN IF NOT EXISTS，
// 所以先查 table_info 再决定加不加：盲目 ALTER 会在每次启动时报错。
//
// 只在新库上验证过的迁移，恰好会在唯一有数据的那些部署上炸。
func migrate(db *sql.DB) error {
	cols := []struct{ table, col, def string }{
		// bot_id：判定流水归属哪个 bot。多租户后它是「谁烧的钱」「谁能看
		// 这条记录」两个判断的共同依据。老库的行默认 0 = 归属未知，
		// 面板按主管理员可见处理。
		{"antiad_log", "bot_id", "INTEGER NOT NULL DEFAULT 0"},
		// whitelisted：反广告按群白名单（/adw）。全局豁免仍在
		// settings.antiad_exempt_users。
		{"group_members", "whitelisted", "INTEGER NOT NULL DEFAULT 0"},
		// media_group：相册 ID，判成广告时整组删除。不建索引：按相册找兄弟消息
		// 带着 chat_id、user_id，走 idx_gmsg_user；而 schemaSQL 先于 migrate 执行，
		// 在这一列上建索引会让老库启动时直接报「no such column」。
		{"group_messages", "media_group", "TEXT NOT NULL DEFAULT ''"},
		// punish：本群的处罚方式。-1 = 跟随 bot 设置（antiad_ban），0 = 禁言，1 = 封禁。
		{"bot_chats", "punish", "INTEGER NOT NULL DEFAULT -1"},
	}
	for _, c := range cols {
		has, err := hasColumn(db, c.table, c.col)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := db.Exec("ALTER TABLE " + c.table + " ADD COLUMN " + c.col + " " + c.def); err != nil {
			return err
		}
	}
	return nil
}

func hasColumn(db *sql.DB, table, col string) (bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) Close() error {
	s.Read.Close()
	return s.Write.Close()
}
