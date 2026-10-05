package panel

// 运行日志页（网页版 / Mini App）的数据面：读进程内的 slog 环形缓冲
//（见 internal/logbuf，由 main 接进 slog）。
//
// 只对主管理员开放：日志是进程级的，一条错误里常带着别的租户的 chat_id
// 与判定上下文，次级管理员没有理由看到不属于自己 bot 的记录。

import (
	"net/http"

	"menshen/internal/core"
	"menshen/internal/logbuf"
)

// sysLogPageSize 是运行日志每页条数。比记录页（20）大：一行日志远短于一条
// 判定记录，一屏能多放一些，滚动到底的代价也低。
const sysLogPageSize = 50

// miniSysLog 返回运行日志的一页。level 是级别下限（空/all/未知 = 不过滤），
// q 是大小写不敏感的子串，匹配消息与字段的键和值。
func miniSysLog(sh *core.Shared, w http.ResponseWriter, uid int64, body map[string]any) {
	if !sh.IsMain(uid) {
		miniErr(w, http.StatusForbidden, "运行日志只有主管理员可以查看")
		return
	}
	if sh.Logs == nil {
		// NewShared 一定会建缓冲，走到这里说明装配被改坏了，明说而不是回空列表。
		miniErr(w, http.StatusInternalServerError, "运行日志缓冲不可用")
		return
	}
	page := clampPage(miniInt(body, "page"))
	records, total, counts := sh.Logs.Query(logbuf.Query{
		MinLevel: logbuf.ParseLevel(miniStr(body, "level")),
		Search:   miniStr(body, "q"),
		Offset:   int((page - 1) * sysLogPageSize),
		Limit:    sysLogPageSize,
	})

	logs := make([]map[string]any, 0, len(records))
	for _, r := range records {
		attrs := make([]map[string]any, 0, len(r.Attrs))
		for _, a := range r.Attrs {
			attrs = append(attrs, map[string]any{"k": a.Key, "v": a.Value})
		}
		logs = append(logs, map[string]any{
			"seq":     r.Seq,
			"at":      r.Time.Unix(),
			"level":   logbuf.LevelName(r.Level),
			"message": r.Message,
			"attrs":   attrs,
		})
	}
	miniOK(w, map[string]any{
		"logs": logs, "page": page, "total": total,
		"counts": map[string]any{
			"debug": counts.Debug, "info": counts.Info,
			"warn": counts.Warn, "error": counts.Error,
		},
	})
}
