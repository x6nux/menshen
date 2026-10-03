package antiad

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"menshen/internal/core"
)

// ---- ④ 原文查看页（v）与申诉详情页（apv）的数据接口 ----
//
// 门槛：GET 只返回警示文案与一个 5 分钟有效的查看凭据（e + 签名 k）；
// 凭据 POST 回来、验签且未过期才返回内容。它挡的是只发 GET 的链接预览
// 与扫描器；真正的访问控制是签名链接本身。
//
// 页面外壳是 React SPA：_w 下的 GET（无 json=1）由 main 先交给
// panel.PublicShellHandler 发 static 壳，本文件只提供 ?json=1 的数据与
// POST 的内容接口。写 HTML 模板的时代已经过去。

const viewGateTTL = 5 * time.Minute

const (
	logViewWarn    = "本页包含被判定消息的原文与账号资料，属于敏感信息。请勿转发或截图外传。"
	appealViewWarn = "敏感内容，请勿转发。左侧是账号资料与留底，右侧是本单的复核与解禁码。"
)

func handleLogViewPage(sh *core.Shared, w http.ResponseWriter, r *http.Request, rt webRoute) {
	if !webSigOK(sh, fmt.Sprintf("v:%d", rt.id), rt.sig) {
		writeWebJSON(w, http.StatusNotFound, map[string]any{"error": "链接无效或已被替换。"})
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Query().Get("json") == "1":
		writeWebJSON(w, http.StatusOK, map[string]any{
			"gate": viewGate(sh, "原文与理由", logViewWarn, rt.id),
		})
	case r.Method == http.MethodPost:
		if !viewPostOK(sh, w, r, rt.id) {
			return
		}
		data, ok := logViewDataOf(sh, rt.id)
		if !ok {
			writeWebJSON(w, http.StatusNotFound, map[string]any{"error": "记录不存在。"})
			return
		}
		writeWebJSON(w, http.StatusOK, map[string]any{"view": data})
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleAppealDetailPage(sh *core.Shared, w http.ResponseWriter, r *http.Request, rt webRoute) {
	if !webSigOK(sh, fmt.Sprintf("apv:%d", rt.id), rt.sig) {
		writeWebJSON(w, http.StatusNotFound, map[string]any{"error": "链接无效或已被替换。"})
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Query().Get("json") == "1":
		writeWebJSON(w, http.StatusOK, map[string]any{
			"gate": viewGate(sh, "申诉详情", appealViewWarn, rt.id),
		})
	case r.Method == http.MethodPost:
		if !viewPostOK(sh, w, r, rt.id) {
			return
		}
		data, ok := appealViewDataOf(sh, rt.id)
		if !ok {
			writeWebJSON(w, http.StatusNotFound, map[string]any{"error": "申诉单不存在。"})
			return
		}
		writeWebJSON(w, http.StatusOK, map[string]any{"view": data})
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// viewGate 生成门槛页数据：查看按钮的说明与一次性凭据。
func viewGate(sh *core.Shared, title, warn string, id int64) map[string]any {
	exp := time.Now().Add(viewGateTTL).Unix()
	return map[string]any{
		"title": title, "warn": warn,
		"exp": exp, "k": logViewPostSig(sh, id, exp),
		"ttl_seconds": int64(viewGateTTL / time.Second),
	}
}

// viewPostOK 校验查看请求里的凭据（JSON 体 {e,k}）：e 未过期且 k 是 vp 签名。
// 失败时已写好 404 响应。
func viewPostOK(sh *core.Shared, w http.ResponseWriter, r *http.Request, id int64) bool {
	var body struct {
		E int64  `json:"e"`
		K string `json:"k"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).
		Decode(&body); err != nil {
		writeWebJSON(w, http.StatusBadRequest, map[string]any{"error": "请求体无法解析。"})
		return false
	}
	if body.E < time.Now().Unix() || !webSigOK(sh, fmt.Sprintf("vp:%d:%d", id, body.E), body.K) {
		writeWebJSON(w, http.StatusNotFound, map[string]any{
			"error": "查看凭据已失效，请刷新页面重试。"})
		return false
	}
	return true
}

// logViewDataOf 组装一条判定记录的完整可查信息；记录不存在时返回 false。
func logViewDataOf(sh *core.Shared, id int64) (map[string]any, bool) {
	row, ok := LoadAdLog(sh.Store, id)
	if !ok {
		return nil, false
	}
	record := map[string]any{
		"id": row.ID, "user_id": row.UserID, "user_name": row.UserName,
		"chat_id": row.ChatID, "chat": fmt.Sprintf("%d", row.ChatID),
		"message_id": row.MessageID,
		"text":       row.Text, "verdict": row.Verdict, "confidence": row.Confidence,
		"decider": row.Decider, "kind": row.Kind,
		"action": row.Action, "action_label": ActionLabel(row.Action),
		"reason": row.Reason, "created": formatTS(sh, row.CreatedAt),
	}
	member := map[string]any{"joined": "", "msgs": int64(0), "hits": int64(0)}
	if c, ok := sh.Cache.Snap().ChatConf(row.BotID, row.ChatID); ok && c.Title != "" {
		record["chat"] = c.Title + "（" + fmt.Sprintf("%d", row.ChatID) + "）"
	}
	var joined, msgs, hits int64
	if err := sh.Store.Read.QueryRow(`SELECT joined_at,msg_count,ad_hits
		FROM group_members WHERE chat_id=? AND user_id=?`,
		row.ChatID, row.UserID).Scan(&joined, &msgs, &hits); err == nil {
		if joined > 0 {
			member["joined"] = formatTS(sh, joined)
		} else {
			member["joined"] = "部署前已在群（未知）"
		}
		member["msgs"], member["hits"] = msgs, hits
	}

	history := []map[string]any{}
	rows, err := sh.Store.Read.Query(`SELECT message_id,text,at FROM group_messages
		WHERE chat_id=? AND user_id=? ORDER BY at DESC LIMIT 100`,
		row.ChatID, row.UserID)
	if err == nil {
		for rows.Next() {
			var messageID, at int64
			var text string
			if rows.Scan(&messageID, &text, &at) == nil {
				history = append(history, map[string]any{
					"message_id": messageID, "text": text, "at": at,
					"time":    formatTS(sh, at),
					"blocked": messageID == row.MessageID,
				})
			}
		}
		rows.Close()
	}

	logs := []map[string]any{}
	rows, err = sh.Store.Read.Query(`SELECT id,verdict,confidence,action,created_at
		FROM antiad_log WHERE chat_id=? AND user_id=? ORDER BY id DESC LIMIT 30`,
		row.ChatID, row.UserID)
	if err == nil {
		for rows.Next() {
			var (
				lid, at int64
				verdict string
				conf    float64
				action  string
			)
			if rows.Scan(&lid, &verdict, &conf, &action, &at) == nil {
				logs = append(logs, map[string]any{
					"id": lid, "verdict": verdict, "confidence": conf,
					"action": action, "action_label": ActionLabel(action),
					"at": at, "time": formatTS(sh, at),
				})
			}
		}
		rows.Close()
	}

	return map[string]any{
		"record": record, "member": member,
		"history": history, "logs": logs,
	}, true
}

// appealViewDataOf 组装一张申诉单与本人资料的全部字段；单子不存在返回 false。
func appealViewDataOf(sh *core.Shared, id int64) (appealViewData, bool) {
	ap, ok := loadAppealByID(sh.Store, id)
	if !ok {
		return appealViewData{}, false
	}
	data := appealViewData{
		ID: ap.ID, UID: ap.UserID, Status: appealStatusLabel(ap.Status),
		Statement: ap.Statement, AIResult: appealAIResultLabel(ap.AIResult),
		AIConf: ap.AIConf * 100, AIReason: ap.AIReason, AIModel: ap.AIModel,
		WebAttempts: ap.WebAttempts, Code: ap.Code,
		Created: formatTS(sh, ap.CreatedAt),
		// 数组字段先给空切片：nil 切片会序列化成 null，前端 length/map
		// 直接抛错把整页卸载（线上真实事故：申诉详情点「查看内容」白屏）。
		Limits:      []appealViewPenalty{},
		Penalties:   []appealViewPenalty{},
		History:     []appealViewMsg{},
		HistoryMore: []appealViewMsg{},
		Logs:        []appealViewLog{},
		Checks:      []webCheckView{},
		Strong:      []relatedView{},
		Weak:        []relatedView{},
	}
	if ap.CodeExpires != 0 {
		data.CodeExpires = formatTS(sh, ap.CodeExpires)
	}
	loadAppealDossier(sh, ap, &data)
	return data, true
}

// relatedMark 给关联账号打标注：联合封禁 / 命中次数 / 无记录。
func relatedMark(sh *core.Shared, uid int64) string {
	if _, ok := sh.Cache.Snap().Gban[uid]; ok {
		return "🚫 联合封禁中"
	}
	var hits int64
	sh.Store.Read.QueryRow(`SELECT COALESCE(SUM(ad_hits),0) FROM group_members
		WHERE user_id=?`, uid).Scan(&hits)
	if hits > 0 {
		return fmt.Sprintf("命中 %d 次", hits)
	}
	return "无记录"
}

func formatTS(sh *core.Shared, unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).In(sh.Cache.Snap().Location()).Format("2006-01-02 15:04")
}

// ---- 页面数据（JSON 形状）----
//
// appealViewData 是申诉详情页的内容（含本人资料与各种留底），由
// adview_dossier.go 的 loadAppealDossier 填充；json 标签即接口契约。

type appealViewData struct {
	// 主栏：这一张申诉单本身。
	ID          int64   `json:"id"`
	UID         int64   `json:"uid"`
	Status      string  `json:"status"`
	Statement   string  `json:"statement"`
	AIResult    string  `json:"ai_result"`
	AIConf      float64 `json:"ai_conf"`
	AIReason    string  `json:"ai_reason"`
	AIModel     string  `json:"ai_model"`
	WebAttempts int64   `json:"web_attempts"`
	Code        string  `json:"code"`
	CodeExpires string  `json:"code_expires"`
	Created     string  `json:"created"`

	// 左栏：用户资料与各种留底。
	UName     string              `json:"u_name"`
	Bot       string              `json:"bot"`
	FirstSeen string              `json:"first_seen"`
	Joined    string              `json:"joined"`
	LastMsg   string              `json:"last_msg"`
	Msgs      int64               `json:"msgs"`
	Hits      int64               `json:"hits"`
	Chats     int64               `json:"chats"`
	GBan      string              `json:"gban"`
	Limits    []appealViewPenalty `json:"limits"`
	Penalties []appealViewPenalty `json:"penalties"`
	// History 是留底发言，最多 dossierMsgLimit 条；太长时后面那截放
	// HistoryMore 折起来，HistoryCount 是总条数。
	History      []appealViewMsg `json:"history"`
	HistoryMore  []appealViewMsg `json:"history_more"`
	HistoryCount int             `json:"history_count"`
	Logs         []appealViewLog `json:"logs"`
	Checks       []webCheckView  `json:"checks"`
	Strong       []relatedView   `json:"strong"`
	Weak         []relatedView   `json:"weak"`
}

type appealViewPenalty struct {
	Type   string `json:"type"`
	Label  string `json:"label"` // 中文类型名
	ChatID int64  `json:"chat_id"`
	Chat   string `json:"chat"`
	Text   string `json:"text"`
	Reason string `json:"reason"`
	At     int64  `json:"at"`
	Time   string `json:"time"`
}

type appealViewMsg struct {
	Chat    string `json:"chat"`
	Text    string `json:"text"`
	At      int64  `json:"at"`
	Time    string `json:"time"`
	Mark    string `json:"mark"` // 被拦 / 演练命中
	Blocked bool   `json:"blocked"`
}

type appealViewLog struct {
	ID          int64   `json:"id"`
	ChatID      int64   `json:"chat_id"`
	Chat        string  `json:"chat"`
	Verdict     string  `json:"verdict"`
	Conf        float64 `json:"confidence"`
	Action      string  `json:"action"`
	ActionLabel string  `json:"action_label"`
	Reason      string  `json:"reason"`
	At          int64   `json:"at"`
	Time        string  `json:"time"`
}

type webCheckView struct {
	Result string `json:"result"`
	Flags  string `json:"flags"`
	IP     string `json:"ip"`
	FP     string `json:"fp"`
	UA     string `json:"ua"`
	At     int64  `json:"at"`
	Time   string `json:"time"`
}

type relatedView struct {
	UID  int64  `json:"uid"`
	Mark string `json:"mark"`
}
