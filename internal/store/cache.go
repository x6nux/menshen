package store

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync/atomic"

	"menshen/internal/upstream"
)

// settings 的默认值：read-with-fallback，不做启动 seed。
// 默认值在 Snapshot 构建时铺满，读侧永远拿到完整表。
//
// 新增设置项只需往这里加一条；新增**表**必须进 schemaSQL，
// 新增**列**必须进 migrate()。
//
// 作用域分三层，见 settingSpecs 的 scope 字段：
//   - 全局：只有主管理员能改，本表是唯一来源
//   - per-bot：本表的值是**默认**，bot_settings 可逐 bot 覆盖
//   - per-chat：不在本表，直接是 bot_chats 的列
var settingDefaults = map[string]string{
	"tz_offset":          "8",
	"log_retention_days": "30",

	// ---- 全局（主管理员）----
	// antiad_enabled 是全平台急停：关掉它，所有 bot 的所有群一起停判。
	// 单个群的启停在 bot_chats.enabled 上。
	"antiad_enabled": "0",
	// 判定模型列表（JSON 数组，按重试顺序）。每项形如 <上游名>/<模型ID>；
	// 旧格式（无前缀）继续兼容，任选可用上游。空列表 = 该级不跑。
	"antiad_so_models":  "[]", // systemone 判定模型列表，空则只走大模型
	"antiad_llm_models": "[]", // 大模型复判/总结模型列表，空则不复判不总结
	// 旧单值键：仅作单元素回退读，面板保存新键时会清掉它。
	"antiad_so_model":  "",
	"antiad_llm_model": "",
	// 上游异常告警：连续失败达到 after 次且距上次告警超过冷却才发一条；
	// after = 0 关闭。计数是进程级的 —— 坏上游是全局资源，不该每个群各报一次。
	"antiad_upstream_alert_after":   "5",
	"antiad_upstream_alert_minutes": "30",
	// 形态摘要是全局一份：所有 bot 的样本汇进同一个池子，学得最快。
	"antiad_digest":         "",
	"antiad_digest_last_id": "0",
	"antiad_digest_min":     "5",
	"antiad_digest_max":     "1200",
	// 联合封禁：默认关。它会跨 bot 跨群批量封人，打开前要想清楚。
	"gban_enabled": "0",
	// 每个次级管理员能接入的 bot 数上限。主管理员不受限。
	"max_bots_per_admin": "5",
	// 是否把所有 bot 的命中告警抄送主管理员。默认关——
	// 分发出去之后，主管的私聊不该被每一个次管的群刷屏。
	"alert_copy_main": "0",
	// ---- AI 请求（全局）：不稳定的是上游模型本身，与哪个 bot 发起无关 ----
	// systemone 平均不到 1 秒出结果，整次请求超过这个时限就当卡住，换一个立即重试。
	"antiad_so_timeout_ms": "2000",
	// 复判走流式，首字超过这个时限就当卡住，换一个立即重试。
	"antiad_llm_ttft_ms": "5000",
	// 1 分钟内重试超过这个次数就进入并发模式（每轮同时发多路，先到先得）。0 = 关闭。
	"antiad_hedge_retries": "5",
	"antiad_hedge_minutes": "5", // 并发模式持续多久，期间再触发会顺延
	"antiad_hedge_fanout":  "2", // 并发模式下每轮同时发几路；期间开销随之成倍
	// 识图模型（看图片与贴纸），空则图片与贴纸不判。全局一份：它按图片去重缓存，
	// 与哪个 bot 收到这张图无关。
	"antiad_vision_model": "",

	// ---- per-bot（owner 或主管可覆盖，下面是默认值）----
	// 三条线都用百分数整数：settingSpec 只支持 int64 校验，
	// 引入浮点要改动整套设置面板机制，不值当。
	"antiad_so_trust":   "80", // systemone 置信度采信线
	"antiad_act_hard":   "90", // 删除 + 禁言线
	"antiad_act_soft":   "75", // 删除线
	"antiad_new_hours":  "72", // 新人时长界
	"antiad_new_msgs":   "10", // 新人消息数界
	"antiad_mute_hours": "24",
	// 滥用护栏：全量送检是明知成本的选择，这几项只堵滥用，不改设计。
	// 窗口长度沿用 limiter.go 的 1 分钟。0 = 不限。
	"antiad_rpm_chat":     "30", // 每群每分钟送检上限
	"antiad_alert_rpm":    "3",  // 同一 (群,人) 每分钟告警上限
	"antiad_cmd_rpm":      "3",  // 每人每分钟 /check、/ban 次数上限
	"antiad_ctx_msgs":     "6",
	"antiad_alert_ttl":    "300", // 群内告警自动撤回秒数，0 = 永不撤回
	"antiad_dm_admins":    "1",   // 是否私聊 owner
	"antiad_exempt_users": "[]",  // 该 bot 的豁免名单
	// 禁言档改为封禁出群（永久）。每群可单独覆盖（bot_chats.punish）。
	"antiad_ban": "0",
	// 判定成员 bot 的消息。默认豁免：群里的工具 bot 常发链接。
	// bot 默认看不见其他 bot 的消息，开了也要对方满足 Bot-to-Bot 条件才收得到。
	"antiad_judge_bots": "0",
	// 管理员私聊改为汇总：每隔这么多分钟最多一条。
	"antiad_alert_every": "5",
	// 汇总游标（antiad_log.id，按 bot 存在 bot_settings）。-1 = 还没初始化：
	// 升级后第一次运行只记位置，不翻旧账。
	"antiad_alert_last_id": "-1",

	// ---- 进群冷判定（per-bot）----
	// 新人进群时不等他发言，先就账号画像（昵称/用户名/简介）判一次，
	// 判为广告号即无限期限制发言，由本人改正后自助解除。默认关：
	// 它对每个进群的人都要花一次 AI 的钱。
	"antiad_cold": "0",
	// 冷判定的采信线，比消息判定的处置线更高——进群画像的证据比一条
	// 具体消息少得多，宁可漏也不要在人刚进门时就误伤。
	"antiad_cold_conf": "85",
	// 本地预筛：只有昵称/简介出现可疑特征的人才送检。关掉就是每个
	// 进群的人都送检，大群里这是数量级的成本差别。
	"antiad_cold_prefilter": "1",
	// 自助解除的重试间隔基数（秒）。不限次数，但第 n 次要等
	// base × 2^(n-1)，封顶 1 小时——有耐心的人也磨不动多少 AI 开销。
	"antiad_unban_base": "60",
}

