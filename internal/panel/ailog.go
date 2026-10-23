package panel

// 模型调用流水页（网页版 / Mini App）的数据面：读 ai_log 表
//（写入在 antiad/ailog.go，随每次 aiCall 落一行）。
//
// 与运行日志同权：只对主管理员开放。流水里带着所有租户的群消息原文与
// 判定上下文，次级管理员没有理由看到不属于自己 bot 的调用。

import (
	"net/http"
	"strings"

	"menshen/internal/billing"
	"menshen/internal/core"
)

// aiLogPageSize 是流水每页条数。列表行不带 input/output 全文（那是详情
// 接口的事），50 条一屏与运行日志页一致。
const aiLogPageSize = 50

// miniAILog 返回模型调用流水的一页（新→旧）。q 匹配 model / upstream /
// status / error 的子串；status 取 ""（全部）/ "ok"（成功）/ "err"（失败）。
func miniAILog(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	if !sh.IsMain(uid) {
		miniErr(w, http.StatusForbidden, "模型调用流水只有主管理员可以查看")
		return
	}
	page := clampPage(miniInt(body, "page"))
	q := strings.TrimSpace(miniStr(body, "q"))
	status := miniStr(body, "status")

	where := " WHERE 1=1"
	args := []any{}
	if q != "" {
		where += ` AND (model LIKE ? ESCAPE '\' OR upstream LIKE ? ESCAPE '\' OR
			status LIKE ? ESCAPE '\' OR error LIKE ? ESCAPE '\')`
		pat := "%" + likeEscape(q) + "%"
		args = append(args, pat, pat, pat, pat)
	}
	switch status {
	case "ok":
		where += " AND status = 'ok'"
	case "err":
		where += " AND status != 'ok'"
	}

	var total int64
	if err := sh.Store.Read.QueryRow("SELECT COUNT(*) FROM ai_log"+where, args...).
		Scan(&total); err != nil {
		miniErr(w, http.StatusInternalServerError, "统计失败")
		return
	}
	args = append(args, aiLogPageSize, (page-1)*aiLogPageSize)
	rows, err := sh.Store.Read.Query(`SELECT id,ts,endpoint,model,upstream,status,
		latency_ms,prompt_tokens,completion_tokens,cached_tokens,quota_cost,error
		FROM ai_log`+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		miniErr(w, http.StatusInternalServerError, "查询失败")
		return
	}
	defer rows.Close()
	logs := make([]map[string]any, 0, aiLogPageSize)
	for rows.Next() {
		var id, ts, latency, pt, ct, cached, cost int64
		var endpoint, model, upstream, st, errText string
		if err := rows.Scan(&id, &ts, &endpoint, &model, &upstream, &st,
			&latency, &pt, &ct, &cached, &cost, &errText); err != nil {
			continue
		}
		logs = append(logs, map[string]any{
			"id": id, "ts": ts, "endpoint": endpoint, "model": model,
			"upstream": upstream, "status": st, "latency_ms": latency,
			"prompt_tokens": pt, "completion_tokens": ct,
			"cached_tokens": cached, "quota_cost": cost,
			"cost_text": billing.FormatUSDFine(cost), "error": errText,
		})
	}
	miniOK(w, map[string]any{"logs": logs, "page": page, "total": total})
}

// miniAILogItem 返回一行流水的完整内容（含 input/output 全文）。
func miniAILogItem(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	if !sh.IsMain(uid) {
		miniErr(w, http.StatusForbidden, "模型调用流水只有主管理员可以查看")
		return
	}
	id := miniInt(body, "id")
	if id < 1 {
		miniErr(w, http.StatusBadRequest, "参数缺失")
		return
	}
	var ts, latency, pt, ct, cached, cost int64
	var endpoint, model, upstream, st, input, output, errText string
	err := sh.Store.Read.QueryRow(`SELECT ts,endpoint,model,upstream,status,latency_ms,
		prompt_tokens,completion_tokens,cached_tokens,quota_cost,input,output,error
		FROM ai_log WHERE id=?`, id).
		Scan(&ts, &endpoint, &model, &upstream, &st, &latency,
			&pt, &ct, &cached, &cost, &input, &output, &errText)
	if err != nil {
		miniErr(w, http.StatusNotFound, "记录不存在")
		return
	}
	miniOK(w, map[string]any{
		"id": id, "ts": ts, "endpoint": endpoint, "model": model,
		"upstream": upstream, "status": st, "latency_ms": latency,
		"prompt_tokens": pt, "completion_tokens": ct,
		"cached_tokens": cached, "quota_cost": cost,
		"cost_text": billing.FormatUSDFine(cost),
		"input":     input, "output": output, "error": errText,
	})
}

// likeEscape 转义 LIKE 的通配符：搜索词里的 % _ \ 按字面匹配（配合
// ESCAPE '\'），否则搜「100%」会搜出全表。
func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
