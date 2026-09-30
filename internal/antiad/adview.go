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
		ID: ap.ID, UID: ap.UserID, Status: appealStatusLabel(ap.Status),
		Statement: ap.Statement, AIResult: appealAIResultLabel(ap.AIResult),
		AIConf: ap.AIConf * 100, AIReason: ap.AIReason, AIModel: ap.AIModel,
		WebAttempts: ap.WebAttempts, Code: ap.Code,
		Created: formatTS(sh, ap.CreatedAt),
	}
	if ap.CodeExpires != 0 {
		data.CodeExpires = formatTS(sh, ap.CodeExpires)
	}
	loadAppealDossier(sh, ap, &data)

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
	// 主栏：这一张申诉单本身。
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

	// 左栏：用户资料与各种留底。
	UName     string // 判定时记下的昵称
	Bot       string
	FirstSeen string
	Joined    string
	LastMsg   string
	Msgs      int64
	Hits      int64
	Chats     int64
	GBan      string
	Limits    []appealViewPenalty // 生效中
	Penalties []appealViewPenalty // 最近处置（含历史）
	// History 是留底发言，最多 dossierMsgLimit 条；太长时后面那截放
	// HistoryMore 折起来，HistoryCount 是总条数。
	History      []appealViewMsg
	HistoryMore  []appealViewMsg
	HistoryCount int
	Logs         []appealViewLog // 判定流水
	Checks       []webCheckView
	Strong       []relatedView
	Weak         []relatedView
}

type appealViewPenalty struct {
	Type   string
	Label  string // 中文类型名
	ChatID int64
	Chat   string
	Text   string
	Reason string
	At     int64
	Time   string
}

type appealViewMsg struct {
	Chat    string
	Text    string
	At      int64
	Time    string
	Mark    string // 被拦 / 演练命中
	Blocked bool
}

type appealViewLog struct {
	ID          int64
	ChatID      int64
	Chat        string
	Verdict     string
	Conf        float64
	Action      string
	ActionLabel string
	Reason      string
	At          int64
	Time        string
}

