package panel

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
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

const miniInitDataHeader = "X-Tg-Init-Data"
const miniBotIDHeader = "X-Bot-Id"

// MiniAppHandler 返回 /miniapp 的处理器；由 main 组合进 HTTP 服务。
func MiniAppHandler(sh *core.Shared) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimRight(r.URL.Path, "/")
		if p == "/miniapp" {
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			fmt.Fprint(w, miniAppHTML)
			return
		}
		if !strings.HasPrefix(p, "/miniapp/api/") {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		miniAPI(sh, w, r, strings.TrimPrefix(p, "/miniapp/api/"))
	})
}

// validateMiniInitData 校验 Telegram WebApp 的 initData。
//
// secret_key = HMAC-SHA256("WebAppData", bot_token)，
// hash = HMAC-SHA256(secret_key, data_check_string)。逐字节比较用常数时间。
func validateMiniInitData(token, initData string) (int64, error) {
	vals, err := url.ParseQuery(initData)
	if err != nil {
		return 0, fmt.Errorf("initData 无法解析")
	}
	hash := vals.Get("hash")
	if hash == "" {
		return 0, fmt.Errorf("initData 缺少 hash")
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
		return 0, fmt.Errorf("initData 签名不匹配")
	}
	ad, _ := strconv.ParseInt(vals.Get("auth_date"), 10, 64)
	if ad == 0 || time.Now().Unix()-ad > 24*3600 {
		return 0, fmt.Errorf("initData 已过期，请重新打开")
	}
	var u struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal([]byte(vals.Get("user")), &u) != nil || u.ID == 0 {
		return 0, fmt.Errorf("initData 缺少用户信息")
	}
	return u.ID, nil
}