// BotRec 是 bots 表的一行。
type BotRec struct {
	Token    string
	BotID    int64
	Username string
	OwnerID  int64
	// SoModels / LLMModels 是判定模型列表（按重试顺序），空表示沿用全局。
	// 每项形如 <上游名>/<模型ID>；旧单值列读出来会并成单元素列表。
	// 只有主管理员能改——模型直接决定判定质量与花掉多少钱。
	SoModels  []string
	LLMModels []string
	Enabled   bool
	CreatedAt int64
	// IsMain 表示这是配置里的主 bot：只做配置管理与接入其他 bot，
	// 不入群、不判定广告（被拉进群会自动退出）。
	IsMain bool
}

// Label 返回面板上展示用的名字，绝不含 token。
func (r *BotRec) Label() string {
	if r.Username != "" {
		return "@" + r.Username
	}
	return strconv.FormatInt(r.BotID, 10)
}

// BotChat 是 bot_chats 的一行：某个 bot 在某个群的行为配置。
type BotChat struct {
	BotID      int64
	ChatID     int64
	Title      string
	Enabled    bool
	Dryrun     bool
	GroupAlert bool
	// Punish 是本群的处罚方式：-1 跟随 bot 设置，0 禁言，1 封禁。见 BanMode。
	Punish    int64
	CreatedAt int64
}

// BanMode 报告这个群的「禁言档」是否改为封禁出群。
// 每群设置优先；跟随时读 bot 级的 antiad_ban（再回落到全局默认）。
func (s *Snapshot) BanMode(c BotChat) bool {
	if c.Punish >= 0 {
		return c.Punish == 1
	}
	return s.BotSettingInt(c.BotID, "antiad_ban", 0) == 1
}

// AdminRec 是 admins 表的一行（次级管理员）。
type AdminRec struct {
	UserID    int64
	Note      string
	AddedBy   int64
	CreatedAt int64
}

// GbanRec 是联合封禁名单的一行。
type GbanRec struct {
	UserID    int64
	Reason    string
	SrcChat   int64
	ByBot     int64
	CreatedAt int64
}

