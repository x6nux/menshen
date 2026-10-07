package panel

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"testing"
	"time"

	"menshen/internal/logbuf"
	"menshen/internal/testutil"
)

// miniSysLogCall 发一次 syslog 请求并解出响应。
func miniSysLogCall(t *testing.T, env *miniTestEnv, body any) map[string]any {
	t.Helper()
	w := miniDo(t, env.h, testutil.TestToken, env.adminInit(), testutil.TestBotID, "syslog", body)
	if w.Code != http.StatusOK {
		t.Fatalf("syslog 应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// miniSysLogMsgs 抽出响应里各行的消息，按返回顺序。
func miniSysLogMsgs(out map[string]any) []string {
	rows := out["logs"].([]any)
	msgs := make([]string, 0, len(rows))
	for _, v := range rows {
		msgs = append(msgs, v.(map[string]any)["message"].(string))
	}
	return msgs
}

// TestMiniSysLogMainOnly：运行日志是进程级数据，只有主管理员能看；次级
// 管理员即便拼出请求也被服务端拒（入口不出现只是第一道）。
func TestMiniSysLogMainOnly(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	sh.Logs.Add(logbuf.Record{Level: slog.LevelInfo, Message: "有内容", Time: time.Now()})
	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}

	if w := miniDo(t, env.h, testutil.TestToken, env.adminInit(),
		testutil.TestBotID, "syslog", nil); w.Code != http.StatusOK {
		t.Fatalf("主管理员应 200，得到 %d：%s", w.Code, w.Body.String())
	}

	testutil.RegisterTestBot(t, sh, testToken2, 43, 888)
	if err := sh.AddAdmin(888, "次管", 777); err != nil {
		t.Fatal(err)
	}
	sub := signInitData(t, testToken2, map[string]string{
		"auth_date": strconv.FormatInt(env.now, 10), "user": `{"id":888}`})
	if w := miniDo(t, env.h, testToken2, sub, 43, "syslog", nil); w.Code != http.StatusForbidden {
		t.Fatalf("次级管理员应 403，得到 %d：%s", w.Code, w.Body.String())
	}
}

// TestMiniSysLogFilterAndSearch：级别为不低于下限、搜索覆盖消息与字段，
// counts 只受搜索影响（界面据此显示各级命中数）。
func TestMiniSysLogFilterAndSearch(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	now := time.Now()
	add := func(l slog.Level, msg string, kv ...string) {
		rec := logbuf.Record{Level: l, Message: msg, Time: now}
		for i := 0; i+1 < len(kv); i += 2 {
			rec.Attrs = append(rec.Attrs, logbuf.Attr{Key: kv[i], Value: kv[i+1]})
		}
		sh.Logs.Add(rec)
	}
	add(slog.LevelDebug, "调试行")
	add(slog.LevelInfo, "webhook 已注册", "bot", "demo")
	add(slog.LevelWarn, "上游抖动")
	add(slog.LevelError, "判定失败", "err", "deadline exceeded")

	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}

	// 全部：新→旧四条。
	out := miniSysLogCall(t, env, map[string]any{})
	if got := miniSysLogMsgs(out); len(got) != 4 || got[0] != "判定失败" || got[3] != "调试行" {
		t.Fatalf("默认应返回全部且新→旧，得到 %v", got)
	}

	// WARN 下限：WARN + ERROR。
	out = miniSysLogCall(t, env, map[string]any{"level": "warn"})
	if got := miniSysLogMsgs(out); len(got) != 2 || got[0] != "判定失败" || got[1] != "上游抖动" {
		t.Fatalf("WARN 下限应命中错误与警告，得到 %v", got)
	}
	if out["total"].(float64) != 2 {
		t.Errorf("total 应为 2，得到 %v", out["total"])
	}
	counts := out["counts"].(map[string]any)
	if counts["debug"].(float64) != 1 || counts["info"].(float64) != 1 ||
		counts["warn"].(float64) != 1 || counts["error"].(float64) != 1 {
		t.Errorf("counts 应覆盖全部级别、不受下限影响，得到 %v", counts)
	}

	// 字段值搜索：命中错误行，并带出字段。
	out = miniSysLogCall(t, env, map[string]any{"q": "DEADLINE"})
	if got := miniSysLogMsgs(out); len(got) != 1 || got[0] != "判定失败" {
		t.Fatalf("字段值搜索应大小写不敏感命中错误行，得到 %v", got)
	}
	row := out["logs"].([]any)[0].(map[string]any)
	attrs := row["attrs"].([]any)[0].(map[string]any)
	if row["level"] != "ERROR" || attrs["k"] != "err" || attrs["v"] != "deadline exceeded" {
		t.Errorf("行应带级别与字段，得到 %v", row)
	}
}

// TestMiniSysLogPagination：超过一页时按新→旧切片，total 是过滤后总数。
func TestMiniSysLogPagination(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	now := time.Now()
	for i := 1; i <= 55; i++ {
		sh.Logs.Add(logbuf.Record{Level: slog.LevelInfo, Message: "行" + strconv.Itoa(i), Time: now})
	}
	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: now.Unix()}

	page1 := miniSysLogCall(t, env, map[string]any{"page": 1})
	if got := page1["logs"].([]any); len(got) != sysLogPageSize {
		t.Fatalf("第一页应有 %d 条，得到 %d", sysLogPageSize, len(got))
	}
	if page1["total"].(float64) != 55 {
		t.Errorf("total 应为 55，得到 %v", page1["total"])
	}
	page2 := miniSysLogCall(t, env, map[string]any{"page": 2})
	if got := page2["logs"].([]any); len(got) != 5 {
		t.Fatalf("第二页应有 5 条，得到 %d", len(got))
	}
	// 新→旧：第一页首条是最后写入的`行55`。
	if first := page1["logs"].([]any)[0].(map[string]any)["message"]; first != "行55" {
		t.Errorf("第一页首条应是最新写入的行55，得到 %v", first)
	}
}