// miniAuth 校验请求并返回操作者；失败时已写好响应。
func miniAuth(sh *core.Shared, w http.ResponseWriter, r *http.Request) (uid, botID int64, ok bool) {
	botID, _ = strconv.ParseInt(r.Header.Get(miniBotIDHeader), 10, 64)
	rec := sh.Cache.Snap().Bots[botID]
	if rec == nil {
		miniErr(w, http.StatusBadRequest, "未知的 bot")
		return 0, 0, false
	}
	uid, err := validateMiniInitData(rec.Token, r.Header.Get(miniInitDataHeader))
	if err != nil {
		miniErr(w, http.StatusUnauthorized, err.Error())
		return 0, 0, false
	}
	if !sh.IsStaff(uid) {
		miniErr(w, http.StatusForbidden, "你不是本服务的管理员")
		return 0, 0, false
	}
	return uid, botID, true
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

// miniCanManageBot 与面板的 CanManageBot 同一套规则。
func miniCanManageBot(sh *core.Shared, uid, botID int64) bool {
	if sh.IsMain(uid) {
		return true
	}
	rec := sh.Cache.Snap().Bots[botID]
	return rec != nil && rec.OwnerID == uid
}

// miniAPI 是 /miniapp/api/* 的总分发。
func miniAPI(sh *core.Shared, w http.ResponseWriter, r *http.Request, op string) {
	uid, _, ok := miniAuth(sh, w, r)
	if !ok {
		return
	}
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
		miniState(sh, w, uid)
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
	case "logs":
		miniLogs(sh, w, uid, body)
	case "log":
		miniLogDetail(sh, w, uid, body)
	case "logact":
		miniLogact(sh, w, r, uid, body)
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

func miniState(sh *core.Shared, w http.ResponseWriter, uid int64) {
	snap := sh.Cache.Snap()
	main := sh.IsMain(uid)

	me := map[string]any{"uid": uid, "main": main}
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
		specs = append(specs, map[string]any{
			"key": sp.key, "label": sp.label, "hint": sp.hint,
			"min": sp.min, "max": sp.max, "group": sp.group,
		})
	}

	stats := miniStats(sh, uid, main)

	// global_defaults 是 antiad/both 组设置与两个模型列表键的全局默认值，
	// 所有管理员都下发：机器人页要拿它在输入框占位里画「30(全局)」，
	// 而次级管理员看不到完整全局设置（global），不能指望从那里取。
	defaults := map[string]any{}
	for _, sp := range settingSpecs {
		if sp.group == "antiad" || sp.group == "both" {
			defaults[sp.key] = snap.Setting(sp.key)
		}
	}
	defaults["antiad_so_models"] = snap.Setting("antiad_so_models")
	defaults["antiad_llm_models"] = snap.Setting("antiad_llm_models")

	out := map[string]any{
		"me": me, "bots": bots, "chats": chats, "bot_settings": botSettings,
		"specs": specs, "stats": stats, "global_defaults": defaults,
	}
	if main {
		out["global"] = snap.Settings
		out["digest"] = snap.Setting("antiad_digest")
	}
	out["whitelist"] = miniWhitelistRows(sh, uid, main)

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
				"status": u.Status, "supports_chat": u.SupportsChat,
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
	}
	miniOK(w, out)
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

// miniBotsClause 返回按权限过滤 antiad_log 的 SQL 片段与参数。
func miniBotsClause(sh *core.Shared, uid int64, main bool) (string, []any) {
	if main {
		return "", nil
	}
	owned := sh.Cache.Snap().BotsOwnedBy(uid, false)
	if len(owned) == 0 {
		return " AND 0", nil
	}
	holders := make([]string, 0, len(owned))
	args := make([]any, 0, len(owned))
	for _, r := range owned {
		holders = append(holders, "?")
		args = append(args, r.BotID)
	}
	return " AND bot_id IN (" + strings.Join(holders, ",") + ")", args
}

func miniWhitelistRows(sh *core.Shared, uid int64, main bool) []map[string]any {
	out := []map[string]any{}
	for _, w := range sh.Cache.Snap().Whitelist {
		if !main && w.BotID != 0 && !miniCanManageBot(sh, uid, w.BotID) {
			continue
		}
		out = append(out, map[string]any{
			"bot_id": w.BotID, "chat_id": w.ChatID, "user_id": w.UserID,
			"expires_at": w.ExpiresAt, "source": w.Source, "by_uid": w.ByUID,
		})
	}
	return out
}

// ---- 写操作 ----

// miniSet 改设置：scope=global（仅主管理员）或 bot（按 spec 分组校验）。
func miniSet(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	key := miniStr(body, "key")
	val := strings.TrimSpace(miniStr(body, "value"))

	// 三个布尔开关不走 settingSpecs（面板里是一键切换），单独放行。
	switch key {
	case "antiad_enabled", "alert_copy_main", "gban_enabled":
		if !sh.IsMain(uid) {
			miniErr(w, http.StatusForbidden, "只有主管理员能改全局开关")
			return
		}
		if val != "0" && val != "1" {
			miniErr(w, http.StatusBadRequest, "取值必须是 0 或 1")
			return
		}
		if err := sh.PutSetting(key, val); err != nil {
			miniErr(w, http.StatusInternalServerError, "保存失败")
			return
		}
		miniOK(w, map[string]any{"ok": true})
		return
	case "tz_name":
		// 时区是字符串型（IANA 名称），与整数型 specs 分开校验。
		if !sh.IsMain(uid) {
			miniErr(w, http.StatusForbidden, "只有主管理员能改全局设置")
			return
		}
		if val == "" || val == "-" {
			val = "Asia/Shanghai"
		}
		if _, err := time.LoadLocation(val); err != nil {
			miniErr(w, http.StatusBadRequest,
				"不是有效的 IANA 时区名，如 Asia/Shanghai")
			return
		}
		if err := sh.PutSetting("tz_name", val); err != nil {
			miniErr(w, http.StatusInternalServerError, "保存失败")
			return
		}
		miniOK(w, map[string]any{"ok": true})
		return
	}

	sp := settingSpecByKey(key)
	if sp == nil {
		miniErr(w, http.StatusBadRequest, "未知设置项")
		return
	}
	botID := miniInt(body, "bot_id")
	if botID != 0 {
		if sp.group != "antiad" && sp.group != "both" {
			miniErr(w, http.StatusBadRequest, "该项不能按 bot 覆盖")
			return
		}
		if !miniCanManageBot(sh, uid, botID) {
			miniErr(w, http.StatusForbidden, "无权管理该 bot")
			return
		}
		if val == "" {
			// 清空 = 删除覆盖、跟随全局（PutBotSetting 在同值时删行）。
			val = sh.Cache.Snap().Setting(key)
		}
	} else {
		if sp.group != "" && sp.group != "both" {
			miniErr(w, http.StatusBadRequest, "该项只能按 bot 设置")
			return
		}
		if !sh.IsMain(uid) {
			miniErr(w, http.StatusForbidden, "只有主管理员能改全局设置")
			return
		}
	}
	if _, err := strconv.ParseInt(val, 10, 64); err != nil {
		miniErr(w, http.StatusBadRequest, "取值必须是整数："+sp.hint)
		return
	}
	n, _ := strconv.ParseInt(val, 10, 64)
	if n < sp.min || (sp.max != 0 && n > sp.max) {
		miniErr(w, http.StatusBadRequest, "取值非法："+sp.hint)
		return
	}
	var err error
	if botID == 0 {
		err = sh.PutSetting(key, val)
	} else {
		err = sh.PutBotSetting(botID, key, val)
	}
	if err != nil {
		miniErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	miniOK(w, map[string]any{"ok": true})
}

func miniBot(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	botID := miniInt(body, "bot_id")
	if !miniCanManageBot(sh, uid, botID) {
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
	case "models":
		which := miniStr(body, "which") // so / llm
		if which != "so" && which != "llm" {
			miniErr(w, http.StatusBadRequest, "which 必须是 so 或 llm")
			return
		}
		text := miniStr(body, "value")
		var models []string
		if text != "" && text != "-" {
			var err error
			models, err = parseModelListSnap(sh.Cache.Snap(), text)
			if err != nil {
				miniErr(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		if err := sh.SetBotModels(botID, which, models); err != nil {
			miniErr(w, http.StatusInternalServerError, "保存失败")
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
	if !miniCanManageBot(sh, uid, botID) {
		miniErr(w, http.StatusForbidden, "无权管理该 bot")
		return
	}
	action := miniStr(body, "action")
	switch action {
	case "add", "update":
		if chatID == 0 {
			miniErr(w, http.StatusBadRequest, "chat_id 无效")
			return
		}
		if _, ok := sh.Cache.Snap().ChatConf(botID, chatID); !ok {
			title := miniChatTitle(sh, botID, chatID)
			if _, err := sh.Store.Write.Exec(`INSERT INTO bot_chats
				(bot_id,chat_id,title,enabled,dryrun,group_alert,created_at)
				VALUES (?,?,?,1,1,0,?)`, botID, chatID, title, time.Now().Unix()); err != nil {
				miniErr(w, http.StatusInternalServerError, "添加失败")
				return
			}
		}
		// 其余字段按请求里出现的项更新。
		cols := []string{}
		args := []any{}
		for _, f := range []string{"enabled", "dryrun", "group_alert"} {
			if v, ok := body[f]; ok {
				on := false
				switch t := v.(type) {
				case bool:
					on = t
				case float64:
					on = t != 0
				}
				cols = append(cols, f+"=?")
				if on {
					args = append(args, 1)
				} else {
					args = append(args, 0)
				}
			}
		}
		if v, ok := body["title"]; ok {
			cols = append(cols, "title=?")
			args = append(args, fmt.Sprint(v))
		}
		if v, ok := body["punish"]; ok {
			cols = append(cols, "punish=?")
			args = append(args, miniInt(map[string]any{"v": v}, "v"))
		}
		if len(cols) > 0 {
			args = append(args, botID, chatID)
			if _, err := sh.Store.Write.Exec(`UPDATE bot_chats SET `+
				strings.Join(cols, ",")+` WHERE bot_id=? AND chat_id=?`, args...); err != nil {
				miniErr(w, http.StatusInternalServerError, "保存失败")
				return
			}
		}
	case "remove":
		if _, err := sh.Store.Write.Exec(`DELETE FROM bot_chats
			WHERE bot_id=? AND chat_id=?`, botID, chatID); err != nil {
			miniErr(w, http.StatusInternalServerError, "删除失败")
			return
		}
	default:
		miniErr(w, http.StatusBadRequest, "未知操作")
		return
	}
	if err := sh.Cache.Reload(); err != nil {
		slog.Error("miniapp：刷新缓存失败", "err", err)
	}
	miniOK(w, map[string]any{"ok": true})
}

func miniUpstream(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	if !sh.IsMain(uid) {
		miniErr(w, http.StatusForbidden, "只有主管理员能配置上游")
		return
	}
	action := miniStr(body, "action")
	switch action {
	case "add":
		name := miniStr(body, "name")
		if err := core.ValidUpstreamName(name); err != nil {
			miniErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, err := sh.Store.Write.Exec(`INSERT INTO upstreams
			(name,base_url,api_key,weight,status,supports_chat,supports_systemone)
			VALUES (?,?,?,?,?,?,?)`, name, miniStr(body, "base_url"),
			miniStr(body, "api_key"), maxInt64(miniInt(body, "weight"), 1),
			boolToInt64(!miniBool(body, "disabled")),
			boolToInt64(miniBool(body, "supports_chat")),
			boolToInt64(miniBool(body, "supports_systemone"))); err != nil {
			miniErr(w, http.StatusInternalServerError, "添加失败")
			return
		}
	case "update":
		id := miniInt(body, "id")
		if name := miniStr(body, "name"); name != "" {
			if err := sh.RenameUpstream(id, name); err != nil {
				miniErr(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		for _, f := range []string{"base_url", "api_key"} {
			if v := miniStr(body, f); v != "" {
				sh.Store.Write.Exec(`UPDATE upstreams SET `+f+`=? WHERE id=?`, v, id)
			}
		}
		if _, ok := body["weight"]; ok {
			sh.Store.Write.Exec(`UPDATE upstreams SET weight=? WHERE id=?`,
				maxInt64(miniInt(body, "weight"), 1), id)
		}
		if _, ok := body["status"]; ok {
			sh.Store.Write.Exec(`UPDATE upstreams SET status=? WHERE id=?`,
				boolToInt64(miniBool(body, "status")), id)
		}
		for _, f := range []string{"supports_chat", "supports_systemone"} {
			if _, ok := body[f]; ok {
				sh.Store.Write.Exec(`UPDATE upstreams SET `+f+`=? WHERE id=?`,
					boolToInt64(miniBool(body, f)), id)
			}
		}
	case "remove":
		id := miniInt(body, "id")
		if names := modelsOfUpstream(sh.Cache.Snap(), id); len(names) > 0 {
			miniErr(w, http.StatusBadRequest,
				"该上游名下有模型，请先删除它们："+strings.Join(names, "、"))
			return
		}
		if _, err := sh.Store.Write.Exec(`DELETE FROM upstreams WHERE id=?`, id); err != nil {
			miniErr(w, http.StatusInternalServerError, "删除失败")
			return
		}
	default:
		miniErr(w, http.StatusBadRequest, "未知操作")
		return
	}
	if err := sh.Cache.Reload(); err != nil {
		slog.Error("miniapp：刷新缓存失败", "err", err)
	}
	miniOK(w, map[string]any{"ok": true})
}

func miniModel(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	if !sh.IsMain(uid) {
		miniErr(w, http.StatusForbidden, "只有主管理员能配置模型")
		return
	}
	action := miniStr(body, "action")
	name := miniStr(body, "name")
	switch action {
	case "add":
		upName := miniStr(body, "upstream")
		modelID := miniStr(body, "model_id")
		if upName == "" || modelID == "" {
			miniErr(w, http.StatusBadRequest, "新增模型要选上游并填模型 ID")
			return
		}
		if err := core.ValidUpstreamName(upName); err != nil {
			miniErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if sh.Cache.Snap().Upstreams == nil {
			miniErr(w, http.StatusBadRequest, "上游不存在")
			return
		}
		found := false
		for _, u := range sh.Cache.Snap().Upstreams {
			if u.Name == upName {
				found = true
				break
			}
		}
		if !found {
			miniErr(w, http.StatusBadRequest, "上游 "+upName+" 不存在")
			return
		}
		name = upName + "/" + modelID
		if sh.Cache.Snap().Models[name] != nil {
			miniErr(w, http.StatusBadRequest, "该模型已存在")
			return
		}
		if _, err := sh.Store.Write.Exec(`INSERT INTO models
			(name,prompt_price,completion_price,cache_read_price,cache_write_price,enabled)
			VALUES (?,?,?,?,?,1)`, name,
			miniFloat(body, "prompt_price"), miniFloat(body, "completion_price"),
			miniFloat(body, "cache_read_price"), miniFloat(body, "cache_write_price")); err != nil {
			miniErr(w, http.StatusInternalServerError, "添加失败")
			return
		}
	case "update", "remove":
		if name == "" {
			miniErr(w, http.StatusBadRequest, "模型名不能为空")
			return
		}
		if sh.Cache.Snap().Models[name] == nil {
			miniErr(w, http.StatusBadRequest, "模型不存在")
			return
		}
		if action == "remove" {
			if _, err := sh.Store.Write.Exec(`DELETE FROM models WHERE name=?`, name); err != nil {
				miniErr(w, http.StatusInternalServerError, "删除失败")
				return
			}
			break
		}
		for _, f := range []string{"prompt_price", "completion_price",
			"cache_read_price", "cache_write_price"} {
			if _, ok := body[f]; ok {
				sh.Store.Write.Exec(`UPDATE models SET `+f+`=? WHERE name=?`,
					miniFloat(body, f), name)
			}
		}
		if _, ok := body["enabled"]; ok {
			sh.Store.Write.Exec(`UPDATE models SET enabled=? WHERE name=?`,
				boolToInt64(miniBool(body, "enabled")), name)
		}
	default:
		miniErr(w, http.StatusBadRequest, "未知操作")
		return
	}
	if err := sh.Cache.Reload(); err != nil {
		slog.Error("miniapp：刷新缓存失败", "err", err)
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
		if target == 0 {
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
		if target == 0 {
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
		antiad.LiftGban(sh, target)
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
	if botID != 0 && !miniCanManageBot(sh, uid, botID) {
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
		if _, err := sh.Store.Write.Exec(`DELETE FROM ad_whitelist
			WHERE bot_id=? AND chat_id=? AND user_id=?`, botID, chatID, target); err != nil {
			miniErr(w, http.StatusInternalServerError, "删除失败")
			return
		}
		if err := sh.Cache.Reload(); err != nil {
			slog.Error("miniapp：刷新缓存失败", "err", err)
		}
	default:
		miniErr(w, http.StatusBadRequest, "未知操作")
		return
	}
	miniOK(w, map[string]any{"ok": true})
}

func miniDigest(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	if !sh.IsMain(uid) {
		miniErr(w, http.StatusForbidden, "形态摘要由主管理员维护")
		return
	}
	switch miniStr(body, "action") {
	case "save":
		if err := sh.PutSetting("antiad_digest", miniStr(body, "value")); err != nil {
			miniErr(w, http.StatusInternalServerError, "保存失败")
			return
		}
	case "run":
		go antiad.RunAdDigest(sh, true)
	default:
		miniErr(w, http.StatusBadRequest, "未知操作")
		return
	}
	miniOK(w, map[string]any{"ok": true})
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

func miniLogs(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	page := miniInt(body, "page")
	if page < 1 {
		page = 1
	}
	clauseWhere, clauseArgs := miniBotsClause(sh, uid, sh.IsMain(uid))

	// 筛选与搜索。默认无筛选（前端会带 verdict=deleted 进来）：
	// 「已删除」是处置动作名前缀，而不是 verdict——放行/跳过的记录
	// 也可能带处置，按动作筛才与「消息被删了」这个用户视角一致。
	condWhere, condArgs := "", []any{}
	switch miniStr(body, "verdict") {
	case "deleted":
		condWhere += " AND action LIKE 'deleted%'"
	case "ad":
		condWhere += " AND verdict='ad'"
	case "clean":
		condWhere += " AND verdict='none'"
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
	miniOK(w, map[string]any{"logs": out, "page": page})
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
	// 流水正文为空时回查全量留底：老记录（冷判定演练分支曾不带原文）
	// 与「留底在、流水空」的记录，详情页仍要能看到原文。
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
	row := sh.Store.Read.QueryRow(`SELECT bot_id,chat_id,user_id,message_id,text
		FROM antiad_log WHERE id=?`+clauseWhere, append([]any{id}, clauseArgs...)...)
	var botID, chatID, userID, msgID int64
	var text string
	if row.Scan(&botID, &chatID, &userID, &msgID, &text) != nil {
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
	} else if !miniCanManageBot(sh, uid, botID) {
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
		if ok2, desc := antiad.LiftMute(inst, chatID, userID); !ok2 {
			miniErr(w, http.StatusBadRequest, "解除失败："+desc)
			return
		}
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
		antiad.LiftGban(sh, userID)
	default:
		miniErr(w, http.StatusBadRequest, "未知操作")
		return
	}
	miniOK(w, map[string]any{"ok": true})
}

func miniAppeals(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	page := miniInt(body, "page")
	if page < 1 {
		page = 1
	}
	clauseWhere, clauseArgs := miniBotsClause(sh, uid, sh.IsMain(uid))
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
	miniOK(w, map[string]any{"appeals": out, "page": page})
}

// miniAppealDetail 单张申诉单：全字段加兑换与网页验证记录。
func miniAppealDetail(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	id := miniInt(body, "id")
	clauseWhere, clauseArgs := miniBotsClause(sh, uid, sh.IsMain(uid))
	row := sh.Store.Read.QueryRow(`SELECT id,bot_id,user_id,status,statement,
		ai_result,ai_conf,ai_reason,ai_model,ai_cost,web_attempts,web_since,
		code,code_expires,created_at,updated_at FROM appeals
		WHERE id=?`+clauseWhere, append([]any{id}, clauseArgs...)...)
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

func miniFloat(body map[string]any, key string) float64 {
	switch v := body[key].(type) {
	case float64:
		return v
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f
	}
	return 0
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func boolToInt64(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

// miniChatTitle 通过该 bot 的实例查群标题；实例不在运行或查询失败返回空串。
// 查不到不算失败 —— bot 还没进群就添加 chat_id 是合法的使用顺序。
func miniChatTitle(sh *core.Shared, botID, chatID int64) string {
	if sh.Reg == nil {
		return ""
	}
	b, ok := sh.Reg.LookupID(botID)
	if !ok {
		return ""
	}
	raw, err := b.TG.Call("getChat", map[string]any{"chat_id": chatID})
	if err != nil {
		return ""
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			Title string `json:"title"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &resp) != nil || !resp.OK {
		return ""
	}
	return resp.Result.Title
}

// parseModelListSnap 与 parseModelList 同规则，但不依赖 bot 实例。
func parseModelListSnap(snap *store.Snapshot, text string) ([]string, error) {
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