// WhiteRec 是 ad_whitelist 的一行。
//
// 范围规则：BotID = 0 表示全平台；ChatID = 0 表示该 bot 名下所有群；
// ExpiresAt = 0 表示永久。
type WhiteRec struct {
	BotID     int64
	ChatID    int64
	UserID    int64
	ExpiresAt int64
	Source    string
	ByUID     int64
	CreatedAt int64
}

type Snapshot struct {
	Models    map[string]*upstream.Model
	Upstreams []*upstream.Upstream
	Settings  map[string]string

	// bots 按 bot_id 索引，botTokens 按 token 索引。
	// 两份指向同一批对象：webhook 侧只有 token，面板侧只有 bot_id。
	Bots      map[int64]*BotRec
	BotTokens map[string]*BotRec

	BotChats    map[int64]map[int64]BotChat // botID -> chatID -> 配置
	BotSettings map[int64]map[string]string // botID -> k -> v
	Admins      map[int64]AdminRec
	Gban        map[int64]GbanRec
	// Whitelist 是申诉解禁 / /white 产生的白名单，量小，线性扫即可。
	Whitelist []WhiteRec
}

func (s *Snapshot) Setting(k string) string { return s.Settings[k] }

func (s *Snapshot) SettingInt(k string, def int64) int64 {
	if v, err := strconv.ParseInt(s.Settings[k], 10, 64); err == nil {
		return v
	}
	return def
}

func (s *Snapshot) SettingInt64List(k string) []int64 {
	var out []int64
	json.Unmarshal([]byte(s.Settings[k]), &out)
	return out
}

// SettingStrings 读一个字符串数组设置（JSON 数组）。
//
// 解析失败返回 nil 而不是报错：设置项被手工改坏时，上层按「没配」处理，
// 比让整条判定链路起不来强。逗号分隔的形态也认，方便手工改库。
func (s *Snapshot) SettingStrings(k string) []string {
	return parseStringList(s.Settings[k])
}

// ParseStringList 是 SettingStrings 的裸函数版，面板写库前校验也用它。
func ParseStringList(v string) []string { return parseStringList(v) }

func parseStringList(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" || v == "[]" {
		return nil
	}
	var out []string
	if json.Unmarshal([]byte(v), &out) == nil {
		return trimAll(out)
	}
	return trimAll(strings.Split(v, ","))
}

func trimAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// botSetting 三级回退：bot 覆盖 → 全局值 → 默认值（全局值本身已铺满默认）。
//
// botID 为 0 时退化成纯全局读取，这让定时任务这类「不属于任何 bot」的
// 调用方不必分叉。
func (s *Snapshot) BotSetting(botID int64, k string) string {
	if m, ok := s.BotSettings[botID]; ok {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return s.Settings[k]
}

func (s *Snapshot) BotSettingInt(botID int64, k string, def int64) int64 {
	if v, err := strconv.ParseInt(s.BotSetting(botID, k), 10, 64); err == nil {
		return v
	}
	return def
}

func (s *Snapshot) BotSettingInt64List(botID int64, k string) []int64 {
	var out []int64
	json.Unmarshal([]byte(s.BotSetting(botID, k)), &out)
	return out
}

// chatConf 取某个 bot 在某群的配置。第二个返回值为假表示该群不在它的名下。
func (s *Snapshot) ChatConf(botID, chatID int64) (BotChat, bool) {
	c, ok := s.BotChats[botID][chatID]
	return c, ok
}

// chatsOf 返回某个 bot 名下的全部群（含未启用的），按 chat_id 稳定排序。
func (s *Snapshot) ChatsOf(botID int64) []BotChat {
	m := s.BotChats[botID]
	out := make([]BotChat, 0, len(m))
	for _, c := range m {
		out = append(out, c)
	}
	// 插入排序：一个 bot 的群数是个位数到两位数，不值得引排序包。
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ChatID < out[j-1].ChatID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ModelsFor 解析某个 bot 实际使用的判定模型**列表**（按重试顺序）：
// bots 表的覆盖优先，否则全局默认。
//
// 新键是 JSON 数组；旧单值键（antiad_so_model / bots.so_model）作为
// 单元素回退 —— 升级不要求管理员重配，读出来就是一条元素的列表。
func (s *Snapshot) ModelsFor(botID int64) (so, llm []string) {
	so = s.modelList("antiad_so_models", "antiad_so_model")
	llm = s.modelList("antiad_llm_models", "antiad_llm_model")
	if r, ok := s.Bots[botID]; ok {
		if len(r.SoModels) > 0 {
			so = r.SoModels
		}
		if len(r.LLMModels) > 0 {
			llm = r.LLMModels
		}
	}
	return
}

// modelList 读列表键，空则回退单值键（单元素列表）。
func (s *Snapshot) modelList(listKey, singleKey string) []string {
	if l := s.SettingStrings(listKey); len(l) > 0 {
		return l
	}
	if v := strings.TrimSpace(s.Settings[singleKey]); v != "" {
		return []string{v}
	}
	return nil
}

// listOrSingle 新列（JSON 数组）优先，空则把旧单值列当单元素列表。
// 升级读侧用，与 modelList 是同一套回退规则。
func listOrSingle(list, single string) []string {
	if l := ParseStringList(list); len(l) > 0 {
		return l
	}
	if v := strings.TrimSpace(single); v != "" {
		return []string{v}
	}
	return nil
}

// botsOwnedBy 返回某人名下的 bot。主管理员传 mainAdmin 取全部。
func (s *Snapshot) BotsOwnedBy(uid int64, all bool) []*BotRec {
	out := make([]*BotRec, 0, len(s.Bots))
	for _, r := range s.Bots {
		if all || r.OwnerID == uid {
			out = append(out, r)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].BotID < out[j-1].BotID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

type Cache struct {
	store *Store
	cfg   atomic.Pointer[Snapshot]
}

func NewCache(s *Store) (*Cache, error) {
	c := &Cache{store: s}
	if err := c.Reload(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Cache) Snap() *Snapshot { return c.cfg.Load() }

// reload 重建整个配置快照并原子替换。任何配置变更后调用。
//
// 任何一段扫描都要查 rows.Err()：rows.Next() 因中途出错（如 WAL 写锁竞争）
// 提前返回 false 时不会自己报错，不查就会把「只扫到一半」悄悄当成「扫完了」，
// 而这里扫的是权限与生效群——静默少一半等于静默改权限。
func (c *Cache) Reload() error {
	snap := &Snapshot{
		Models:      map[string]*upstream.Model{},
		Settings:    map[string]string{},
		Bots:        map[int64]*BotRec{},
		BotTokens:   map[string]*BotRec{},
		BotChats:    map[int64]map[int64]BotChat{},
		BotSettings: map[int64]map[string]string{},
		Admins:      map[int64]AdminRec{},
		Gban:        map[int64]GbanRec{},
	}

	if err := c.loadModels(snap); err != nil {
		return err
	}
	if err := c.loadUpstreams(snap); err != nil {
		return err
	}
	if err := c.loadSettings(snap); err != nil {
		return err
	}
	if err := c.loadTenancy(snap); err != nil {
		return err
	}

	c.cfg.Store(snap)
	return nil
}

func (c *Cache) loadModels(snap *Snapshot) error {
	rows, err := c.store.Read.Query(`SELECT name,prompt_price,completion_price,
		cache_read_price,cache_write_price,enabled FROM models`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		m := &upstream.Model{}
		var en int64
		if err := rows.Scan(&m.Name, &m.PromptPrice, &m.CompletionPrice,
			&m.CacheReadPrice, &m.CacheWritePrice, &en); err != nil {
			return err
		}
		m.Enabled = en == 1
		snap.Models[m.Name] = m
	}
	return rows.Err()
}

func (c *Cache) loadUpstreams(snap *Snapshot) error {
	rows, err := c.store.Read.Query(`SELECT id,name,base_url,api_key,weight,status,
		supports_chat,supports_systemone FROM upstreams ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		u := &upstream.Upstream{}
		var sc, so int64
		if err := rows.Scan(&u.ID, &u.Name, &u.BaseURL, &u.APIKey, &u.Weight,
			&u.Status, &sc, &so); err != nil {
			return err
		}
		u.SupportsChat, u.SupportsSystemOne = sc == 1, so == 1
		snap.Upstreams = append(snap.Upstreams, u)
	}
	return rows.Err()
}

func (c *Cache) loadSettings(snap *Snapshot) error {
	// 先铺默认值，再用 DB 覆盖
	for k, v := range settingDefaults {
		snap.Settings[k] = v
	}
	rows, err := c.store.Read.Query(`SELECT k,v FROM settings`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		snap.Settings[k] = v
	}
	return rows.Err()
}

// loadTenancy 加载多租户那四张表：bots / bot_chats / bot_settings / admins，
// 外加联合封禁名单。
func (c *Cache) loadTenancy(snap *Snapshot) error {
	rows, err := c.store.Read.Query(`SELECT token,bot_id,username,owner_id,
		so_model,llm_model,enabled,created_at,is_main,so_models,llm_models
		FROM bots ORDER BY bot_id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		r := &BotRec{}
		var en, main int64
		var soModel, llmModel, soModels, llmModels string
		if err := rows.Scan(&r.Token, &r.BotID, &r.Username, &r.OwnerID,
			&soModel, &llmModel, &en, &r.CreatedAt, &main,
			&soModels, &llmModels); err != nil {
			rows.Close()
			return err
		}
		r.Enabled = en == 1
		r.IsMain = main == 1
		r.SoModels = listOrSingle(soModels, soModel)
		r.LLMModels = listOrSingle(llmModels, llmModel)
		snap.Bots[r.BotID] = r
		snap.BotTokens[r.Token] = r
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = c.store.Read.Query(`SELECT bot_id,chat_id,title,enabled,dryrun,
		group_alert,punish,created_at FROM bot_chats`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var bc BotChat
		var en, dry, ga int64
		if err := rows.Scan(&bc.BotID, &bc.ChatID, &bc.Title, &en, &dry, &ga,
			&bc.Punish, &bc.CreatedAt); err != nil {
			rows.Close()
			return err
		}
		bc.Enabled, bc.Dryrun, bc.GroupAlert = en == 1, dry == 1, ga == 1
		if snap.BotChats[bc.BotID] == nil {
			snap.BotChats[bc.BotID] = map[int64]BotChat{}
		}
		snap.BotChats[bc.BotID][bc.ChatID] = bc
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = c.store.Read.Query(`SELECT bot_id,k,v FROM bot_settings`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var botID int64
		var k, v string
		if err := rows.Scan(&botID, &k, &v); err != nil {
			rows.Close()
			return err
		}
		if snap.BotSettings[botID] == nil {
			snap.BotSettings[botID] = map[string]string{}
		}
		snap.BotSettings[botID][k] = v
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = c.store.Read.Query(`SELECT user_id,note,added_by,created_at FROM admins`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var a AdminRec
		if err := rows.Scan(&a.UserID, &a.Note, &a.AddedBy, &a.CreatedAt); err != nil {
			rows.Close()
			return err
		}
		snap.Admins[a.UserID] = a
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = c.store.Read.Query(
		`SELECT user_id,reason,src_chat,by_bot,created_at FROM gban`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var g GbanRec
		if err := rows.Scan(&g.UserID, &g.Reason, &g.SrcChat, &g.ByBot,
			&g.CreatedAt); err != nil {
			return err
		}
		snap.Gban[g.UserID] = g
	}
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = c.store.Read.Query(`SELECT bot_id,chat_id,user_id,expires_at,
		source,by_uid,created_at FROM ad_whitelist`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var w WhiteRec
		if err := rows.Scan(&w.BotID, &w.ChatID, &w.UserID, &w.ExpiresAt,
			&w.Source, &w.ByUID, &w.CreatedAt); err != nil {
			return err
		}
		snap.Whitelist = append(snap.Whitelist, w)
	}
	return rows.Err()
}

// Whitelisted 报告此人在这个范围内是否处于白名单中。
//
// 纯内存判断，放在查群管理员（要发 TG API）之前。范围匹配：
// 全平台（bot_id=0）或本 bot；该 bot 名下所有群（chat_id=0）或本群；
// 未到期（expires_at=0 为永久）。
func (s *Snapshot) Whitelisted(botID, chatID, uid, now int64) bool {
	for _, w := range s.Whitelist {
		if w.UserID != uid {
			continue
		}
		if w.BotID != 0 && w.BotID != botID {
			continue
		}
		if w.ChatID != 0 && w.ChatID != chatID {
			continue
		}
		if w.ExpiresAt != 0 && w.ExpiresAt <= now {
			continue
		}
		return true
	}
	return false
}
