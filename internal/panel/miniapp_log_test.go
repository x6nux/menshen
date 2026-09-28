package panel

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"menshen/internal/testutil"
)

// TestMiniLogsFiltersAndSearch：记录列表的筛选（前端默认带已删除）与
// 搜索（原文 / 理由 / 纯数字 ID）是记录页可操作化的前提，先守后端。
func TestMiniLogsFiltersAndSearch(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}
	init := env.adminInit()
	call := func(op string, body any) map[string]any {
		w := miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, op, body)
		if w.Code != http.StatusOK {
			t.Fatalf("%s 应 200，得到 %d：%s", op, w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	ids := func(out map[string]any) map[int64]bool {
		m := map[int64]bool{}
		for _, v := range out["logs"].([]any) {
			f := v.(map[string]any)
			m[int64(f["id"].(float64))] = true
		}
		return m
	}

	seed := func(id int64, userID int64, verdict, action, text, reason string) {
		if _, err := sh.Store.Write.Exec(`INSERT INTO antiad_log
			(bot_id,chat_id,user_id,message_id,text,verdict,confidence,decider,
			 ad_kind,action,reason,created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			testutil.TestBotID, -100, userID, id, text, verdict, 0.9, "so",
			"", action, reason, env.now); err != nil {
			t.Fatal(err)
		}
	}
	// 四条：两条被删除、一条放行、一条被护栏拦下。
	seed(1, 555, "ad", "deleted_muted", "加微信买号", "推广")
	seed(2, 556, "none", "none", "今天天气不错", "")
	seed(3, 557, "skipped", "skipped", "随便聊聊", "超出本群送检频率上限")
	seed(4, 558, "ad", "deleted", "促销优惠", "推销")

	if got := ids(call("logs", map[string]any{"verdict": "deleted"})); len(got) != 2 ||
		!got[1] || !got[4] {
		t.Errorf("默认已删除筛选应命中 #1 #4，得到 %v", got)
	}
	if got := ids(call("logs", map[string]any{"verdict": "clean"})); !got[2] || len(got) != 1 {
		t.Errorf("正常筛选应命中 #2，得到 %v", got)
	}
	if got := ids(call("logs", map[string]any{"verdict": "skipped"})); !got[3] || len(got) != 1 {
		t.Errorf("跳过筛选应命中 #3，得到 %v", got)
	}
	if got := ids(call("logs", map[string]any{"q": "微信"})); !got[1] || len(got) != 1 {
		t.Errorf("原文搜索应命中 #1，得到 %v", got)
	}
	if got := ids(call("logs", map[string]any{"q": "频率"})); !got[3] || len(got) != 1 {
		t.Errorf("理由搜索应命中 #3，得到 %v", got)
	}
	if got := ids(call("logs", map[string]any{"q": "556"})); !got[2] || len(got) != 1 {
		t.Errorf("纯数字应按 uid 搜到 #2，得到 %v", got)
	}
	if got := ids(call("logs", map[string]any{"user_id": 555})); !got[1] || len(got) != 1 {
		t.Errorf("按 uid 筛选应命中 #1，得到 %v", got)
	}
}

// TestMiniLogactOps：记录详情页的操作——解除禁言要发 TG、白名单要落库、
// 联合封禁按主管理员权限走。
func TestMiniLogactOps(t *testing.T) {
	reg, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	testutil.EnableAntiad(t, b, -100)
	fake := b.TG.(*testutil.FakeTG)
	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}
	init := env.adminInit()

	if _, err := sh.Store.Write.Exec(`INSERT INTO antiad_log
		(bot_id,chat_id,user_id,message_id,text,verdict,confidence,decider,
		 ad_kind,action,reason,created_at)
		VALUES (?,?,?,?,'加微信买号','ad',0.9,'so','promo','deleted_muted','推广',?)`,
		testutil.TestBotID, -100, 999, 7, env.now); err != nil {
		t.Fatal(err)
	}
	act := func(op string, body any) *int {
		w := miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, op, body)
		code := w.Code
		if code != http.StatusOK {
			t.Fatalf("%s 应 200，得到 %d：%s", op, w.Code, w.Body.String())
		}
		return &code
	}

	// 解除禁言：TG 收到全 true 的权限集。
	act("logact", map[string]any{"id": 1, "action": "unmute"})
	last := fake.LastCall("restrictChatMember")
	if last == nil || last["user_id"].(float64) != 999 {
		t.Fatalf("解除禁言应 restrict user 999，得到 %v", last)
	}
	perms := last["permissions"].(map[string]any)
	if perms["can_send_messages"] != true {
		t.Error("解除禁言必须逐项给回权限")
	}

	// 加白名单：默认 24 小时，立即生效。
	act("logact", map[string]any{"id": 1, "action": "white"})
	if !sh.Cache.Snap().Whitelisted(testutil.TestBotID, -100, 999, env.now) {
		t.Error("白名单没生效")
	}

	// 联合封禁与解除（主管理员）。
	act("logact", map[string]any{"id": 1, "action": "gban"})
	if _, ok := sh.Cache.Snap().Gban[999]; !ok {
		t.Error("联合封禁没落库")
	}
	act("logact", map[string]any{"id": 1, "action": "ungban"})
	if _, ok := sh.Cache.Snap().Gban[999]; ok {
		t.Error("联合封禁没解除")
	}

	// 复查：没有留底时直接报错，不惊动群里。
	w := miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "logact",
		map[string]any{"id": 1, "action": "review"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("无留底复查应 400，得到 %d", w.Code)
	}
	if _, err := sh.Store.Write.Exec(`INSERT INTO group_messages
		(chat_id,message_id,user_id,text,at) VALUES (-100,7,999,'hi',?)`, env.now); err != nil {
		t.Fatal(err)
	}
	act("logact", map[string]any{"id": 1, "action": "review"})

	_ = reg
}

// TestMiniAppealAct：申诉单的人工处理——签发解禁码、驳回、人工解除，
// 结案后的单子不能再动。
func TestMiniAppealAct(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}
	init := env.adminInit()
	seedAppeal := func(id int64, status string) {
		if _, err := sh.Store.Write.Exec(`INSERT INTO appeals
			(bot_id,user_id,status,created_at,updated_at) VALUES (?,?,?,?,?)`,
			testutil.TestBotID, 900+id, status, env.now, env.now); err != nil {
			t.Fatal(err)
		}
	}
	statusOf := func(id int64) string {
		var s string
		if err := sh.Store.Read.QueryRow(
			`SELECT status FROM appeals WHERE id=?`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	call := func(op string, body any) (int, map[string]any) {
		w := miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, op, body)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	seedAppeal(1, "noweb")
	if code, out := call("appealact", map[string]any{"id": 1, "action": "issue_code"}); code != http.StatusOK {
		t.Fatalf("签发解禁码应 200，得到 %d：%v", code, out)
	}
	if statusOf(1) != "code" {
		t.Errorf("签发后状态应为 code，得到 %s", statusOf(1))
	}
	var codeStr string
	if err := sh.Store.Read.QueryRow(
		`SELECT code FROM appeals WHERE id=1`).Scan(&codeStr); err != nil || codeStr == "" {
		t.Errorf("解禁码没落库（err=%v）", err)
	}
	if code, out := call("appeal", map[string]any{"id": 1}); code != http.StatusOK ||
		out["has_code"] != true {
		t.Errorf("申诉详情应带 has_code，得到 %d %v", code, out)
	}

	seedAppeal(2, "web")
	if code, _ := call("appealact", map[string]any{"id": 2, "action": "reject"}); code != http.StatusOK {
		t.Fatalf("驳回应 200")
	}
	if statusOf(2) != "rejected" {
		t.Errorf("驳回后状态应为 rejected，得到 %s", statusOf(2))
	}
	if code, _ := call("appealact", map[string]any{"id": 2, "action": "approve"}); code != http.StatusBadRequest {
		t.Errorf("已结案的单子再操作应 400，得到 %d", code)
	}

	// 人工解除：没有有效限制的单子直接结案为 lifted。
	seedAppeal(3, "ai")
	if code, _ := call("appealact", map[string]any{"id": 3, "action": "approve"}); code != http.StatusOK {
		t.Fatalf("人工解除应 200")
	}
	if statusOf(3) != "lifted" {
		t.Errorf("人工解除后状态应为 lifted，得到 %s", statusOf(3))
	}
}

// TestMiniSetTimezone：时区是字符串型设置——只认 IANA 名称，只有主
// 管理员能改，非法值打回。
func TestMiniSetTimezone(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	testutil.RegisterTestBot(t, sh, testToken2, 43, 888)
	if err := sh.AddAdmin(888, "次管", 777); err != nil {
		t.Fatal(err)
	}
	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}
	init := env.adminInit()
	sub := signInitData(t, testToken2, map[string]string{
		"auth_date": strconv.FormatInt(env.now, 10), "user": `{"id":888}`})

	w := miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "set",
		map[string]any{"scope": "global", "key": "tz_name", "value": "Europe/London"})
	if w.Code != http.StatusOK {
		t.Fatalf("主管理员设时区应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	if got := sh.Cache.Snap().Setting("tz_name"); got != "Europe/London" {
		t.Errorf("时区没落库，得到 %s", got)
	}

	w = miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "set",
		map[string]any{"scope": "global", "key": "tz_name", "value": "Mars/Base"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("非法时区名应 400，得到 %d", w.Code)
	}

	w = miniDo(t, env.h, testutil.TestToken, sub, 43, "set",
		map[string]any{"scope": "global", "key": "tz_name", "value": "Asia/Shanghai"})
	if w.Code != http.StatusForbidden {
		t.Errorf("次级管理员设时区应 403，得到 %d", w.Code)
	}
}
