package panel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"menshen/internal/antiad"
	"menshen/internal/billing"
	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/upstream"
)

// ---- Telegram Mini App：/miniapp ----
//
// 鉴权用 Telegram WebApp 的 initData：HMAC-SHA256 校验通过、且 24 小时内，
// 再看此人是不是服务管理员。读写走与面板同一套规则（主管理员全通，
// 次级管理员只碰自己名下的 bot），上游 / 模型 / 全局设置 / 名单只有主管理员能动。
//
// Mini App 只服务 Telegram；浏览器的网页版管理面板走 /admin（见 admin_panel.go），
// 用会话 cookie 鉴权，与这里的入口和鉴权路径分开。

const miniInitDataHeader = "X-Tg-Init-Data"
const miniBotIDHeader = "X-Bot-Id"

// MiniAppHandler 返回 /miniapp 的处理器；由 main 组合进 HTTP 服务。
//
// 路由边界（前端是 React 产物，由 go:embed 托管，见 miniapp_embed.go）：
//   - GET/HEAD  /miniapp           → 前端入口页；产物缺失时 503 构建提示
//   - GET/HEAD  /miniapp/assets/*  → 前端静态资源，仅真实存在的普通文件（目录/缺失 404，禁止列举）
//   - GET/HEAD  /miniapp/<其他>     → 产物根下的普通文件；否则 SPA 回退到入口页（无产物则 404）
//   - POST      /miniapp/api[/…]   → API；其余方法 405，不落入 SPA 回退
func MiniAppHandler(sh *core.Shared) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Path
		// 含 .. 的路径一律 404：path.Clean 会把 .. 解析到 assets 之外，
		// 从而经 SPA 回退拿到入口页。fs 层同样拒绝这类名字（fs.ValidPath）。
		if containsDotDot(raw) {
			http.NotFound(w, r)
			return
		}
		// path.Clean 归一重复斜杠、尾斜杠与 . 段，使等价路径落到同一路由。
		p := path.Clean(raw)
		switch {
		case p == "/miniapp":
			if !allowPageMethod(w, r) {
				return
			}
			miniAppIndex(w, r)
		case p == "/miniapp/api" || strings.HasPrefix(p, "/miniapp/api/"):
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			op := ""
			if strings.HasPrefix(p, "/miniapp/api/") {
				op = strings.TrimPrefix(p, "/miniapp/api/")
			}
			miniAPI(sh, w, r, op)
		case p == "/miniapp/assets" || strings.HasPrefix(p, "/miniapp/assets/"):
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			miniAppAsset(w, r, p)
		case strings.HasPrefix(p, "/miniapp/"):
			if !allowPageMethod(w, r) {
				return
			}
			// 产物根下的普通文件优先于 SPA 回退；文件名不带内容哈希，用
			// 短缓存。其余路径回退入口页（刷新/外链不白屏），没有产物就
			// 没有页面可回退。
			if miniAppDistFile(w, r, p, "public, max-age=3600") {
				return
			}
			if _, ok := miniAppDistFS(); !ok {
				http.NotFound(w, r)
				return
			}
			miniAppIndex(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

// containsDotDot 报告路径里是否有 .. 段（与 net/http 的同名判断一致）。
func containsDotDot(p string) bool {
	if !strings.Contains(p, "..") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// allowPageMethod 校验页面路由的方法（GET/HEAD），否则写 405 与 Allow 头。
func allowPageMethod(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET, HEAD")
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

// writeHTML 输出一段 HTML；HEAD 只写头不写 body（net/http 对直接 Write 的
// 响应不会替我们拦 body，httptest 里也看得到）。
func writeHTML(w http.ResponseWriter, r *http.Request, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		fmt.Fprint(w, body)
	}
}

// miniAppMissingHTML 是产物缺失时的占位页，文案直接给出构建方式。
const miniAppMissingHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>门神配置</title>
</head>
<body style="font:15px/1.7 -apple-system,BlinkMacSystemFont,sans-serif;max-width:34em;margin:12vh auto;padding:0 1.5em;color:#1a1a1a">
<h1 style="font-size:20px">前端未构建</h1>
<p>当前二进制没有内嵌 Mini App 前端产物。</p>
<p>本地开发：<code>npm --prefix web run build</code> 后重新 <code>go build</code>；<br>
部署构建：<code>docker build</code>（Dockerfile 会先构建前端再嵌入）。</p>
</body>
</html>`

// miniAppIndex 输出前端入口页；产物缺失时返回 503 与构建提示。
func miniAppIndex(w http.ResponseWriter, r *http.Request) {
	dist, ok := miniAppDistFS()
	if !ok {
		writeHTML(w, r, http.StatusServiceUnavailable, miniAppMissingHTML)
		return
	}
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		http.Error(w, "index.html 读取失败", http.StatusInternalServerError)
		return
	}
	writeHTML(w, r, http.StatusOK, string(index))
}

// miniAppDistFile 服务产物目录下的普通文件（assets/* 与根下文件共用）。
// p 是 path.Clean 后的路径；命中并已写出响应返回 true。目录与缺失一律
// 不处理（返回 false），由调用方决定 404 还是 SPA 回退。
func miniAppDistFile(w http.ResponseWriter, r *http.Request, p, cache string) bool {
	dist, ok := miniAppDistFS()
	if !ok {
		return false
	}
	name := strings.TrimPrefix(p, "/miniapp/")
	info, err := fs.Stat(dist, name)
	if err != nil || info.IsDir() {
		return false
	}
	w.Header().Set("Cache-Control", cache)
	http.StripPrefix("/miniapp/", http.FileServerFS(dist)).ServeHTTP(w, r)
	return true
}

// miniAppAsset 输出 /miniapp/assets/* 下的静态资源：只服务真实存在的普通
// 文件，目录（含 /miniapp/assets/）与缺失一律 404，不允许把产物目录列举
// 出去。文件名带内容哈希，可以长期强缓存。
func miniAppAsset(w http.ResponseWriter, r *http.Request, p string) {
	if !miniAppDistFile(w, r, p, "public, max-age=31536000, immutable") {
		http.NotFound(w, r)
	}
}

// validateMiniInitData 校验 Telegram WebApp 的 initData，并返回用户 id 与
// username（没有 username 时为空串，界面自行回退成 uid）。
//
// secret_key = HMAC-SHA256("WebAppData", bot_token)，
// hash = HMAC-SHA256(secret_key, data_check_string)。逐字节比较用常数时间。
func validateMiniInitData(token, initData string) (int64, string, error) {
	vals, err := url.ParseQuery(initData)
	if err != nil {
		return 0, "", fmt.Errorf("initData 无法解析")
	}
	hash := vals.Get("hash")
	if hash == "" {
		return 0, "", fmt.Errorf("initData 缺少 hash")
	}
	vals.Del("hash")
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+"="+vals.Get(k))
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(strings.Join(lines, "\n")))
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(hash)) {
		return 0, "", fmt.Errorf("initData 签名不匹配")
	}
	ad, _ := strconv.ParseInt(vals.Get("auth_date"), 10, 64)
	if ad == 0 || time.Now().Unix()-ad > 24*3600 {
		return 0, "", fmt.Errorf("initData 已过期，请重新打开")
	}
	var u struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	if json.Unmarshal([]byte(vals.Get("user")), &u) != nil || u.ID == 0 {
		return 0, "", fmt.Errorf("initData 缺少用户信息")
	}
	return u.ID, u.Username, nil
}

// miniAuth 校验 Telegram initData 并返回操作者（uid + username）与目标 bot；
// 失败时已写好响应。浏览器面板不走这里——见 admin_panel.go 的 adminAuth。
func miniAuth(sh *core.Shared, w http.ResponseWriter, r *http.Request) (uid int64, username string, botID int64, ok bool) {
	botID, _ = strconv.ParseInt(r.Header.Get(miniBotIDHeader), 10, 64)
	rec := sh.Cache.Snap().Bots[botID]
	if rec == nil {
		miniErr(w, http.StatusBadRequest, "未知的 bot")
		return 0, "", 0, false
	}
	uid, username, err := validateMiniInitData(rec.Token, r.Header.Get(miniInitDataHeader))
	if err != nil {
		miniErr(w, http.StatusUnauthorized, err.Error())
		return 0, "", 0, false
	}
	if !sh.IsStaff(uid) {
		miniErr(w, http.StatusForbidden, "你不是本服务的管理员")
		return 0, "", 0, false
	}
	return uid, username, botID, true
}

func miniErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

func miniOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

// specMeta 是下发给 Mini App 的设置项元信息（不含值）。
func specMeta(sp settingSpec) map[string]any {
	kind := "number"
	if sp.min == 0 && sp.max == 1 {
		kind = "toggle"
	}
	if stringSpecByKey(sp.key) != nil {
		kind = "text" // 字符串型（时区/附加链接/验证码密钥），前端用文本框
	}
	return map[string]any{
		"key": sp.key, "label": sp.label, "hint": sp.hint, "kind": kind,
		"min": sp.min, "max": sp.max, "group": sp.group,
	}
}

// miniAPI 是 /miniapp/api/* 的总分发：先按 initData 鉴权，再交给共用的 op 分发。
func miniAPI(sh *core.Shared, w http.ResponseWriter, r *http.Request, op string) {
	uid, username, _, ok := miniAuth(sh, w, r)
	if !ok {
		return
	}
	miniDispatch(sh, w, r, uid, username, op)
}

// miniDispatch 解析请求体并按 op 调用对应的操作函数；鉴权由调用方各自完成
// （Mini App 走 initData，网页版面板走会话 cookie），操作实现只此一份。
func miniDispatch(sh *core.Shared, w http.ResponseWriter, r *http.Request, uid int64, username, op string) {
	var body map[string]any
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&body); err != nil {
			miniErr(w, http.StatusBadRequest, "请求体无法解析")
			return
		}
	}
	if body == nil {
		body = map[string]any{}
	}

	switch op {
	case "state":
		miniState(sh, w, uid, username)
	case "set":
		miniSet(sh, w, uid, body)
	case "bot":
		miniBot(sh, w, uid, body)
	case "chat":
		miniChat(sh, w, uid, body)
	case "upstream":
		miniUpstream(sh, w, uid, body)
	case "model":
		miniModel(sh, w, uid, body)
	case "admin":
		miniAdmin(sh, w, uid, body)
	case "gban":
		miniGban(sh, w, uid, body)
	case "gbanown":
		miniGbanOwn(sh, w, uid, body)
	case "whitelist":
		miniWhitelist(sh, w, uid, body)
	case "digest":
		miniDigest(sh, w, uid, body)
	case "rules":
		miniRules(sh, w, uid, body)
	case "user":
		miniUser(sh, w, r, uid, body)
	case "logs":
		miniLogs(sh, w, uid, body)
	case "log":
		miniLogDetail(sh, w, uid, body)
	case "logact":
		miniLogact(sh, w, r, uid, body)
	case "syslog":
		miniSysLog(sh, w, uid, body)
	case "appeals":
		miniAppeals(sh, w, uid, body)
	case "appeal":
		miniAppealDetail(sh, w, uid, body)
	case "appealact":
		miniAppealact(sh, w, uid, body)
	default:
		miniErr(w, http.StatusNotFound, "未知操作")
	}
}

func miniInt(body map[string]any, key string) int64 {
	switch v := body[key].(type) {
	case float64:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}

func miniStr(body map[string]any, key string) string {
	s, _ := body[key].(string)
	return strings.TrimSpace(s)
}

func miniBool(body map[string]any, key string) bool {
	b, _ := body[key].(bool)
	return b
}

// ---- 状态 ----

func miniState(sh *core.Shared, w http.ResponseWriter, uid int64, username string) {
	snap := sh.Cache.Snap()
	main := sh.IsMain(uid)

	// username 来自 initData 签名，可能为空（用户没设 @username）；界面
	// 优先显示 @username，没有才回退 uid。
	me := map[string]any{"uid": uid, "main": main, "username": username}
	bots := []map[string]any{}
	chats := []map[string]any{}
	botSettings := map[string]map[string]string{}
	for _, rec := range snap.BotsOwnedBy(uid, main) {
		bots = append(bots, map[string]any{
			"bot_id": rec.BotID, "username": rec.Username, "label": rec.Label(),
			"owner_id": rec.OwnerID, "enabled": rec.Enabled, "is_main": rec.IsMain,
			"so_models": rec.SoModels, "llm_models": rec.LLMModels,
			"live": sh.Reg != nil && miniBotLive(sh, rec.BotID),
		})
		for _, c := range snap.ChatsOf(rec.BotID) {
			chats = append(chats, map[string]any{
				"bot_id": rec.BotID, "chat_id": c.ChatID, "title": c.Title,
				"enabled": c.Enabled, "dryrun": c.Dryrun,
				"group_alert": c.GroupAlert, "punish": c.Punish,
			})
		}
		botSettings[strconv.FormatInt(rec.BotID, 10)] = snap.BotSettings[rec.BotID]
	}

	specs := make([]map[string]any, 0, len(settingSpecs))
	for _, sp := range settingSpecs {
		specs = append(specs, specMeta(sp))
	}
	// sections 是按主题分好组的同一批设置项：全局设置页与 bot 页共用它，
	// 同类选项排在一起，两个页面不会各有一个顺序。
	sections := make([]map[string]any, 0, len(settingSections))
	for _, g := range specsInSections(nil) {
		items := make([]map[string]any, 0, len(g.Specs))
		for _, sp := range g.Specs {
			items = append(items, specMeta(sp))
		}
		sections = append(sections, map[string]any{"name": g.Name, "specs": items})
	}

	stats := miniStats(sh, uid, main)

	// global_defaults 是 antiad/both 组设置与模型列表键的全局默认值，
	// 下发给所有管理员：机器人页需要它在输入框占位中显示全局值，
	// 而次级管理员看不到完整全局设置（global），无法从那里取。
	defaults := map[string]any{}
	for _, sp := range settingSpecs {
		if sp.group == "antiad" || sp.group == "both" {
			defaults[sp.key] = snap.Setting(sp.key)
		}
	}
	defaults["antiad_so_models"] = snap.Setting("antiad_so_models")
	defaults["antiad_llm_models"] = snap.Setting("antiad_llm_models")
	defaults["antiad_rule_model"] = snap.Setting("antiad_rule_model")

	out := map[string]any{
		"me": me, "bots": bots, "chats": chats, "bot_settings": botSettings,
		"specs": specs, "sections": sections, "stats": stats,
		"todo":            miniTodo(sh, uid, main),
		"global_defaults": defaults,
		// tz_name 是展示时区（IANA）：次级管理员没有 global（那是主管理员的
		// 设置全量），所以单独给所有管理员下发一份，前端全站时间格式化用它。
		"tz_name": snap.Setting("tz_name"),
	}
	if main {
		// global 会发给浏览器：密钥类设置（web_secret / captcha_secret）
		// 不能原样下发，拷贝一份后删除；是否已设置由 settings_set 反映。
		g := make(map[string]string, len(snap.Settings))
		for k, v := range snap.Settings {
			g[k] = v
		}
		delete(g, "web_secret")
		delete(g, "captcha_secret")
		out["global"] = g
		out["digest"] = snap.Setting("antiad_digest")
		out["digest_fix"] = snap.Setting("antiad_digest_fix")
		// settings_set 是显式写过的设置键（settings 表里有行的键）。
		// snap.Settings 里铺了代码默认值，不能用来判断 `已设置`；设置页的
		// `已设置 N 项` 只数这个集合。
		out["settings_set"] = miniSettingsSet(sh)
	}
	out["whitelist"] = miniWhitelistRows(sh, uid, main)
	out["profile_ok"] = miniProfileOKRows(sh, uid, main)

	// 全局联合封禁组对所有管理员开放（共同维护的名单），专属组按人给。
	gbans := []map[string]any{}
	for _, g := range snap.Gban {
		gbans = append(gbans, map[string]any{
			"user_id": g.UserID, "reason": g.Reason,
			"src_chat": g.SrcChat, "created_at": g.CreatedAt})
	}
	out["gban"] = gbans
	ownBans := []map[string]any{}
	for _, g := range snap.GbanOwnBans[uid] {
		ownBans = append(ownBans, map[string]any{
			"user_id": g.UserID, "reason": g.Reason,
			"src_chat": g.SrcChat, "created_at": g.CreatedAt})
	}
	ownChats := []int64{}
	for chatID := range snap.GbanOwnChats[uid] {
		ownChats = append(ownChats, chatID)
	}
	out["gban_own"] = map[string]any{
		"enabled": snap.GbanOwnOn(uid), "chats": ownChats, "bans": ownBans}

	if main {
		ups := []map[string]any{}
		for _, u := range snap.Upstreams {
			ups = append(ups, map[string]any{
				"id": u.ID, "name": u.Name, "base_url": u.BaseURL,
				"api_key": upstream.MaskKey(u.APIKey), "weight": u.Weight,
				"status": u.Status, "kind": string(u.EffectiveKind()),
				"supports_chat":      u.SupportsChat,
				"supports_systemone": u.SupportsSystemOne,
			})
		}
		out["upstreams"] = ups

		models := []map[string]any{}
		for name, m := range snap.Models {
			models = append(models, map[string]any{
				"name": name, "enabled": m.Enabled,
				"prompt_price": m.PromptPrice, "completion_price": m.CompletionPrice,
				"cache_read_price": m.CacheReadPrice, "cache_write_price": m.CacheWritePrice,
				"upstream": m.UpstreamName(), "model_id": m.ModelID(),
			})
		}
		sort.Slice(models, func(i, j int) bool {
			return models[i]["name"].(string) < models[j]["name"].(string)
		})
		out["models"] = models

		admins := []map[string]any{}
		for _, a := range snap.Admins {
			admins = append(admins, map[string]any{
				"user_id": a.UserID, "note": a.Note})
		}
		out["admins"] = admins

		// 归属候选：主管理员（配置）与次级管理员（库里），去重。
		// 给前端的改派下拉用；次级管理员没有改派权，不下发。
		opts := []map[string]any{}
		seen := map[int64]bool{}
		for _, id := range sh.Cfg.AdminIDs {
			opts = append(opts, map[string]any{"user_id": id, "label": "主管理员"})
			seen[id] = true
		}
		for _, a := range snap.Admins {
			if seen[a.UserID] {
				continue
			}
			label := "次级管理员"
			if a.Note != "" {
				label += " · " + a.Note
			}
			opts = append(opts, map[string]any{"user_id": a.UserID, "label": label})
		}
		out["owner_opts"] = opts
	}
	miniOK(w, out)
}

// miniSettingsSet 返回显式写过的设置键（settings 表里有行的 k）。代码默认值
// 只铺在快照内存里、不会进表——设置页 `已设置 N 项` 要的正是这个区别。
func miniSettingsSet(sh *core.Shared) []string {
	keys := []string{}
	rows, err := sh.Store.Read.Query(`SELECT k FROM settings`)
	if err != nil {
		slog.Error("miniapp：读取已设置键失败", "err", err)
		return keys
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			slog.Error("miniapp：读取已设置键失败", "err", err)
			return keys
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		slog.Error("miniapp：读取已设置键失败", "err", err)
	}
	return keys
}

func miniBotLive(sh *core.Shared, botID int64) bool {
	if sh.Reg == nil {
		return false
	}
	_, live := sh.Reg.LookupID(botID)
	return live
}

func miniStats(sh *core.Shared, uid int64, main bool) map[string]any {
	since := time.Now().Unix() - 86400
	where, args := miniBotsClause(sh, uid, main)
	q := append([]any{since}, args...)
	var checked, hits, cost int64
	sh.Store.Read.QueryRow(`SELECT COALESCE(SUM(CASE WHEN verdict!='skipped' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN verdict='ad' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(quota_cost),0) FROM antiad_log
		WHERE created_at >= ?`+where, q...).Scan(&checked, &hits, &cost)
	var chats int
	for _, r := range sh.Cache.Snap().BotsOwnedBy(uid, main) {
		for _, c := range sh.Cache.Snap().ChatsOf(r.BotID) {
			if c.Enabled {
				chats++
			}
		}
	}
	return map[string]any{
		"checked": checked, "hits": hits, "cost": cost,
		"cost_text": billing.FormatUSDFine(cost), "chats": chats,
	}
}

// miniTodo 汇总工作台的待办：可见范围内未结的申诉单数、演练中的群数与
// 停用的 bot 数。未结申诉与 miniAppeals 用同一套权限 clause 与状态集合，
// 两处的数字必须一致；演练群与停用 bot 直接数快照，与群/机器人列表同口径。
func miniTodo(sh *core.Shared, uid int64, main bool) map[string]any {
	where, args := miniBotsClause(sh, uid, main)
	var openAppeals int64
	if err := sh.Store.Read.QueryRow(`SELECT COUNT(*) FROM appeals
		WHERE 1=1`+where+` AND status IN (`+store.AppealOpenStatusesSQL+`)`,
		args...).Scan(&openAppeals); err != nil {
		slog.Error("miniapp：统计未结申诉失败", "err", err)
	}
	snap := sh.Cache.Snap()
	dryrun, disabled := 0, 0
	for _, rec := range snap.BotsOwnedBy(uid, main) {
		if !rec.Enabled {
			disabled++
		}
		for _, c := range snap.ChatsOf(rec.BotID) {
			if c.Dryrun {
				dryrun++
			}
		}
	}
	return map[string]any{
		"open_appeals": openAppeals, "dryrun_chats": dryrun,
		"disabled_bots": disabled,
	}
}

// miniBotsClause 返回按权限过滤 antiad_log 的 SQL 片段与参数。
func miniBotsClause(sh *core.Shared, uid int64, main bool) (string, []any) {
	return botsClause(sh, uid, main)
}

func miniWhitelistRows(sh *core.Shared, uid int64, main bool) []map[string]any {
	out := []map[string]any{}
	for _, w := range sh.Cache.Snap().Whitelist {
		if !main && w.BotID != 0 && !sh.CanManageBot(uid, w.BotID) {
			continue
		}
		out = append(out, map[string]any{
			"bot_id": w.BotID, "chat_id": w.ChatID, "user_id": w.UserID,
			"expires_at": w.ExpiresAt, "source": w.Source, "by_uid": w.ByUID,
		})
	}
	return out
}

// miniProfileOKRows 是资料临时放行清单（见 antiad.GrantProfileOK）。
// 与白名单同一套可见性：谁能管那台 bot 就看得到它的行。
func miniProfileOKRows(sh *core.Shared, uid int64, main bool) []map[string]any {
	now := time.Now().Unix()
	out := []map[string]any{}
	for _, p := range sh.Cache.Snap().ProfileOK {
		if !main && !sh.CanManageBot(uid, p.BotID) {
			continue
		}
		if p.ExpiresAt != 0 && p.ExpiresAt <= now {
			continue
		}
		out = append(out, map[string]any{
			"bot_id": p.BotID, "user_id": p.UserID, "hours": p.Hours,
			"reason": p.Reason, "created_at": p.CreatedAt, "expires_at": p.ExpiresAt,
		})
	}
	return out
}

// ---- 写操作 ----

// miniSet 改设置：scope=global（仅主管理员）或 bot（按 spec 分组校验）。
func miniSet(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	// 规则与 TG 面板同一份（见 ops_settings.go）。
	if err := setSetting(sh, uid, miniInt(body, "bot_id"),
		miniStr(body, "key"), miniStr(body, "value")); err != nil {
		miniFail(w, err, "保存失败")
		return
	}
	miniOK(w, map[string]any{"ok": true})
}

func miniBot(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	botID := miniInt(body, "bot_id")
	if !sh.CanManageBot(uid, botID) {
		miniErr(w, http.StatusForbidden, "无权管理该 bot")
		return
	}
	action := miniStr(body, "action")
	switch action {
	case "enable", "disable":
		if sh.Reg == nil {
			miniErr(w, http.StatusInternalServerError, "注册表不可用")
			return
		}
		if err := sh.Reg.SetBotEnabled(botID, action == "enable"); err != nil {
			miniErr(w, http.StatusBadRequest, err.Error())
			return
		}
	case "remove":
		if sh.Reg == nil {
			miniErr(w, http.StatusInternalServerError, "注册表不可用")
			return
		}
		// 主 bot 的拦截在 Unregister 里，这里不重复判断：无权限者只会看到
		// 无法移除，不应从文案差异中推断目标是否为主 bot。
		if err := sh.Reg.Unregister(botID); err != nil {
			miniErr(w, http.StatusBadRequest, err.Error())
			return
		}
	case "owner":
		if !sh.IsMain(uid) {
			miniErr(w, http.StatusForbidden, "只有主管理员能改归属")
			return
		}
		rec := sh.Cache.Snap().Bots[botID]
		if rec == nil {
			miniErr(w, http.StatusBadRequest, "该 bot 不存在")
			return
		}
		if rec.IsMain {
			miniErr(w, http.StatusBadRequest, "主 bot 的归属由配置文件决定，不能改派")
			return
		}
		ownerID := miniInt(body, "owner_id")
		// 归属人必须是管理员：改派给一个普通用户，等于把这个 bot 变成
		// 谁都管不了的黑盒，而它在群里的一切行为还在继续花钱。
		if !sh.IsStaff(ownerID) {
			miniErr(w, http.StatusBadRequest, "归属人必须是主管理员或次级管理员")
			return
		}
		if err := sh.SetBotOwner(botID, ownerID); err != nil {
			miniErr(w, http.StatusInternalServerError, "保存失败")
			return
		}
	case "models":
		if _, err := setBotModels(sh, uid, botID, miniStr(body, "which"),
			miniStr(body, "value")); err != nil {
			miniFail(w, err, "保存失败")
			return
		}
	default:
		miniErr(w, http.StatusBadRequest, "未知操作")
		return
	}
	miniOK(w, map[string]any{"ok": true})
}

// miniChat 增改 / 删除某个群配置。
func miniChat(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	botID := miniInt(body, "bot_id")
	chatID := miniInt(body, "chat_id")
	action := miniStr(body, "action")
	if action == "bulk_update" {
		// 批量更新一次只碰一个 bot 的群：bot_id 必填且先于权限检查校验，
		// 否则主管理员缺 bot_id 时会被当成 0 号 bot 一路放行到 `未知操作`。
		if botID == 0 {
			miniErr(w, http.StatusBadRequest, "缺少 bot_id")
			return
		}
		if !sh.CanManageBot(uid, botID) {
			miniErr(w, http.StatusForbidden, "无权管理该 bot")
			return
		}
		miniChatBulk(sh, w, botID, body)
		return
	}
	if !sh.CanManageBot(uid, botID) {
		miniErr(w, http.StatusForbidden, "无权管理该 bot")
		return
	}
	if action == "backfill" {
		// 手动补全历史成员入群时间：与自动触发同一条路径，但绕过 24 小时冷却。
		if chatID == 0 {
			miniErr(w, http.StatusBadRequest, "chat_id 无效")
			return
		}
		inst, live := lookupBot(sh, botID)
		if !live {
			miniErr(w, http.StatusBadRequest, "该 bot 未在运行，无法补全")
			return
		}
		title := chatTitle(sh, botID, chatID)
		started, why := antiad.StartJoinBackfill(inst, chatID, title, true)
		if !started {
			if why == "" {
				miniErr(w, http.StatusBadRequest, "补全任务已在运行，稍等结果")
				return
			}
			miniErr(w, http.StatusBadRequest, why)
			return
		}
		miniOK(w, map[string]any{"ok": true,
			"note": "已开始补全，结果会私聊发给管理员（大群可能要几分钟）"})
		return
	}
	switch action {
	case "add", "update":
		if _, ok := sh.Cache.Snap().ChatConf(botID, chatID); !ok {
			if err := sh.AddChat(botID, chatID, chatTitle(sh, botID, chatID)); err != nil {
				miniFail(w, err, "添加失败")
				return
			}
		}
		// 其余字段按请求里出现的项更新；字段规则与批量更新共用一处。
		if _, err := sh.UpdateChats(botID, []int64{chatID}, miniChatPatch(body)); err != nil {
			miniFail(w, err, "保存失败")
			return
		}
	case "remove":
		if err := sh.RemoveChat(botID, chatID); err != nil {
			miniFail(w, err, "删除失败")
			return
		}
	default:
		miniErr(w, http.StatusBadRequest, "未知操作")
		return
	}
	miniOK(w, map[string]any{"ok": true})
}

// miniChatPatch 收集 fields 里出现的群配置字段；没出现的键一律不动
// （enabled:false 是合法值，不能用零值当哨兵）。取值校验在 UpdateChats。
func miniChatPatch(fields map[string]any) core.ChatPatch {
	flag := func(k string) *bool {
		v, ok := fields[k]
		if !ok {
			return nil
		}
		on := false
		switch t := v.(type) {
		case bool:
			on = t
		case float64:
			on = t != 0
		}
		return &on
	}
	p := core.ChatPatch{Enabled: flag("enabled"), Dryrun: flag("dryrun"),
		GroupAlert: flag("group_alert")}
	if v, ok := fields["title"]; ok {
		p.Title = ptr(fmt.Sprint(v))
	}
	if v, ok := fields["punish"]; ok {
		p.Punish = ptr(miniInt(map[string]any{"v": v}, "v"))
	}
	return p
}

// miniChatBulk 批量更新一个 bot 名下的群配置（chat op 的 bulk_update）。
// 只更新该 bot 名下真实存在的群（WHERE bot_id=? AND chat_id=?），不存在的
// chat_id 跳过；fields 只认 enabled/dryrun/group_alert/punish，未出现的键不改。
func miniChatBulk(sh *core.Shared, w http.ResponseWriter, botID int64, body map[string]any) {
	ids, err := miniChatIDList(body)
	if err != nil {
		miniErr(w, http.StatusBadRequest, err.Error())
		return
	}
	fields := map[string]any{}
	if raw, ok := body["fields"]; ok {
		m, isMap := raw.(map[string]any)
		if !isMap {
			miniErr(w, http.StatusBadRequest, "fields 必须是对象")
			return
		}
		for _, f := range []string{"enabled", "dryrun", "group_alert", "punish"} {
			if v, exists := m[f]; exists {
				fields[f] = v
			}
		}
	}
	updated, err := sh.UpdateChats(botID, ids, miniChatPatch(fields))
	if err != nil {
		miniFail(w, err, "保存失败")
		return
	}
	miniOK(w, map[string]any{"ok": true,
		"note": fmt.Sprintf("已更新 %d 个群", updated)})
}

// miniChatIDList 解析 bulk_update 的 chat_ids：必须是非空数组、最多 100 个；
// 元素容错 JSON 解码后的 float64 与直接构造的 int64（以及 json.Number）。
func miniChatIDList(body map[string]any) ([]int64, error) {
	raw, ok := body["chat_ids"]
	if !ok {
		return nil, fmt.Errorf("缺少 chat_ids")
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("chat_ids 必须是数组")
	}
	if len(arr) == 0 {
		return nil, fmt.Errorf("chat_ids 不能为空")
	}
	if len(arr) > 100 {
		return nil, fmt.Errorf("一次最多更新 100 个群")
	}
	out := make([]int64, 0, len(arr))
	for _, e := range arr {
		switch n := e.(type) {
		case float64:
			out = append(out, int64(n))
		case int64:
			out = append(out, n)
		case int:
			out = append(out, int64(n))
		case json.Number:
			i, err := n.Int64()
			if err != nil {
				return nil, fmt.Errorf("chat_ids 里有非数字项")
			}
			out = append(out, i)
		default:
			return nil, fmt.Errorf("chat_ids 里有非数字项")
		}
	}
	return out, nil
}

func miniUpstream(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	if !sh.IsMain(uid) {
		miniErr(w, http.StatusForbidden, "只有主管理员能配置上游")
		return
	}
	action := miniStr(body, "action")
	switch action {
	case "add":
		p, err := miniUpstreamPatch(body)
		if err != nil {
			miniErr(w, http.StatusBadRequest, err.Error())
			return
		}
		p.Name, p.BaseURL = ptr(miniStr(body, "name")), ptr(miniStr(body, "base_url"))
		p.APIKey = ptr(miniStr(body, "api_key"))
		p.Enabled = ptr(!miniBool(body, "disabled"))
		if err := sh.AddUpstream(p); err != nil {
			miniFail(w, err, "添加失败")
			return
		}
	case "update":
		p, err := miniUpstreamPatch(body)
		if err != nil {
			miniErr(w, http.StatusBadRequest, err.Error())
			return
		}
		// 空串 = 不改：前端编辑页 api_key 留空表示保留原密钥。
		for _, f := range []struct {
			key string
			dst **string
		}{{"name", &p.Name}, {"base_url", &p.BaseURL}, {"api_key", &p.APIKey}} {
			if v := miniStr(body, f.key); v != "" {
				*f.dst = &v
			}
		}
		if _, ok := body["status"]; ok {
			p.Enabled = ptr(miniBool(body, "status"))
		}
		if err := sh.UpdateUpstream(miniInt(body, "id"), p); err != nil {
			miniFail(w, err, "保存失败")
			return
		}
	case "remove":
		if err := sh.DeleteUpstream(miniInt(body, "id")); err != nil {
			miniFail(w, err, "删除失败")
			return
		}
	case "test":
		// 连通性测试不改库，不走下面的 Cache.Reload / {"ok":true} 收尾。
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		model, latency, err := antiad.TestUpstream(ctx, sh, miniInt(body, "id"))
		if err != nil {
			if errors.Is(err, antiad.ErrUpstreamNotFound) ||
				errors.Is(err, antiad.ErrUpstreamNoModel) ||
				errors.Is(err, antiad.ErrUpstreamNoChat) {
				miniErr(w, http.StatusBadRequest, err.Error())
				return
			}
			// 请求本身失败（网络、鉴权、上游 5xx）：HTTP 200 + ok:false，
			// 前端行内展示原因，不当成接口错误处理。
			miniOK(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		miniOK(w, map[string]any{
			"ok": true, "latency_ms": latency.Milliseconds(), "model": model})
		return
	default:
		miniErr(w, http.StatusBadRequest, "未知操作")
		return
	}
	miniOK(w, map[string]any{"ok": true})
}

// miniUpstreamPatch 取 add / update 共有的可选字段：权重、渠道类型、能力
// 开关，只收请求里出现的键。取值校验在 core。
func miniUpstreamPatch(body map[string]any) (core.UpstreamPatch, error) {
	var p core.UpstreamPatch
	if _, ok := body["weight"]; ok {
		p.Weight = ptr(miniInt(body, "weight"))
	}
	if raw := miniStr(body, "kind"); raw != "" {
		k, err := upstream.ParseKind(raw)
		if err != nil {
			return p, err
		}
		p.Kind = &k
	}
	if _, ok := body["supports_chat"]; ok {
		p.Chat = ptr(miniBool(body, "supports_chat"))
	}
	if _, ok := body["supports_systemone"]; ok {
		p.SystemOne = ptr(miniBool(body, "supports_systemone"))
	}
	return p, nil
}

func miniModel(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	if !sh.IsMain(uid) {
		miniErr(w, http.StatusForbidden, "只有主管理员能配置模型")
		return
	}
	name := miniStr(body, "name")
	var err error
	switch miniStr(body, "action") {
	case "add":
		var prices [4]float64
		for i, col := range core.ModelPriceCols {
			if prices[i], err = miniPrice(body, col); err != nil {
				miniErr(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		_, err = sh.AddModel(miniStr(body, "upstream"), miniStr(body, "model_id"), prices)
	case "remove":
		err = sh.DeleteModel(name)
	case "update":
		var p core.ModelPatch
		for i, col := range core.ModelPriceCols {
			if _, ok := body[col]; !ok {
				continue
			}
			v, perr := miniPrice(body, col)
			if perr != nil {
				miniErr(w, http.StatusBadRequest, perr.Error())
				return
			}
			p.Prices[i] = &v
		}
		if _, ok := body["enabled"]; ok {
			p.Enabled = ptr(miniBool(body, "enabled"))
		}
		err = sh.UpdateModel(name, p)
	default:
		miniErr(w, http.StatusBadRequest, "未知操作")
		return
	}
	if err != nil {
		miniFail(w, err, "保存失败")
		return
	}
	miniOK(w, map[string]any{"ok": true})
}

func miniAdmin(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	if !sh.IsMain(uid) {
		miniErr(w, http.StatusForbidden, "只有主管理员能管理管理员")
		return
	}
	target := miniInt(body, "user_id")
	switch miniStr(body, "action") {
	case "add":
		if target <= 0 {
			miniErr(w, http.StatusBadRequest, "user_id 无效")
			return
		}
		if err := sh.AddAdmin(target, miniStr(body, "note"), uid); err != nil {
			miniErr(w, http.StatusBadRequest, err.Error())
			return
		}
	case "remove":
		if err := sh.RemoveAdmin(target); err != nil {
			miniErr(w, http.StatusInternalServerError, "删除失败")
			return
		}
	default:
		miniErr(w, http.StatusBadRequest, "未知操作")
		return
	}
	miniOK(w, map[string]any{"ok": true})
}

func miniGban(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	// 全局组对所有管理员开放：共同维护的名单，每人都能加人/移人
	// （自动命中仍走判定链路的门槛）。
	target := miniInt(body, "user_id")
	switch miniStr(body, "action") {
	case "add":
		if target <= 0 {
			miniErr(w, http.StatusBadRequest, "user_id 无效")
			return
		}
		reason := miniStr(body, "reason")
		if reason == "" {
			reason = "管理员手工加入"
		}
		if err := antiad.GbanAdd(sh, target, reason, 0, 0); err != nil {
			miniErr(w, http.StatusInternalServerError, "添加失败")
			return
		}
		// 手工加入与判定命中同待遇：落名单即在全局组覆盖范围内执行。
		go antiad.EnforceGban(sh, target, reason)
	case "remove":
		note := "该用户不在你能解除的名单里"
		if s := antiad.AdminLiftGban(sh, uid, target); s != "" {
			note = "已解除：" + s
		}
		miniOK(w, map[string]any{"ok": true, "note": note})
		return
	default:
		miniErr(w, http.StatusBadRequest, "未知操作")
		return
	}
	miniOK(w, map[string]any{"ok": true})
}

// miniGbanOwn 管理请求者自己的专属联合封禁组：开关、圈定生效群、
// 名单增删。
func miniGbanOwn(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	target := miniInt(body, "user_id")
	switch miniStr(body, "action") {
	case "enable":
		if err := antiad.GbanOwnSetEnabled(sh, uid, miniBool(body, "on")); err != nil {
			miniErr(w, http.StatusInternalServerError, "保存失败")
			return
		}
	case "chat":
		chatID := miniInt(body, "chat_id")
		if !ownChat(sh, uid, chatID) {
			miniErr(w, http.StatusBadRequest, "只能圈定自己名下 bot 覆盖的群")
			return
		}
		if err := antiad.GbanOwnSetChat(sh, uid, chatID, miniBool(body, "on")); err != nil {
			miniErr(w, http.StatusInternalServerError, "保存失败")
			return
		}
	case "add":
		if target == 0 {
			miniErr(w, http.StatusBadRequest, "user_id 无效")
			return
		}
		reason := miniStr(body, "reason")
		if reason == "" {
			reason = "管理员手工加入"
		}
		if err := antiad.GbanOwnAddBan(sh, uid, target, reason, 0); err != nil {
			miniErr(w, http.StatusInternalServerError, "添加失败")
			return
		}
		go antiad.EnforceGbanOwn(sh, uid, target)
	case "remove":
		if err := antiad.GbanOwnRemoveBan(sh, uid, target); err != nil {
			miniErr(w, http.StatusInternalServerError, "移除失败")
			return
		}
	default:
		miniErr(w, http.StatusBadRequest, "未知操作")
		return
	}
	miniOK(w, map[string]any{"ok": true})
}

// ownChat 报告某个群是否由请求者名下的 bot 覆盖：专属组的生效群
// 只能从这里圈。
func ownChat(sh *core.Shared, uid, chatID int64) bool {
	snap := sh.Cache.Snap()
	for _, rec := range snap.BotsOwnedBy(uid, false) {
		for _, c := range snap.ChatsOf(rec.BotID) {
			if c.ChatID == chatID {
				return true
			}
		}
	}
	return false
}

func miniWhitelist(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	botID := miniInt(body, "bot_id")
	chatID := miniInt(body, "chat_id")
	target := miniInt(body, "user_id")
	if botID != 0 && !sh.CanManageBot(uid, botID) {
		miniErr(w, http.StatusForbidden, "无权管理该 bot")
		return
	}
	if botID == 0 && !sh.IsMain(uid) {
		miniErr(w, http.StatusForbidden, "全平台白名单只有主管理员能写")
		return
	}
	switch miniStr(body, "action") {
	case "add":
		if target == 0 {
			miniErr(w, http.StatusBadRequest, "user_id 无效")
			return
		}
		hours := miniInt(body, "hours")
		var ttl time.Duration
		if hours > 0 {
			ttl = time.Duration(hours) * time.Hour
		}
		if err := antiad.AddWhitelist(sh, botID, chatID, target, ttl, "adw", uid); err != nil {
			miniErr(w, http.StatusInternalServerError, "写入失败")
			return
		}
	case "remove":
		if err := antiad.RemoveWhitelist(sh, botID, chatID, target); err != nil {
			miniFail(w, err, "删除失败")
			return
		}
	case "unprofile":
		// 撤销资料放行（复判给的临时放行，见 antiad.GrantProfileOK）。
		// 与白名单分开：它只免掉资料这一路，不是整号放行。
		if botID == 0 || target == 0 {
			miniErr(w, http.StatusBadRequest, "缺少 bot_id 或 user_id")
			return
		}
		if err := antiad.RevokeProfileOK(sh, botID, target, "管理员撤销"); err != nil {
			miniFail(w, err, "撤销失败")
			return
		}
	default:
		miniErr(w, http.StatusBadRequest, "未知操作")
		return
	}
	miniOK(w, map[string]any{"ok": true})
}

func miniDigest(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	var err error
	switch miniStr(body, "action") {
	case "save":
		err = setDigest(sh, uid, false, miniStr(body, "value"))
	case "save_fix":
		// 修正文本是写给总结模型的口径说明，与摘要正文分开存：摘要是
		// 模型写的、可以手工改，修正文本是人写的、每轮总结都会附上。
		err = setDigest(sh, uid, true, miniStr(body, "value"))
	case "run":
		if !sh.IsMain(uid) {
			err = core.Denied("形态摘要由主管理员维护")
			break
		}
		go antiad.RunAdDigest(sh, true)
	default:
		miniErr(w, http.StatusBadRequest, "未知操作")
		return
	}
	if err != nil {
		miniFail(w, err, "保存失败")
		return
	}
	miniOK(w, map[string]any{"ok": true})
}

// ---- AI 必封规则（主管理员专属）----

// miniRules 维护 AI 从历史封禁里总结出的必封正则规则。
//
// 规则默认候选、不启用；enforce=1 命中即最高档处置（零 AI 成本），
// enforce=0 命中只作为证据进 prompt。防误封由服务端强制：保存先跑全库
// 测试，enforce 只允许在 last_fp=0 且 last_undone=0 时打开。
func miniRules(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	if !sh.IsMain(uid) {
		miniErr(w, http.StatusForbidden, "只有主管理员能维护必封规则")
		return
	}
	id := miniInt(body, "id")
	switch miniStr(body, "action") {
	case "list":
		rules, err := antiad.ListRules(sh)
		if err != nil {
			slog.Error("miniapp：读取必封规则失败", "err", err)
			miniErr(w, http.StatusInternalServerError, "读取失败")
			return
		}
		miniOK(w, map[string]any{"rules": rules})
	case "test":
		// 带 id 用库里的规则（并写回 last_*），只带 pattern 时试跑一条
		// 不落库的草稿（草稿不要求过严格校验，人工试跑就是它的用途）。
		var res antiad.RuleTestResult
		var err error
		if id != 0 {
			res, err = antiad.TestRuleByID(sh, id)
		} else if pattern := miniStr(body, "pattern"); pattern == "" {
			err = core.Bad("缺少规则正则")
		} else if res, err = antiad.TestRulePattern(sh, pattern); err != nil {
			err = core.Bad("测试失败：%s", err.Error())
		}
		if err != nil {
			miniFail(w, err, "测试失败")
			return
		}
		miniOK(w, map[string]any{"test": miniRuleTestJSON(res)})
	case "save":
		id, res, err := antiad.SaveRule(sh, uid, id, antiad.RuleInput{
			Name: miniStr(body, "name"), Pattern: miniStr(body, "pattern"),
			Category: miniStr(body, "category"), Note: miniStr(body, "note")})
		if err != nil {
			miniFail(w, err, "保存失败")
			return
		}
		miniOK(w, map[string]any{"ok": true, "id": id, "test": miniRuleTestJSON(res)})
	case "toggle", "enforce", "remove":
		var err error
		switch miniStr(body, "action") {
		case "toggle":
			err = antiad.SetRuleEnabled(sh, id, miniBool(body, "enabled"))
		case "enforce":
			err = antiad.SetRuleEnforce(sh, id, miniBool(body, "enforce"))
		default:
			err = antiad.DeleteRule(sh, id)
		}
		if err != nil {
			miniFail(w, err, "保存失败")
			return
		}
		miniOK(w, map[string]any{"ok": true})
	case "agent_start":
		// 启动一轮 AI 规则发现（单飞、后台跑）。失败原因（没有 OpenAI
		// 兼容上游、已在运行、指定记录不合法等）直接回给前端。
		// 带 record_id 时进入指定记录模式：围绕该条已确认广告写规则，
		// 工具不受限，仍可搜索同类形态。
		var err error
		if id := miniInt(body, "record_id"); id > 0 {
			err = antiad.StartRuleDiscoveryForRecord(sh, uid, id)
		} else {
			err = antiad.StartRuleDiscovery(sh, uid)
		}
		if err != nil {
			miniErr(w, http.StatusBadRequest, err.Error())
			return
		}
		miniOK(w, map[string]any{"ok": true, "agent": antiad.RuleAgentStatus(sh)})
	case "agent_status":
		// 返回的 agent 对象形状固定：前端按它轮询运行态与步骤日志。
		miniOK(w, map[string]any{"agent": antiad.RuleAgentStatus(sh)})
	case "agent_stop":
		miniOK(w, map[string]any{
			"ok": true, "stopped": antiad.StopRuleDiscovery(sh)})
	default:
		miniErr(w, http.StatusBadRequest, "未知操作")
	}
}

// miniRuleTestJSON 把测试结果转成前端契约形状。
func miniRuleTestJSON(r antiad.RuleTestResult) map[string]any {
	samples := func(list []antiad.RuleSample) []map[string]any {
		out := make([]map[string]any, 0, len(list))
		for _, s := range list {
			out = append(out, map[string]any{
				"id": s.ID, "verdict": s.Verdict, "action": s.Action,
				"kind": s.Kind, "user_id": s.UserID, "chat_id": s.ChatID,
				"created_at": s.CreatedAt, "text": s.Text,
			})
		}
		return out
	}
	kinds := make([]map[string]any, 0, len(r.Kinds))
	for _, k := range r.Kinds {
		kinds = append(kinds, map[string]any{
			"kind": k.Kind, "total": k.Total, "matched": k.Matched,
			"coverage": k.Coverage(),
		})
	}
	return map[string]any{
		"pattern": r.Pattern, "scanned": r.Scanned, "matched": r.Matched,
		"tp": r.TP, "fp": r.FP, "undone": r.Undone, "neutral": r.Neutral,
		"ads_total": r.AdsTotal, "coverage": r.Coverage(), "kinds": kinds,
		"tp_samples":     samples(r.TPSamples),
		"fp_samples":     samples(r.FPSamples),
		"undone_samples": samples(r.UndoneSamples),
	}
}

// miniLogCols 是记录列表与详情共用的列清单。
const miniLogCols = `id,bot_id,chat_id,user_id,message_id,verdict,confidence,
		ad_kind,action,reason,text,decider,quota_cost,created_at`

func miniLogRow(out map[string]any, id, botID, chatID, userID, msgID, cost, at int64,
	verdict, kind, action, reason, text, decider string, conf float64, viewURL string) {

	out["id"] = id
	out["bot_id"] = botID
	out["chat_id"] = chatID
	out["user_id"] = userID
	out["message_id"] = msgID
	out["verdict"] = verdict
	out["confidence"] = conf
	out["kind"] = kind
	out["action"] = action
	out["reason"] = reason
	out["text"] = text
	out["decider"] = decider
	out["cost_text"] = billing.FormatUSDFine(cost)
	out["created_at"] = at
	out["view_url"] = viewURL
}

// mainBotID 返回主 bot 的记录号；没配主 bot 时返回 0。
// 主管理员在 Mini App 里要的是平台级视角（统计与记录不过滤 bot），
// 拿主 bot 的记录号当作全局范围的标记用。
func mainBotID(sh *core.Shared) int64 {
	for _, rec := range sh.Cache.Snap().Bots {
		if rec.IsMain {
			return rec.BotID
		}
	}
	return 0
}

// miniUser 返回一个人的资料卡与判定记录（Mini App 的用户页）。
//
// 默认只给被处置过的记录（antiad.ProcessedCond）：管理员点进一个人的页面，
// 先要看他被罚过什么，而不是他所有被判过正常的话；点切换看全部。
func miniUser(sh *core.Shared, w http.ResponseWriter, r *http.Request, uid int64, body map[string]any) {
	target := miniInt(body, "user_id")
	if target == 0 {
		miniErr(w, http.StatusBadRequest, "缺少 user_id")
		return
	}
	all := miniStr(body, "filter") == "all"
	page := clampPage(miniInt(body, "page"))
	botID := miniInt(body, "bot_id")
	if botID == 0 {
		// 前端只在切 bot 时带 body；常规请求从 X-Bot-Id 头取（每个请求都带）。
		botID, _ = strconv.ParseInt(r.Header.Get(miniBotIDHeader), 10, 64)
	}
	if botID == 0 || !sh.CanManageBot(uid, botID) {
		miniErr(w, http.StatusForbidden, "无权查看该 bot 的数据")
		return
	}

	// 主管理员是平台级视角（与面板 /user、Mini App 记录列表一致）：统计与
	// 记录按全局算，而不是按他恰好打开的那个 bot。拿主 bot 的记录号来算 ——
	// logScope/dossierChats 见到 is_main 就不设范围。
	scopeBot := botID
	if sh.IsMain(uid) {
		if id := mainBotID(sh); id != 0 {
			scopeBot = id
		}
	}

	d := antiad.LoadUserDossier(sh, scopeBot, target)
	// 昵称/用户名/简介：挑一个与该用户有共同会话的 bot 现查，查不到再回落到
	// 流水里留存的 user_name。主管理员这里 scopeBot 是主 bot（平台级视角），
	// 而主 bot 不入群、查不到任何人，不换 bot 则始终查不到。
	inst, _ := lookupBot(sh, scopeBot)
	antiad.ResolveUserProfile(sh, &d, inst, scopeBot, target)

	clauseWhere, clauseArgs := miniBotsClause(sh, uid, sh.IsMain(uid))
	where := `user_id=?` + clauseWhere
	args := append([]any{target}, clauseArgs...)
	if !all {
		where += ` AND ` + antiad.ProcessedCond
	}
	var total int64
	sh.Store.Read.QueryRow(`SELECT COUNT(*) FROM antiad_log WHERE `+where, args...).Scan(&total)

	rows := antiad.LoadUserLogs(sh, scopeBot, target, !all, 20, int((page-1)*20))
	logs := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		logs = append(logs, map[string]any{
			"id": r.ID, "chat_id": r.ChatID, "verdict": r.Verdict,
			"confidence": r.Conf, "kind": r.Kind, "action": r.Action,
			"reason": r.Reason, "text": r.Text, "created_at": r.At,
		})
	}
	miniOK(w, map[string]any{
		"user_id": target, "name": d.Name, "username": d.Username, "bio": d.Bio,
		"kept": d.Kept, "msgs": d.Msgs, "hits": d.Hits, "chats": d.ChatCount,
		"first_seen": d.FirstSeen, "last_msg": d.LastMsg,
		"total": d.Total, "processed": d.Processed, "shown": total,
		"page": page, "filter": map[bool]string{true: "all", false: "act"}[all],
		"logs": logs,
	})
}

// lookupBot 取一个在跑的实例（没有就返回 false：资料卡退化成只有数据库里的）。
func lookupBot(sh *core.Shared, botID int64) (*core.Bot, bool) {
	if sh.Reg == nil {
		return nil, false
	}
	return sh.Reg.LookupID(botID)
}

func miniLogs(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	page := clampPage(miniInt(body, "page"))
	clauseWhere, clauseArgs := miniBotsClause(sh, uid, sh.IsMain(uid))

	// 筛选与搜索。默认无筛选（前端会带 verdict=deleted 进来）：
	// 已删除是处置动作名前缀而非 verdict；放行/跳过的记录也可能带处置，
	// 按动作筛选才与消息被删除的用户视角一致。
	condWhere, condArgs := "", []any{}
	switch miniStr(body, "verdict") {
	case "deleted":
		condWhere += " AND action LIKE 'deleted%'"
	case "ad":
		condWhere += " AND verdict='ad'"
	case "clean":
		// 流水写入的是 'clean'（见 antiad.logAd）；'none' 也一并匹配，
		// 避免历史记录在筛选时被排除。
		condWhere += " AND verdict IN ('clean','none')"
	case "skipped":
		condWhere += " AND verdict='skipped'"
	}
	if target := miniInt(body, "user_id"); target != 0 {
		condWhere += " AND user_id=?"
		condArgs = append(condArgs, target)
	}
	if chat := miniInt(body, "chat_id"); chat != 0 {
		condWhere += " AND chat_id=?"
		condArgs = append(condArgs, chat)
	}
	if qs := miniStr(body, "q"); qs != "" {
		like := "%" + qs + "%"
		condWhere += " AND (text LIKE ? OR reason LIKE ?"
		condArgs = append(condArgs, like, like)
		if n, err := strconv.ParseInt(qs, 10, 64); err == nil {
			condWhere += " OR user_id=? OR chat_id=?"
			condArgs = append(condArgs, n, n)
		}
		condWhere += ")"
	}

	q := append([]any{}, clauseArgs...)
	q = append(q, condArgs...)
	// total 与列表用完全相同的 WHERE 与 args（同一份 clauseWhere/condWhere），
	// 只是不带分页；前端靠它判断无限滚动还有没有下一页。
	var total int64
	if err := sh.Store.Read.QueryRow(`SELECT COUNT(*) FROM antiad_log
		WHERE 1=1`+clauseWhere+condWhere, q...).Scan(&total); err != nil {
		miniErr(w, http.StatusInternalServerError, "查询失败")
		return
	}
	rows, err := sh.Store.Read.Query(`SELECT `+miniLogCols+` FROM antiad_log
		WHERE 1=1`+clauseWhere+condWhere+` ORDER BY id DESC LIMIT 20 OFFSET ?`,
		append(q, (page-1)*20)...)
	if err != nil {
		miniErr(w, http.StatusInternalServerError, "查询失败")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, botID, chatID, userID, msgID, cost, at int64
		var verdict, kind, action, reason, text, decider string
		var conf float64
		if rows.Scan(&id, &botID, &chatID, &userID, &msgID, &verdict, &conf,
			&kind, &action, &reason, &text, &decider, &cost, &at) == nil {
			m := map[string]any{}
			miniLogRow(m, id, botID, chatID, userID, msgID, cost, at,
				verdict, kind, action, reason, text, decider, conf,
				antiad.LogViewURL(sh, id))
			out = append(out, m)
		}
	}
	miniOK(w, map[string]any{"logs": out, "page": page, "total": total})
}

// miniLogDetail 单条记录的全部字段，供点进去的详情页。
func miniLogDetail(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	id := miniInt(body, "id")
	clauseWhere, clauseArgs := miniBotsClause(sh, uid, sh.IsMain(uid))
	row := sh.Store.Read.QueryRow(`SELECT `+miniLogCols+` FROM antiad_log
		WHERE id=?`+clauseWhere, append([]any{id}, clauseArgs...)...)
	var lid, botID, chatID, userID, msgID, cost, at int64
	var verdict, kind, action, reason, text, decider string
	var conf float64
	if row.Scan(&lid, &botID, &chatID, &userID, &msgID, &verdict, &conf,
		&kind, &action, &reason, &text, &decider, &cost, &at) != nil {
		miniErr(w, http.StatusNotFound, "记录不存在")
		return
	}
	// 流水正文为空时回查全量留底（group_messages），使留底存在但流水
	// 为空的记录在详情页仍能看到原文。
	if text == "" && msgID != 0 {
		sh.Store.Read.QueryRow(`SELECT text FROM group_messages
			WHERE chat_id=? AND message_id=?`, chatID, msgID).Scan(&text)
	}
	out := map[string]any{}
	miniLogRow(out, lid, botID, chatID, userID, msgID, cost, at,
		verdict, kind, action, reason, text, decider, conf,
		antiad.LogViewURL(sh, lid))
	miniOK(w, out)
}

// miniLogact 对一条记录执行操作。权限：能管该 bot 的人；联合封禁
// 额外要求主管理员。bot_id 为 0 的旧记录回落到请求头里的 bot。
func miniLogact(sh *core.Shared, w http.ResponseWriter, r *http.Request,
	uid int64, body map[string]any) {
	id := miniInt(body, "id")
	action := miniStr(body, "action")
	clauseWhere, clauseArgs := miniBotsClause(sh, uid, sh.IsMain(uid))
	row := sh.Store.Read.QueryRow(`SELECT bot_id,chat_id,user_id,message_id,text,action
		FROM antiad_log WHERE id=?`+clauseWhere, append([]any{id}, clauseArgs...)...)
	var botID, chatID, userID, msgID int64
	var text, rowAction string
	if row.Scan(&botID, &chatID, &userID, &msgID, &text, &rowAction) != nil {
		miniErr(w, http.StatusNotFound, "记录不存在")
		return
	}
	if botID == 0 {
		botID, _ = strconv.ParseInt(r.Header.Get(miniBotIDHeader), 10, 64)
	}
	if action == "gban" || action == "ungban" {
		if !sh.IsMain(uid) {
			miniErr(w, http.StatusForbidden, "联合封禁只有主管理员能操作")
			return
		}
	} else if !sh.CanManageBot(uid, botID) {
		miniErr(w, http.StatusForbidden, "无权管理该 bot")
		return
	}
	if sh.Reg == nil {
		miniErr(w, http.StatusInternalServerError, "注册表不可用")
		return
	}
	inst, live := sh.Reg.LookupID(botID)
	if !live {
		miniErr(w, http.StatusBadRequest, "该 bot 未在运行，无法执行群内操作")
		return
	}
	snap := sh.Cache.Snap()
	conf, ok := snap.ChatConf(botID, chatID)
	if !ok {
		// 群已移除也能操作：解除禁言、加白名单仍有意义。
		conf = store.BotChat{BotID: botID, ChatID: chatID}
	}

	switch action {
	case "review":
		if !antiad.ReviewByRecord(inst, conf, chatID, userID, uid) {
			miniErr(w, http.StatusBadRequest, "该用户在本群没有留底消息，无法复查")
			return
		}
	case "ban":
		if note := antiad.ManualMarkByRecord(inst, conf, chatID, msgID,
			userID, text, uid); note != "" {
			miniOK(w, map[string]any{"ok": true, "note": "部分动作失败：" + note})
			return
		}
	case "unmute":
		// 解封（判定维持）：与 TG 记录卡片的 `🔓 解封` 同一条路径 ——
		// 撤掉生效中的限制并清记录，但不动判定本身。记录行要完整取出来
		// （ReleaseUser 要按动作决定解封还是解禁言，并在理由里留痕）。
		full, ok2 := antiad.LoadAdLog(sh.Store, id)
		if !ok2 {
			miniErr(w, http.StatusNotFound, "记录不存在")
			return
		}
		antiad.ReleaseUser(inst, full, uid)
	case "white":
		hours := miniInt(body, "hours")
		if hours <= 0 {
			hours = 24
		}
		if err := antiad.AddWhitelist(sh, botID, chatID, userID,
			time.Duration(hours)*time.Hour, "miniapp", uid); err != nil {
			miniErr(w, http.StatusInternalServerError, "写入失败")
			return
		}
	case "gban":
		if err := antiad.GbanAdd(sh, userID,
			fmt.Sprintf("配置台将记录 #%d 人工标黑", id), chatID, botID); err != nil {
			miniErr(w, http.StatusInternalServerError, "写入失败")
			return
		}
	case "ungban":
		note := "该用户不在你能解除的名单里"
		if s := antiad.AdminLiftGban(sh, uid, userID); s != "" {
			note = "已解除：" + s
		}
		miniOK(w, map[string]any{"ok": true, "note": note})
		return
	default:
		miniErr(w, http.StatusBadRequest, "未知操作")
		return
	}
	miniOK(w, map[string]any{"ok": true})
}

func miniAppeals(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	page := clampPage(miniInt(body, "page"))
	clauseWhere, clauseArgs := miniBotsClause(sh, uid, sh.IsMain(uid))
	// 未结筛选放在查询里：只筛已取回的那 20 行的话，更新更早的未结单
	// 永远不出现在列表里，管理员会遗漏真正要处理的单子。
	if miniStr(body, "filter") == "open" {
		clauseWhere += " AND status IN (" + store.AppealOpenStatusesSQL + ")"
	}
	var total int64
	if err := sh.Store.Read.QueryRow(`SELECT COUNT(*) FROM appeals
		WHERE 1=1`+clauseWhere, clauseArgs...).Scan(&total); err != nil {
		miniErr(w, http.StatusInternalServerError, "查询失败")
		return
	}
	rows, err := sh.Store.Read.Query(`SELECT id,bot_id,user_id,status,statement,
		ai_result,ai_conf,ai_reason,ai_model,web_attempts,code,code_expires,
		created_at,updated_at FROM appeals WHERE 1=1`+clauseWhere+
		` ORDER BY id DESC LIMIT 20 OFFSET ?`, append(clauseArgs, (page-1)*20)...)
	if err != nil {
		miniErr(w, http.StatusInternalServerError, "查询失败")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, botID, userID, attempts, codeExp, at, upd int64
		var status, statement, aiResult, aiReason, aiModel, code string
		var conf float64
		if rows.Scan(&id, &botID, &userID, &status, &statement, &aiResult,
			&conf, &aiReason, &aiModel, &attempts, &code, &codeExp, &at, &upd) == nil {
			out = append(out, map[string]any{
				"id": id, "bot_id": botID, "user_id": userID, "status": status,
				"statement": core.TruncateRunes(statement, 300),
				"ai_result": aiResult, "ai_conf": conf,
				"ai_reason":    core.TruncateRunes(aiReason, 300),
				"ai_model":     aiModel,
				"web_attempts": attempts,
				"has_code":     code != "", "code_expires": codeExp,
				"created_at": at, "updated_at": upd,
				"detail_url": antiad.AppealDetailURL(sh, id),
			})
		}
	}
	miniOK(w, map[string]any{"appeals": out, "page": page, "total": total})
}

// miniAppealDetail 单张申诉单：全字段加兑换与网页验证记录。
func miniAppealDetail(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	id := miniInt(body, "id")
	clauseWhere, clauseArgs := miniBotsClause(sh, uid, sh.IsMain(uid))
	row := sh.Store.Read.QueryRow(`SELECT `+antiad.AppealColumns+`
		FROM appeals WHERE id=?`+clauseWhere, append([]any{id}, clauseArgs...)...)
	var aid, botID, userID, cost, attempts, webSince, codeExp, at, upd int64
	var status, statement, aiResult, aiReason, aiModel, code string
	var conf float64
	if row.Scan(&aid, &botID, &userID, &status, &statement, &aiResult, &conf,
		&aiReason, &aiModel, &cost, &attempts, &webSince, &code, &codeExp,
		&at, &upd) != nil {
		miniErr(w, http.StatusNotFound, "申诉单不存在")
		return
	}
	out := map[string]any{
		"id": aid, "bot_id": botID, "user_id": userID, "status": status,
		"statement": statement, "ai_result": aiResult, "ai_conf": conf,
		"ai_reason": aiReason, "ai_model": aiModel,
		"ai_cost":      billing.FormatUSDFine(cost),
		"web_attempts": attempts, "has_code": code != "",
		"code": code, "code_expires": codeExp,
		"created_at": at, "updated_at": upd,
		"detail_url": antiad.AppealDetailURL(sh, aid),
	}
	redeems := []map[string]any{}
	rd, err := sh.Store.Read.Query(`SELECT chat_id,by_uid,at FROM appeal_redeems
		WHERE appeal_id=?`, aid)
	if err == nil {
		for rd.Next() {
			var chatID, by, rat int64
			if rd.Scan(&chatID, &by, &rat) == nil {
				redeems = append(redeems, map[string]any{
					"chat_id": chatID, "by_uid": by, "at": rat})
			}
		}
		rd.Close()
	}
	out["redeems"] = redeems
	var checks, passes int
	sh.Store.Read.QueryRow(`SELECT COUNT(*), COALESCE(SUM(result='pass'),0)
		FROM web_checks WHERE appeal_id=?`, aid).Scan(&checks, &passes)
	out["web_checks"] = checks
	out["web_passes"] = passes
	miniOK(w, out)
}

// miniAppealact 对一张申诉单执行人工处理。
func miniAppealact(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	id := miniInt(body, "id")
	action := miniStr(body, "action")
	clauseWhere, clauseArgs := miniBotsClause(sh, uid, sh.IsMain(uid))
	var botID int64
	err := sh.Store.Read.QueryRow(`SELECT bot_id FROM appeals
		WHERE id=?`+clauseWhere, append([]any{id}, clauseArgs...)...).Scan(&botID)
	if err != nil {
		miniErr(w, http.StatusNotFound, "申诉单不存在")
		return
	}
	if sh.Reg == nil {
		miniErr(w, http.StatusInternalServerError, "注册表不可用")
		return
	}
	inst, live := sh.Reg.LookupID(botID)
	if !live {
		miniErr(w, http.StatusBadRequest, "该 bot 未在运行，无法处理")
		return
	}
	var actErr error
	switch action {
	case "approve":
		actErr = antiad.AdminLiftAppeal(inst, id, uid)
	case "reject":
		actErr = antiad.AdminRejectAppeal(inst, id, uid)
	case "issue_code":
		_, actErr = antiad.AdminIssueCode(inst, id)
	case "rerun":
		actErr = antiad.AdminRerunAppealAI(inst, id)
	default:
		miniErr(w, http.StatusBadRequest, "未知操作")
		return
	}
	if actErr != nil {
		miniErr(w, http.StatusBadRequest, actErr.Error())
		return
	}
	miniOK(w, map[string]any{"ok": true})
}

// ---- 小工具 ----

// miniPrice 读取一个价格字段：缺省按 0 处理，出现负价、NaN、非数字一律拒绝。
// 负价会让开销核算变成负数，NaN 会让面板上的数字直接变成 NaN。
func miniPrice(body map[string]any, key string) (float64, error) {
	v, ok := body[key]
	if !ok {
		return 0, nil
	}
	var f float64
	switch t := v.(type) {
	case float64:
		f = t
	case string:
		if s := strings.TrimSpace(t); s != "" {
			var err error
			f, err = strconv.ParseFloat(s, 64)
			if err != nil {
				return 0, fmt.Errorf("%s 必须是数字", key)
			}
		}
	default:
		return 0, fmt.Errorf("%s 必须是数字", key)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0, fmt.Errorf("%s 必须是非负数", key)
	}
	return f, nil
}

// parseModelList 解析逗号分隔的模型名列表：逐个校验存在且启用，去重但
// 保持输入顺序（顺序即重试顺序）。返回的错误直接给管理员看。
func parseModelList(snap *store.Snapshot, text string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(text, ",") {
		name := strings.TrimSpace(raw)
		if name == "" || seen[name] {
			continue
		}
		m := snap.Models[name]
		if m == nil {
			return nil, fmt.Errorf("模型 %s 不在「模型定价」里", name)
		}
		if !m.Enabled {
			return nil, fmt.Errorf("模型 %s 已被停用", name)
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("至少要填一个模型名")
	}
	return out, nil
}
