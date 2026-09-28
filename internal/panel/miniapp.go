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
	case "whitelist":
		miniWhitelist(sh, w, uid, body)
	case "digest":
		miniDigest(sh, w, uid, body)
	case "logs":
		miniLogs(sh, w, uid, body)
	case "appeals":
		miniAppeals(sh, w, uid, body)
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

	out := map[string]any{
		"me": me, "bots": bots, "chats": chats, "bot_settings": botSettings,
		"specs": specs, "stats": stats,
	}
	if main {
		out["global"] = snap.Settings
		out["digest"] = snap.Setting("antiad_digest")
	}
	out["whitelist"] = miniWhitelistRows(sh, uid, main)

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

		gbans := []map[string]any{}
		for _, g := range snap.Gban {
			gbans = append(gbans, map[string]any{
				"user_id": g.UserID, "reason": g.Reason,
				"src_chat": g.SrcChat, "created_at": g.CreatedAt})
		}
		out["gban"] = gbans
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
	if !sh.IsMain(uid) {
		miniErr(w, http.StatusForbidden, "只有主管理员能管理联合封禁")
		return
	}
	target := miniInt(body, "user_id")
	switch miniStr(body, "action") {
	case "add":
		if target == 0 {
			miniErr(w, http.StatusBadRequest, "user_id 无效")
			return
		}
		if err := antiad.GbanAdd(sh, target, miniStr(body, "reason"), 0, 0); err != nil {
			miniErr(w, http.StatusInternalServerError, "添加失败")
			return
		}
	case "remove":
		antiad.LiftGban(sh, target)
	default:
		miniErr(w, http.StatusBadRequest, "未知操作")
		return
	}
	miniOK(w, map[string]any{"ok": true})
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

func miniLogs(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	page := miniInt(body, "page")
	if page < 1 {
		page = 1
	}
	target := miniInt(body, "user_id")
	where, args := miniBotsClause(sh, uid, sh.IsMain(uid))
	q := []any{}
	if target != 0 {
		where = " AND user_id=?" + where
		q = append(q, target)
	}
	q = append(q, args...)
	rows, err := sh.Store.Read.Query(`SELECT id,bot_id,chat_id,user_id,verdict,
		confidence,ad_kind,action,created_at FROM antiad_log WHERE 1=1`+where+
		` ORDER BY id DESC LIMIT 20 OFFSET ?`, append(q, (page-1)*20)...)
	if err != nil {
		miniErr(w, http.StatusInternalServerError, "查询失败")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, botID, chatID, userID, at int64
		var verdict, kind, action string
		var conf float64
		if rows.Scan(&id, &botID, &chatID, &userID, &verdict, &conf, &kind,
			&action, &at) == nil {
			out = append(out, map[string]any{
				"id": id, "bot_id": botID, "chat_id": chatID, "user_id": userID,
				"verdict": verdict, "confidence": conf, "kind": kind,
				"action": action, "created_at": at,
				"view_url": antiad.LogViewURL(sh, id),
			})
		}
	}
	miniOK(w, map[string]any{"logs": out, "page": page})
}

func miniAppeals(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	page := miniInt(body, "page")
	if page < 1 {
		page = 1
	}
	where, args := miniBotsClause(sh, uid, sh.IsMain(uid))
	rows, err := sh.Store.Read.Query(`SELECT id,bot_id,user_id,status,ai_result,
		ai_conf,created_at FROM appeals WHERE 1=1`+where+
		` ORDER BY id DESC LIMIT 20 OFFSET ?`, append(args, (page-1)*20)...)
	if err != nil {
		miniErr(w, http.StatusInternalServerError, "查询失败")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, botID, userID, at int64
		var status, aiResult string
		var conf float64
		if rows.Scan(&id, &botID, &userID, &status, &aiResult, &conf, &at) == nil {
			out = append(out, map[string]any{
				"id": id, "bot_id": botID, "user_id": userID, "status": status,
				"ai_result": aiResult, "ai_conf": conf, "created_at": at,
				"detail_url": antiad.AppealDetailURL(sh, id),
			})
		}
	}
	miniOK(w, map[string]any{"appeals": out, "page": page})
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
