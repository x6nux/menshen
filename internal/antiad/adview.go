package antiad

import (
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"menshen/internal/core"
)

// ---- ④ 原文查看页（v）与申诉详情页（apv）----
//
// 门槛：GET 只返回警示横幅和一个「查看内容」按钮；按钮 POST 一个签名
// 表单（e = 5 分钟后的时间戳，k = vp:<id>:<e> 的签名），服务端验签且未
// 过期才渲染内容。它挡的是只发 GET 的链接预览与扫描器；真正的访问控制
// 是签名链接本身。

const viewGateTTL = 5 * time.Minute

func handleLogViewPage(sh *core.Shared, w http.ResponseWriter, r *http.Request, rt webRoute) {
	if !webSigOK(sh, fmt.Sprintf("v:%d", rt.id), rt.sig) {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeHTMLHeaders(w, "")
		fmt.Fprint(w, gatePage(sh, "原文与理由",
			"/_w/v/"+strconv.FormatInt(rt.id, 10)+"/"+rt.sig, rt.id))
	case http.MethodPost:
		if !viewPostOK(sh, r, rt.id) {
			http.NotFound(w, r)
			return
		}
		renderLogView(sh, w, rt.id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleAppealDetailPage(sh *core.Shared, w http.ResponseWriter, r *http.Request, rt webRoute) {
	if !webSigOK(sh, fmt.Sprintf("apv:%d", rt.id), rt.sig) {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeHTMLHeaders(w, "")
		fmt.Fprint(w, gatePage(sh, "申诉详情",
			"/_w/apv/"+strconv.FormatInt(rt.id, 10)+"/"+rt.sig, rt.id))
	case http.MethodPost:
		if !viewPostOK(sh, r, rt.id) {
			http.NotFound(w, r)
			return
		}
		renderAppealView(sh, w, rt.id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// viewPostOK 校验查看页的 POST 表单：e 未过期且 k 是 vp 签名。
func viewPostOK(sh *core.Shared, r *http.Request, id int64) bool {
	if err := r.ParseForm(); err != nil {
		return false
	}
	exp, err := strconv.ParseInt(r.PostFormValue("e"), 10, 64)
	if err != nil || exp < time.Now().Unix() {
		return false
	}
	return webSigOK(sh, fmt.Sprintf("vp:%d:%d", id, exp), r.PostFormValue("k"))
}

// renderLogView 渲染一条判定记录的全部可查信息。
func renderLogView(sh *core.Shared, w http.ResponseWriter, id int64) {
	row, ok := LoadAdLog(sh.Store, id)
	if !ok {
		http.NotFound(w, nil)
		return
	}
	data := logViewData{Row: row, Chat: strconv.FormatInt(row.ChatID, 10),
		UserName: row.UserName, Created: formatTS(sh, row.CreatedAt)}

	if c, ok := sh.Cache.Snap().ChatConf(row.BotID, row.ChatID); ok && c.Title != "" {
		data.Chat = c.Title + "（" + data.Chat + "）"
	}
	var joined, msgs, hits int64
	if err := sh.Store.Read.QueryRow(`SELECT joined_at,msg_count,ad_hits
		FROM group_members WHERE chat_id=? AND user_id=?`,
		row.ChatID, row.UserID).Scan(&joined, &msgs, &hits); err == nil {
		if joined > 0 {
			data.Joined = formatTS(sh, joined)
		} else {
			data.Joined = "部署前已在群（未知）"
		}
		data.Msgs, data.Hits = msgs, hits
	}

	rows, err := sh.Store.Read.Query(`SELECT message_id,text,at FROM group_messages
		WHERE chat_id=? AND user_id=? ORDER BY at DESC LIMIT 100`,
		row.ChatID, row.UserID)
	if err == nil {
		for rows.Next() {
			var h logMsg
			if rows.Scan(&h.MessageID, &h.Text, &h.At) == nil {
				h.Blocked = h.MessageID == row.MessageID
				h.Time = formatTS(sh, h.At)
				data.History = append(data.History, h)
			}
		}
		rows.Close()
	}

	rows, err = sh.Store.Read.Query(`SELECT id,verdict,confidence,action,created_at
		FROM antiad_log WHERE chat_id=? AND user_id=? ORDER BY id DESC LIMIT 30`,
		row.ChatID, row.UserID)
	if err == nil {
		for rows.Next() {
			var l logBrief
			if rows.Scan(&l.ID, &l.Verdict, &l.Conf, &l.Action, &l.At) == nil {
				l.Time = formatTS(sh, l.At)
				l.ActionLabel = ActionLabel(l.Action)
				data.Logs = append(data.Logs, l)
			}
		}
		rows.Close()
	}

	writeHTMLHeaders(w, "")
	renderTemplate(w, logViewTmpl, data)
}

// renderAppealView 渲染一张申诉单的全部字段。
func renderAppealView(sh *core.Shared, w http.ResponseWriter, id int64) {
	ap, ok := loadAppealByID(sh.Store, id)
	if !ok {
		http.NotFound(w, nil)
		return
	}
	data := appealViewData{
		ID: ap.ID, UID: ap.UserID, Status: ap.Status,
		Statement: ap.Statement, AIResult: appealAIResultLabel(ap.AIResult),
		AIConf: ap.AIConf * 100, AIReason: ap.AIReason, AIModel: ap.AIModel,
		WebAttempts: ap.WebAttempts, Code: ap.Code,
		Created: formatTS(sh, ap.CreatedAt),
	}
	if ap.CodeExpires != 0 {
		data.CodeExpires = formatTS(sh, ap.CodeExpires)
	}

	rows, err := sh.Store.Read.Query(`SELECT type,chat_id,text,reason,at FROM (
			SELECT 'join_profile' AS type, chat_id, '' AS text, reason, created_at AS at
			FROM join_mutes WHERE bot_id=? AND user_id=?
			UNION ALL
			SELECT 'message', chat_id, text, reason, created_at
			FROM antiad_log WHERE bot_id=? AND user_id=?
			AND action IN ('deleted_muted','muted','deleted_banned','banned')
		) ORDER BY at DESC LIMIT 20`, ap.BotID, ap.UserID, ap.BotID, ap.UserID)
	if err == nil {
		for rows.Next() {
			var p appealViewPenalty
			if rows.Scan(&p.Type, &p.ChatID, &p.Text, &p.Reason, &p.At) == nil {
				p.Time = formatTS(sh, p.At)
				data.Penalties = append(data.Penalties, p)
			}
		}
		rows.Close()
	}

	var fp, ip string
	rows, err = sh.Store.Read.Query(`SELECT result,flags,created_at FROM web_checks
		WHERE appeal_id=? ORDER BY id DESC LIMIT 20`, ap.ID)
	if err == nil {
		for rows.Next() {
			var c webCheckView
			if rows.Scan(&c.Result, &c.Flags, &c.At) == nil {
				c.Time = formatTS(sh, c.At)
				data.Checks = append(data.Checks, c)
			}
		}
		rows.Close()
	}
	// 关联账号取最近一次验证的指纹与 IP。
	sh.Store.Read.QueryRow(`SELECT fp,ip FROM web_checks WHERE appeal_id=?
		ORDER BY id DESC LIMIT 1`, ap.ID).Scan(&fp, &ip)
	strong, weak := relatedAccounts(sh, fp, ip, ap.UserID)
	for _, uid := range strong {
		data.Strong = append(data.Strong, relatedView{UID: uid, Mark: relatedMark(sh, uid)})
	}
	for _, uid := range weak {
		data.Weak = append(data.Weak, relatedView{UID: uid, Mark: relatedMark(sh, uid)})
	}

	writeHTMLHeaders(w, "")
	renderTemplate(w, appealViewTmpl, data)
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

// renderTemplate 渲染模板；执行失败时已经写出部分内容，只能记日志。
func renderTemplate(w http.ResponseWriter, tmpl *template.Template, data any) {
	if err := tmpl.Execute(w, data); err != nil {
		slog.Error("查看页模板渲染失败", "err", err)
	}
}

// ---- 页面数据 ----

type logViewData struct {
	Row      AdLogRow
	Chat     string
	UserName string
	Created  string
	Joined   string
	Msgs     int64
	Hits     int64
	History  []logMsg
	Logs     []logBrief
}

type logMsg struct {
	MessageID int64
	Text      string
	At        int64
	Time      string
	Blocked   bool
}

type logBrief struct {
	ID          int64
	Verdict     string
	Conf        float64
	Action      string
	ActionLabel string
	At          int64
	Time        string
}

type appealViewData struct {
	ID          int64
	UID         int64
	Status      string
	Statement   string
	AIResult    string
	AIConf      float64
	AIReason    string
	AIModel     string
	WebAttempts int64
	Code        string
	CodeExpires string
	Created     string
	Penalties   []appealViewPenalty
	Checks      []webCheckView
	Strong      []relatedView
	Weak        []relatedView
}

type appealViewPenalty struct {
	Type   string
	ChatID int64
	Text   string
	Reason string
	At     int64
	Time   string
}

type webCheckView struct {
	Result string
	Flags  string
	At     int64
	Time   string
}

type relatedView struct {
	UID  int64
	Mark string
}

// ---- 模板 ----

const viewStyle = `body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;
 margin:0 auto;padding:24px;max-width:720px;line-height:1.6;color:#1a1a1a;background:#fafafa}
h1{font-size:20px}h2{font-size:16px;margin-top:24px;border-bottom:1px solid #e5e5e5;padding-bottom:4px}
.card{background:#fff;border:1px solid #e5e5e5;border-radius:10px;padding:16px;margin-bottom:16px}
.warn{color:#8a5a00;background:#fff8e6;border:1px solid #f0d9a8;border-radius:8px;
 padding:10px 12px;font-size:13px;margin-bottom:16px}
code,pre{font-family:ui-monospace,Menlo,monospace;background:#f2f2f2;border-radius:6px;
 padding:2px 6px;font-size:13px;word-break:break-all}
pre{padding:10px;white-space:pre-wrap}
table{border-collapse:collapse;width:100%;font-size:14px}
td,th{border:1px solid #eee;padding:6px 8px;text-align:left;vertical-align:top}
.muted{color:#888;font-size:13px}.blocked{background:#fff0f0}
button{font-size:15px;padding:10px 18px;border-radius:8px;border:1px solid #ccc;
 background:#fff;cursor:pointer}`

func gatePage(sh *core.Shared, title, action string, id int64) string {
	exp := time.Now().Add(viewGateTTL).Unix()
	k := logViewPostSig(sh, id, exp)
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>` + title + `</title><style>` + viewStyle + `</style></head><body>
<h1>` + title + `</h1>
<div class="warn">本页包含被判定消息的原文与账号资料，属于敏感信息。请勿转发或截图外传。</div>
<form method="post" action="` + action + `">
<input type="hidden" name="e" value="` + strconv.FormatInt(exp, 10) + `">
<input type="hidden" name="k" value="` + k + `">
<button type="submit">查看内容</button>
</form>
<p class="muted">链接只对持有者有效；表单 5 分钟内有效。</p>
</body></html>`
}

// viewFuncs 是查看页模板的辅助函数：置信度 0..1 渲染成百分数。
var viewFuncs = template.FuncMap{
	"pct": func(f float64) float64 { return f * 100 },
}

var logViewTmpl = template.Must(template.New("v").Funcs(viewFuncs).Parse(
	`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>原文与理由</title><style>` + viewStyle + `</style></head><body>
<h1>原文与理由</h1>
<div class="warn">敏感内容，请勿转发。页面仅供管理员核对判定依据。</div>
<div class="card">
<p>记录：<code>#{{.Row.ID}}</code> ｜ 时间：{{.Created}}</p>
<p>用户：<code>{{.Row.UserID}}</code>{{if .UserName}}（{{.UserName}}）{{end}}</p>
<p>群组：{{.Chat}}</p>
<p>入群时间：{{.Joined}} ｜ 群内发言：{{.Msgs}} ｜ 历史命中：{{.Hits}}</p>
</div>
<div class="card">
<h2>判定</h2>
<p>结论：{{.Row.Verdict}} ｜ 类型：{{.Row.Kind}} ｜ 置信度：{{printf "%.0f" (pct .Row.Confidence)}}%</p>
<p>来源：{{.Row.Decider}}{{if .Row.Reason}} ｜ 理由：{{.Row.Reason}}{{end}}</p>
<p>处置：{{.Row.Action}}</p>
<h2>被拦原文</h2>
<pre>{{.Row.Text}}</pre>
</div>
{{if .History}}
<div class="card"><h2>该群最近留底（100 条）</h2>
<table><tr><th>时间</th><th>内容</th></tr>
{{range .History}}<tr{{if .Blocked}} class="blocked"{{end}}><td class="muted">{{.Time}}</td><td>{{.Text}}{{if .Blocked}} <b>← 被拦</b>{{end}}</td></tr>
{{end}}</table></div>
{{end}}
{{if .Logs}}
<div class="card"><h2>该群最近判定记录（30 条）</h2>
<table><tr><th>#</th><th>时间</th><th>结论</th><th>处置</th></tr>
{{range .Logs}}<tr><td>{{.ID}}</td><td class="muted">{{.Time}}</td>
<td>{{.Verdict}} {{printf "%.0f" (pct .Conf)}}%</td><td>{{.ActionLabel}}</td></tr>
{{end}}</table></div>
{{end}}
</body></html>`))

var appealViewTmpl = template.Must(template.New("apv").Funcs(viewFuncs).Parse(
	`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>申诉详情</title><style>` + viewStyle + `</style></head><body>
<h1>申诉详情</h1>
<div class="warn">敏感内容，请勿转发。</div>
<div class="card">
<p>申诉单：<code>#{{.ID}}</code> ｜ 状态：{{.Status}}</p>
<p>申诉人：<code>{{.UID}}</code> ｜ 提交时间：{{.Created}}</p>
<p>申诉理由：{{if .Statement}}{{.Statement}}{{else}}（未填写）{{end}}</p>
</div>
<div class="card">
<h2>AI 复核</h2>
<p>{{.AIResult}}{{if .AIModel}} ｜ 模型 <code>{{.AIModel}}</code>{{end}}
{{if .AIConf}} ｜ 置信度 {{printf "%.0f" .AIConf}}%{{end}}</p>
{{if .AIReason}}<p>理由：{{.AIReason}}</p>{{end}}
<h2>网页验证</h2>
<p>尝试次数：{{.WebAttempts}}</p>
{{if .Code}}<p>解禁码：<code>{{.Code}}</code>{{if .CodeExpires}}（有效期至 {{.CodeExpires}}）{{end}}</p>{{end}}
{{if .Checks}}<table><tr><th>时间</th><th>结果</th><th>信号</th></tr>
{{range .Checks}}<tr><td class="muted">{{.Time}}</td><td>{{.Result}}</td><td>{{.Flags}}</td></tr>{{end}}
</table>{{end}}
</div>
{{if .Penalties}}
<div class="card"><h2>涉及的处罚</h2>
<table><tr><th>类型</th><th>群</th><th>原文</th><th>理由</th></tr>
{{range .Penalties}}<tr><td>{{.Type}}</td><td><code>{{.ChatID}}</code></td>
<td>{{if .Text}}<code>{{.Text}}</code>{{end}}</td><td>{{.Reason}}</td></tr>
{{end}}</table></div>
{{end}}
{{if .Strong}}<div class="card"><h2>强关联账号（同指纹）</h2>
{{range .Strong}}<p><code>{{.UID}}</code> {{.Mark}}</p>{{end}}</div>{{end}}
{{if .Weak}}<div class="card"><h2>弱关联账号（30 天内同 IP，仅供参考）</h2>
{{range .Weak}}<p><code>{{.UID}}</code> {{.Mark}}</p>{{end}}</div>{{end}}
</body></html>`))