type webCheckView struct {
	Result string
	Flags  string
	IP     string
	FP     string
	UA     string
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

// dossierStyle 是申诉详情页的额外样式：左栏资料 + 右栏申诉单两列。
// 窄屏（<960px）退化成一列，申诉单在前、资料在后。
const dossierStyle = `
body{max-width:1200px}
.wrap{display:flex;gap:16px;align-items:flex-start}
.main{flex:1 1 auto;min-width:0}
.side{flex:0 0 440px;order:-1;min-width:0}
.side .card{padding:14px;margin-bottom:12px}
.side h2{margin-top:0;font-size:15px}
.kv{font-size:14px}.kv div{margin-bottom:4px}
ul.plain{margin:0;padding-left:18px}
ul.plain li{margin-bottom:9px;font-size:13.5px}
ul.plain li.hit{background:#fff0f0;border-radius:4px}
.txt{margin-top:2px;word-break:break-word}
.sub{font-size:12.5px;color:#555;margin-top:3px;word-break:break-word}
.ts{font-family:ui-monospace,Menlo,monospace;font-size:12px;color:#888}
.badge{display:inline-block;font-size:11px;padding:1px 6px;border-radius:6px;
 background:#eef1f5;color:#445;vertical-align:1px}
.badge.on{background:#fdecec;color:#a22}
.side code{font-size:12px}
@media(max-width:960px){.wrap{display:block}.side{width:auto}}
`

var appealViewTmpl = template.Must(template.New("apv").Funcs(viewFuncs).Parse(
	`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>申诉详情</title><style>` + viewStyle + dossierStyle + `</style></head><body>
<h1>申诉详情</h1>
<div class="warn">敏感内容，请勿转发。左侧是账号资料与留底，右侧是本单的复核与解禁码。</div>
<div class="wrap">
<main class="main">
<div class="card">
<p>申诉单：<code>#{{.ID}}</code> ｜ 状态：{{.Status}}</p>
<p>申诉人：<code>{{.UID}}</code>{{if .UName}}（{{.UName}}）{{end}} ｜ 提交时间：{{.Created}}</p>
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
<p class="muted">验证与发言记录见左侧资料栏。</p>
</div>
</main>
<aside class="side">
<div class="card"><h2>账号信息</h2><div class="kv">
<div>用户 ID：<code>{{.UID}}</code></div>
{{if .UName}}<div>判定时昵称：{{.UName}}</div>{{end}}
<div>所属 bot：{{.Bot}}</div>
{{if .FirstSeen}}<div>首次见到：{{.FirstSeen}}</div>{{end}}
{{if .Joined}}<div>最早入群：{{.Joined}}</div>{{end}}
{{if .LastMsg}}<div>最近发言：{{.LastMsg}}</div>{{end}}
<div>群内发言：{{.Msgs}} 条 ｜ 历史命中：{{.Hits}} 次</div>
<div>涉及群组：{{.Chats}} 个</div>
{{if .GBan}}<div>联合封禁：{{.GBan}}</div>{{end}}
<div>生效中限制：{{len .Limits}} 条</div>
</div></div>
{{if .Limits}}<div class="card"><h2>当前生效限制</h2><ul class="plain">
{{range .Limits}}<li><span class="badge on">生效中</span> {{.Label}} · {{.Chat}}
{{if .Time}}<span class="ts">{{.Time}}</span>{{end}}
{{if .Reason}}<div class="sub">理由：{{.Reason}}</div>{{end}}
{{if .Text}}<div class="sub">原消息：{{.Text}}</div>{{end}}</li>
{{end}}</ul></div>{{end}}
{{if .Penalties}}<div class="card"><h2>历史处罚（最近 {{len .Penalties}} 条）</h2><ul class="plain">
{{range .Penalties}}<li><span class="badge">历史</span> {{.Label}} · {{.Chat}}
<span class="ts">{{.Time}}</span>
{{if .Reason}}<div class="sub">理由：{{.Reason}}</div>{{end}}
{{if .Text}}<div class="sub">原消息：{{.Text}}</div>{{end}}</li>
{{end}}</ul></div>{{end}}
{{if .History}}<div class="card"><h2>群内留底发言（最近 {{.HistoryCount}} 条）</h2><ul class="plain">
{{range .History}}<li{{if .Blocked}} class="hit"{{end}}><span class="ts">{{.Time}}</span>{{if .Chat}} <span class="muted">{{.Chat}}</span>{{end}}
<div class="txt">{{.Text}}{{if .Mark}} <b>← {{.Mark}}</b>{{end}}</div></li>
{{end}}</ul>
{{if .HistoryMore}}<details><summary>展开其余 {{len .HistoryMore}} 条</summary><ul class="plain">
{{range .HistoryMore}}<li{{if .Blocked}} class="hit"{{end}}><span class="ts">{{.Time}}</span>{{if .Chat}} <span class="muted">{{.Chat}}</span>{{end}}
<div class="txt">{{.Text}}{{if .Mark}} <b>← {{.Mark}}</b>{{end}}</div></li>
{{end}}</ul></details>{{end}}</div>{{end}}
{{if .Logs}}<div class="card"><h2>判定流水（最近 {{len .Logs}} 条）</h2><ul class="plain">
{{range .Logs}}<li><span class="ts">{{.Time}}</span> {{.Chat}} · {{.Verdict}} {{printf "%.0f" (pct .Conf)}}% → {{.ActionLabel}}
{{if .Reason}}<div class="sub">理由：{{.Reason}}</div>{{end}}</li>
{{end}}</ul></div>{{end}}
{{if .Checks}}<div class="card"><h2>网页验证记录（最近 {{len .Checks}} 条）</h2><ul class="plain">
{{range .Checks}}<li><span class="ts">{{.Time}}</span> {{.Result}}
{{if .IP}}<div class="sub">IP：<code>{{.IP}}</code>{{if .FP}} ｜ 指纹：<code>{{.FP}}</code>{{end}}</div>{{end}}
{{if .Flags}}<div class="sub">信号：{{.Flags}}</div>{{end}}
{{if .UA}}<div class="sub">UA：{{.UA}}</div>{{end}}</li>
{{end}}</ul></div>{{end}}
{{if or .Strong .Weak}}<div class="card"><h2>关联账号</h2>
{{if .Strong}}<div class="sub">强关联（同指纹）</div><ul class="plain">
{{range .Strong}}<li><code>{{.UID}}</code> {{.Mark}}</li>{{end}}</ul>{{end}}
{{if .Weak}}<div class="sub">弱关联（30 天内同 IP，仅供参考）</div><ul class="plain">
{{range .Weak}}<li><code>{{.UID}}</code> {{.Mark}}</li>{{end}}</ul>{{end}}
</div>{{end}}
</aside>
</div>
</body></html>`))
