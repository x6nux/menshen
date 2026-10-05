package store

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

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
--
-- is_main：配置里那个 bot 是**主 bot**，只做配置管理与接入其他 bot，
-- 不入群、不判定广告。标记落库而不是运行时比对 cfg.BotToken ——
-- webhook 模式下该字段可为空，配置一改就会漂移。
CREATE TABLE IF NOT EXISTS bots (
  token      TEXT    PRIMARY KEY,
  bot_id     INTEGER NOT NULL,
  username   TEXT    NOT NULL DEFAULT '',
  owner_id   INTEGER NOT NULL,
  so_model   TEXT    NOT NULL DEFAULT '',
  llm_model  TEXT    NOT NULL DEFAULT '',
  enabled    INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  is_main    INTEGER NOT NULL DEFAULT 0,
  -- so_models / llm_models 是 JSON 数组，按重试顺序排列；空串回退到
  -- 上面那两个单值旧列。模型名形如 <上游名>/<模型ID>。
  so_models  TEXT    NOT NULL DEFAULT '',
  llm_models TEXT    NOT NULL DEFAULT ''
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

-- 联合封禁的全局组。默认存在，bot 可选择加入（bots 级设置 gban_global）：
-- 加入的 bot 共享彼此的命中，也接收全组执行。
CREATE TABLE IF NOT EXISTS gban (
  user_id    INTEGER PRIMARY KEY,
  reason     TEXT    NOT NULL DEFAULT '',
  src_chat   INTEGER NOT NULL DEFAULT 0,
  by_bot     INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);

-- 专属联合封禁组：每个管理员（主/次）名下一个，可整体开关并圈定生效群。
-- 封禁条目按管理员分账本——名下 bot 命中最高档处置时记进归属人的专属组，
-- 只在他圈定的群里执行。enabled 缺行视为开启：组的存在不需要显式创建。
CREATE TABLE IF NOT EXISTS gban_own (
  owner_id   INTEGER PRIMARY KEY,
  enabled    INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS gban_own_chats (
  owner_id INTEGER NOT NULL,
  chat_id  INTEGER NOT NULL,
  PRIMARY KEY (owner_id, chat_id)
);
CREATE TABLE IF NOT EXISTS gban_own_bans (
  owner_id   INTEGER NOT NULL,
  user_id    INTEGER NOT NULL,
  reason     TEXT    NOT NULL DEFAULT '',
  src_chat   INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (owner_id, user_id)
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
  kind       TEXT    NOT NULL DEFAULT 'profile',
  reason     TEXT    NOT NULL DEFAULT '',
  notice_msg INTEGER NOT NULL DEFAULT 0,
  attempts   INTEGER NOT NULL DEFAULT 0,
  shape      TEXT    NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  PRIMARY KEY (chat_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_jmute_user ON join_mutes(user_id);

-- 判定用的 AI 上游。反广告只发两种请求：systemone 决策端点做主判，
-- chat/completions 做复判与形态总结，因此只有这两个开关。
-- kind 是渠道类型（openai / cloudflare），决定端点路径与响应信封；
-- 两个 supports 开关仍决定该渠道用在哪条链路上（见 upstream 包）。
CREATE TABLE IF NOT EXISTS upstreams (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  name               TEXT    NOT NULL,
  base_url           TEXT    NOT NULL,
  api_key            TEXT    NOT NULL,
  weight             INTEGER NOT NULL DEFAULT 1,
  status             INTEGER NOT NULL DEFAULT 1,
  supports_chat      INTEGER NOT NULL DEFAULT 1,
  supports_systemone INTEGER NOT NULL DEFAULT 0,
  kind               TEXT    NOT NULL DEFAULT 'openai'
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
  prewarm_checked_at INTEGER NOT NULL DEFAULT 0,
  profile_hash TEXT NOT NULL DEFAULT '',
  prewarm_next_at INTEGER NOT NULL DEFAULT 0,
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

-- group_messages 是群消息的全量留底，供 /check 事后复查。
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

-- profile_shapes 是同模板批量账号的「资料形状」指纹：资料文本归一化后
-- （链接/数字/@用户名占位、去零宽与标点）的形态。第一个账号被判广告后
-- 学习下来，后续同形状账号零 AI 直接禁言。全局表，不按 bot 隔离：
-- 账号资料模板是攻击者侧的公开复用，跨租户命中是收益。
-- last_hit 兼作过期依据（CleanupData 按 log_retention_days 保留期清理）。
CREATE TABLE IF NOT EXISTS profile_shapes (
  shape      TEXT PRIMARY KEY,
  kind       TEXT    NOT NULL DEFAULT '',
  hits       INTEGER NOT NULL DEFAULT 0,
  last_hit   INTEGER NOT NULL DEFAULT 0,
  sample     TEXT    NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);

-- 申诉单：一个用户对一个 bot 的一次申诉。状态机见 docs/superpowers/specs
-- 的 §1.5；同一人同一 bot 同时只能有一张未结单。
CREATE TABLE IF NOT EXISTS appeals (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  bot_id       INTEGER NOT NULL,
  user_id      INTEGER NOT NULL,
  status       TEXT    NOT NULL,              -- statement/ai/lifted/web/noweb/code/redeemed/rejected/expired
  statement    TEXT    NOT NULL DEFAULT '',
  ai_result    TEXT    NOT NULL DEFAULT '',   -- uphold / overturn / error / skipped
  ai_conf      REAL    NOT NULL DEFAULT 0,
  ai_reason    TEXT    NOT NULL DEFAULT '',
  ai_model     TEXT    NOT NULL DEFAULT '',
  ai_cost      INTEGER NOT NULL DEFAULT 0,
  web_attempts INTEGER NOT NULL DEFAULT 0,
  web_since    INTEGER NOT NULL DEFAULT 0,    -- 进入 web 的时刻，24 小时窗口从这里算
  code         TEXT    NOT NULL DEFAULT '',
  code_expires INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_appeal_user ON appeals(bot_id, user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_appeal_code ON appeals(code) WHERE code != '';

-- 兑换记录：主键保证每个群只兑换一次；chat_id = 0 表示私聊兑换。
CREATE TABLE IF NOT EXISTS appeal_redeems (
  appeal_id INTEGER NOT NULL,
  chat_id   INTEGER NOT NULL,
  by_uid    INTEGER NOT NULL,
  at        INTEGER NOT NULL,
  PRIMARY KEY (appeal_id, chat_id)
);

-- 网页验证记录：不论成败都记，账号关联靠它。IP 与浏览器特征只存在这里，
-- 不写进任何 Telegram 消息、也不上任何网页。
CREATE TABLE IF NOT EXISTS web_checks (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  appeal_id  INTEGER NOT NULL,
  bot_id     INTEGER NOT NULL,
  user_id    INTEGER NOT NULL,
  ip         TEXT    NOT NULL DEFAULT '',
  ua         TEXT    NOT NULL DEFAULT '',
  fp         TEXT    NOT NULL DEFAULT '',
  signals    TEXT    NOT NULL DEFAULT '',     -- 规范化前的原始特征 JSON
  flags      TEXT    NOT NULL DEFAULT '',     -- 命中的硬 / 软信号
  result     TEXT    NOT NULL,                -- pass / bot / turnstile
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_wc_fp   ON web_checks(fp);
CREATE INDEX IF NOT EXISTS idx_wc_ip   ON web_checks(ip, created_at);
CREATE INDEX IF NOT EXISTS idx_wc_user ON web_checks(user_id);

-- 白名单：bot_id = 0 全平台；chat_id = 0 该 bot 名下所有群；expires_at = 0 永久。
CREATE TABLE IF NOT EXISTS ad_whitelist (
  bot_id     INTEGER NOT NULL,
  chat_id    INTEGER NOT NULL,
  user_id    INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  source     TEXT    NOT NULL,                -- appeal / adw
  by_uid     INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (bot_id, chat_id, user_id)
);

-- profile_ok：复判确认「资料本身没问题」的账号，到期前不再因资料被判广告。
-- 与白名单不同：它只免掉「账号资料」这一路的嫌疑，正文、引用、历史照常判断。
-- phash 绑定当时那份资料（用户名/昵称/简介），改过就重新判；时长由复判模型
-- 在 1~72 小时之间给（见 antiad.GrantProfileOK）。
CREATE TABLE IF NOT EXISTS profile_ok (
  bot_id     INTEGER NOT NULL,
  user_id    INTEGER NOT NULL,
  phash      TEXT    NOT NULL,
  hours      INTEGER NOT NULL DEFAULT 0,
  reason     TEXT    NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  PRIMARY KEY (bot_id, user_id)
);

-- ad_rules 是 AI 从历史封禁记录里总结出的「必封正则规则」，全局一份，
-- 只有主管理员能维护。规则默认**候选、不启用**：enabled=0 时完全不参与；
-- enabled=1 且 enforce=0 时命中只作为证据注入判定 prompt；
-- enforce=1 时命中即按最高档处置（复用人工标记广告的动作），零 AI 成本。
--
-- 误封防线：任何创建/保存都先跑一次全库测试（见 antiad.TestRulePattern），
-- last_tp/last_fp/last_undone 就是那一轮的统计；enforce 只允许在
-- last_fp=0 且 last_undone=0 时打开（服务端强制）。
CREATE TABLE IF NOT EXISTS ad_rules (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  name           TEXT    NOT NULL,
  pattern        TEXT    NOT NULL,
  category       TEXT    NOT NULL DEFAULT '',
  note           TEXT    NOT NULL DEFAULT '',
  source         TEXT    NOT NULL DEFAULT 'ai',
  enabled        INTEGER NOT NULL DEFAULT 0,
  enforce        INTEGER NOT NULL DEFAULT 0,
  hits           INTEGER NOT NULL DEFAULT 0,
  last_matched   INTEGER NOT NULL DEFAULT 0,
  last_tp        INTEGER NOT NULL DEFAULT 0,
  last_fp        INTEGER NOT NULL DEFAULT 0,
  last_undone    INTEGER NOT NULL DEFAULT 0,
  last_scanned   INTEGER NOT NULL DEFAULT 0,
  last_ads_total INTEGER NOT NULL DEFAULT 0,
  last_kinds     TEXT    NOT NULL DEFAULT '',
  last_tested_at INTEGER NOT NULL DEFAULT 0,
  created_at     INTEGER NOT NULL DEFAULT 0,
  created_by     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_ad_rules_enabled ON ad_rules(enabled);
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
	if err := migrateSettingUnits(write); err != nil {
		write.Close()
		return nil, err
	}
	if err := ensureIndexes(write); err != nil {
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
		// whitelisted：反广告按群白名单（/white）。全局豁免仍在
		// settings.antiad_exempt_users。
		{"group_members", "whitelisted", "INTEGER NOT NULL DEFAULT 0"},
		// media_group：相册 ID，判成广告时整组删除。不建索引：按相册找兄弟消息
		// 带着 chat_id、user_id，走 idx_gmsg_user；而 schemaSQL 先于 migrate 执行，
		// 在这一列上建索引会让老库启动时直接报「no such column」。
		{"group_messages", "media_group", "TEXT NOT NULL DEFAULT ''"},
		// punish：本群的处罚方式。-1 = 跟随 bot 设置（antiad_ban），0 = 禁言，1 = 封禁。
		{"bot_chats", "punish", "INTEGER NOT NULL DEFAULT -1"},
		// is_main：主 bot 标记。老库升级时先全部按工作 bot 处理，
		// 由 ensureMainBot 在启动时把配置里那个 bot 置 1。
		{"bots", "is_main", "INTEGER NOT NULL DEFAULT 0"},
		// so_models / llm_models：模型列表（JSON 数组，按重试顺序）。
		// 老库的空串会回退到 so_model / llm_model 单值列。
		{"bots", "so_models", "TEXT NOT NULL DEFAULT ''"},
		{"bots", "llm_models", "TEXT NOT NULL DEFAULT ''"},
		// user_name：判定当时的昵称与用户名。广告号被处置后常改名，
		// 事后再查就对不上了，所以判定时就要记下来。
		{"antiad_log", "user_name", "TEXT NOT NULL DEFAULT ''"},
		// last_ads_total / last_kinds：最近一轮全库测试的覆盖率分母与按
		// 类型细分（JSON）。老库默认 0/空串，前端按「未测过覆盖率」展示。
		{"ad_rules", "last_ads_total", "INTEGER NOT NULL DEFAULT 0"},
		{"ad_rules", "last_kinds", "TEXT NOT NULL DEFAULT ''"},
		// lifted_at：这条处罚被人工解除（或申诉撤销）的时刻。
		// 永久禁言没有到期时间，靠时间窗推断「是否仍在限制中」会把
		// 已解除的也一直列出来，所以解除时要落一个显式标记。
		{"antiad_log", "lifted_at", "INTEGER NOT NULL DEFAULT 0"},
		// kind：渠道类型。老库一律按 openai 处理，行为与升级前完全一致。
		{"upstreams", "kind", "TEXT NOT NULL DEFAULT 'openai'"},
		// kind：进群类限制的来源。profile = 冷判定/延迟复查（资料里有广告），
		// prewarm = 前置号识别（空壳+招呼的综合特征）。申诉提示词与解除
		// 口径按它分流。
		{"join_mutes", "kind", "TEXT NOT NULL DEFAULT 'profile'"},
		// prewarm_checked_at：前置号复查的上次执行时间。复查按进群时长的
		// 阶梯（1/5/10/30/60min）预排在 prewarm_next_at，这个字段只做展示
		// 与排查。
		{"group_members", "prewarm_checked_at", "INTEGER NOT NULL DEFAULT 0"},
		// profile_hash：上次复查时落下的资料指纹（profileHash，见
		// antiad/profile_ok.go）。指纹没变就不花 AI 重判；空串表示还没查过。
		{"group_members", "profile_hash", "TEXT NOT NULL DEFAULT ''"},
		// prewarm_next_at：前置号复查的下次到期时间（预排）。0 = 从未
		// 复查过，立刻进一次候选；到点选中后由 worker 按进群时长的
		// 阶梯（1/5/10/30/60min）覆盖。
		{"group_members", "prewarm_next_at", "INTEGER NOT NULL DEFAULT 0"},
		// shape：这条进群限制对应的资料形状哈希（profile_shapes.shape）。
		// 解除限制时按它反查删除，避免误伤解掉之后形状还在复用。
		{"join_mutes", "shape", "TEXT NOT NULL DEFAULT ''"},
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

// migrateSettingUnits 把按旧单位存的设置换算成新单位。目前只有禁言时长：
// antiad_mute_hours（小时）→ antiad_mute_minutes（分钟）。
//
// 老部署里 0 表示永久禁言，乘 60 之后仍是 0；非数字的脏值不换算（新键缺省，
// 即回到默认 24 小时），但旧键一律删掉——留着的话读侧会以为它还生效。
// 换算完删除旧键，所以这段迁移只会生效一次。
func migrateSettingUnits(w *sql.DB) error {
	conv := func(v string) (string, bool) {
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return "", false
		}
		return strconv.FormatInt(n*60, 10), true
	}

	var gv string
	switch err := w.QueryRow(`SELECT v FROM settings WHERE k='antiad_mute_hours'`).Scan(&gv); {
	case err == nil:
		if nv, ok := conv(gv); ok {
			if _, err := w.Exec(`INSERT INTO settings (k,v)
				VALUES ('antiad_mute_minutes',?) ON CONFLICT(k) DO NOTHING`, nv); err != nil {
				return err
			}
		}
		if _, err := w.Exec(`DELETE FROM settings WHERE k='antiad_mute_hours'`); err != nil {
			return err
		}
	case err != sql.ErrNoRows:
		return err
	}

	rows, err := w.Query(`SELECT bot_id,v FROM bot_settings WHERE k='antiad_mute_hours'`)
	if err != nil {
		return err
	}
	type rec struct {
		botID int64
		v     string
	}
	var list []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.botID, &r.v); err != nil {
			rows.Close()
			return err
		}
		list = append(list, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range list {
		if nv, ok := conv(r.v); ok {
			if _, err := w.Exec(`INSERT INTO bot_settings (bot_id,k,v)
				VALUES (?,'antiad_mute_minutes',?) ON CONFLICT(bot_id,k) DO NOTHING`,
				r.botID, nv); err != nil {
				return err
			}
		}
		if _, err := w.Exec(`DELETE FROM bot_settings
			WHERE bot_id=? AND k='antiad_mute_hours'`, r.botID); err != nil {
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

// AppealOpenStatuses 是申诉单的「未结」状态集合。
//
// 它同时出现在读侧（openAppeal）、人工处理（AdminLift/Reject）、保留期清理
// 与未结唯一索引里。分散成几份字面量迟早漂移——noweb 就漂过一次：用户能
// 反复发起、管理员又结不了案。统一放在这里，读侧与 SQL 各取所需。
var AppealOpenStatuses = []string{"statement", "ai", "web", "code", "noweb"}

// AppealOpenStatusesSQL 是同一个集合的 SQL 字面量（IN 列表用）。
const AppealOpenStatusesSQL = "'statement','ai','web','code','noweb'"

// ensureIndexes 建运行期查询依赖的索引。
//
// 必须晚于 migrate：schemaSQL 先于它执行，而 bot_id 这类列是老库升级时
// 才补上的，把索引写进 schemaSQL 会让老库启动直接报「no such column」
// ——group_messages.media_group 已经踩过一次同样的坑（见 migrate 注释）。
//
// 索引对应的高频查询：
//   - antiad_log(bot_id,id)：汇总游标的 MAX(id)（每分钟每 bot 一次）、
//     面板「拦截记录」按 bot 分页。缺它时两者都是全表扫。
//   - antiad_log(bot_id,created_at)：面板与 Mini App 的「近 24 小时」统计。
//   - antiad_log(user_id,id)：/user、Mini App 按人筛、申诉的有效处罚查询。
//   - group_members(user_id,ad_hits,chat_id)：申诉卡片的关联账号（覆盖索引）。
//   - group_members(last_msg_at)：保留期清理（谓词已按单列改写）。
//   - group_members(chat_id,prewarm_next_at)：前置号复查按到期时间取人。
//     复查阶梯本身不建索引：选人只按 prewarm_next_at <= now，不逐行算年龄。
//   - web_checks(appeal_id,id)/(created_at)：申诉详情页与保留期清理。
//   - alert_cleanup(bot_id,due_at)/(due_at)：每分钟的撤回扫描与清理。
//   - ad_hashes(last_hit_at)、appeals(updated_at)、ad_whitelist(expires_at)、
//     gban(created_at)：保留期清理与面板列表。
func ensureIndexes(db *sql.DB) error {
	stmts := []string{
		`CREATE INDEX IF NOT EXISTS idx_antiad_bot_id ON antiad_log(bot_id, id)`,
		`CREATE INDEX IF NOT EXISTS idx_antiad_bot_time ON antiad_log(bot_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_antiad_uid ON antiad_log(user_id, id)`,
		`CREATE INDEX IF NOT EXISTS idx_gmember_user ON group_members(user_id, ad_hits, chat_id)`,
		`CREATE INDEX IF NOT EXISTS idx_gmember_last_msg ON group_members(last_msg_at)`,
		`CREATE INDEX IF NOT EXISTS idx_gmember_pwnext ON group_members(chat_id, prewarm_next_at)`,
		`CREATE INDEX IF NOT EXISTS idx_wc_appeal ON web_checks(appeal_id, id)`,
		`CREATE INDEX IF NOT EXISTS idx_wc_time ON web_checks(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_alert_cleanup_due ON alert_cleanup(bot_id, due_at)`,
		`CREATE INDEX IF NOT EXISTS idx_alert_cleanup_at ON alert_cleanup(due_at)`,
		`CREATE INDEX IF NOT EXISTS idx_adh_last_hit ON ad_hashes(last_hit_at)`,
		`CREATE INDEX IF NOT EXISTS idx_appeal_updated ON appeals(updated_at)`,
		`CREATE INDEX IF NOT EXISTS idx_aw_expires ON ad_whitelist(expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_gban_created ON gban(created_at)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return fmt.Errorf("建索引失败（%s）: %w", q, err)
		}
	}

	// 同一 (bot,user) 只允许一张未结单：重复投递的回调并发建单会留下孤儿单，
	// 而孤儿单会永久占住 openAppeal 的入口。老库上可能已有重复，直接建
	// 唯一索引会让启动失败，所以先收拢（每个组合保留最新一张）。
	if _, err := db.Exec(`UPDATE appeals SET status='expired', updated_at=?
		WHERE status IN (`+AppealOpenStatusesSQL+`) AND id NOT IN (
			SELECT MAX(id) FROM appeals WHERE status IN (`+AppealOpenStatusesSQL+`)
			GROUP BY bot_id, user_id)`, time.Now().Unix()); err != nil {
		return fmt.Errorf("收拢重复的未结申诉单失败: %w", err)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_appeal_open
		ON appeals(bot_id, user_id) WHERE status IN (` + AppealOpenStatusesSQL + `)`); err != nil {
		return fmt.Errorf("建未结申诉唯一索引失败: %w", err)
	}
	return nil
}

func (s *Store) Close() error {
	s.Read.Close()
	return s.Write.Close()
}
