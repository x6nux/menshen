package antiad

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"menshen/internal/testutil"
)

func itoaTest(n int64) string { return strconv.FormatInt(n, 10) }

// TestViewPageGateAndContent：GET 只给警示与查看凭据（不含原文），POST
// 验签且未过期才返回内容；过期凭据 404。
func TestViewPageGateAndContent(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	b.Cfg.PublicURL = "https://ad.example.com"
	if err := EnsureWebSecret(b.Shared); err != nil {
		t.Fatal(err)
	}
	res, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,prompt_tokens,completion_tokens,quota_cost,created_at,bot_id,user_name)
		VALUES (-100,555,900,?,'ad',0.9,'llm','scam','deleted_muted','理由',0,0,0,?,42,'昵称')`,
		`买号 <script>alert(1)</script>`, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	path := "/_w/v/" + itoaTest(id) + "/" + logViewSig(b.Shared, id)
	handler := WebHandler(b.Shared)

	// GET ?json=1：门槛数据，不含原文。
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path+"?json=1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET 应 200，得到 %d", w.Code)
	}
	var gateOut struct {
		Gate struct {
			Title string `json:"title"`
			Warn  string `json:"warn"`
			Exp   int64  `json:"exp"`
			K     string `json:"k"`
		} `json:"gate"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &gateOut); err != nil {
		t.Fatalf("门槛响应应是 JSON：%v（%s）", err, w.Body.String())
	}
	if gateOut.Gate.K == "" || gateOut.Gate.Exp <= time.Now().Unix() {
		t.Errorf("门槛数据应带未过期的查看凭据：%+v", gateOut.Gate)
	}
	if strings.Contains(w.Body.String(), "买号") {
		t.Error("GET 不该返回原文")
	}

	// POST 凭据：返回内容（JSON 原样保留文本，前端负责转义）。
	body, _ := json.Marshal(map[string]any{"e": gateOut.Gate.Exp, "k": gateOut.Gate.K})
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST 应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	var viewOut struct {
		View struct {
			Record struct {
				ID   int64  `json:"id"`
				Text string `json:"text"`
			} `json:"record"`
		} `json:"view"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &viewOut); err != nil {
		t.Fatalf("内容响应应是 JSON：%v（%s）", err, w.Body.String())
	}
	if viewOut.View.Record.ID != id ||
		!strings.Contains(viewOut.View.Record.Text, "买号") {
		t.Errorf("验签通过后应能看到原文：%+v", viewOut.View.Record)
	}

	// 过期凭据：404。
	old := time.Now().Add(-time.Minute).Unix()
	body, _ = json.Marshal(map[string]any{"e": old, "k": logViewPostSig(b.Shared, id, old)})
	req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("过期凭据应 404，得到 %d", w.Code)
	}
}

// TestAppealDetailShowsUserDossier：申诉详情页左栏要给出账号信息与各种资料
// （处罚、留底发言、判定流水、网页验证明细），不能只有右栏一张单子。
func TestAppealDetailShowsUserDossier(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	b.Cfg.PublicURL = "https://ad.example.com"
	if err := EnsureWebSecret(b.Shared); err != nil {
		t.Fatal(err)
	}
	testutil.EnableAntiad(t, b, -100) // 建群，标题「测试群」
	botID := b.BotID()
	now := time.Now().Unix()

	res, err := b.Store.Write.Exec(`INSERT INTO appeals
		(bot_id,user_id,status,statement,ai_result,ai_conf,ai_reason,ai_model,
		 web_attempts,code,code_expires,created_at,updated_at)
		VALUES (?,?,'code','我是清白的','uphold',0.95,'资料里有推广话术',
		 'test/model',1,'MSU-AAAA',?,?,?)`,
		botID, 555, now+86400, now, now)
	if err != nil {
		t.Fatal(err)
	}
	appealID, _ := res.LastInsertId()

	if _, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,prompt_tokens,completion_tokens,quota_cost,created_at,bot_id,user_name)
		VALUES (-100,555,900,'加微信 日入5000','ad',0.99,'llm','scam',
		 'deleted_muted','账号资料与消息都像广告',0,0,0,?,?,'广告昵称')`,
		now, botID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,prompt_tokens,completion_tokens,quota_cost,created_at,
		 bot_id,user_name,lifted_at)
		VALUES (-100,555,901,'上次那条','ad',0.9,'llm','scam',
		 'deleted_muted','上一条已改判',0,0,0,?,?,'广告昵称',?)`,
		now-3*86400, botID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Store.Write.Exec(`INSERT INTO join_mutes
		(bot_id,chat_id,user_id,reason,created_at) VALUES (?,?,?,?,?)`,
		botID, -100, 555, "资料里写着引流", now); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Store.Write.Exec(`INSERT INTO group_messages
		(chat_id,message_id,user_id,text,at) VALUES (-100,900,555,'加微信 日入5000',?)`,
		now); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Store.Write.Exec(`INSERT INTO group_members
		(chat_id,user_id,joined_at,first_seen,msg_count,last_msg_at,ad_hits)
		VALUES (-100,555,?,?,3,?,1)`, now-3600, now-3600, now); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Store.Write.Exec(`INSERT INTO web_checks
		(appeal_id,bot_id,user_id,ip,ua,fp,signals,flags,result,created_at)
		VALUES (?,?,?,'1.2.3.4','UA/1.0','fphash','{}','屏幕尺寸为 0','pass',?)`,
		appealID, botID, 555, now); err != nil {
		t.Fatal(err)
	}

	path := "/_w/apv/" + itoaTest(appealID) + "/" + appealViewSig(b.Shared, appealID)
	handler := WebHandler(b.Shared)

	// GET ?json=1：门槛数据；POST 凭据才返回内容。
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path+"?json=1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET 应 200，得到 %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "我是清白的") {
		t.Error("门槛数据不该包含申诉理由")
	}
	var gateOut struct {
		Gate struct {
			Exp int64  `json:"exp"`
			K   string `json:"k"`
		} `json:"gate"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &gateOut); err != nil || gateOut.Gate.K == "" {
		t.Fatalf("门槛响应缺查看凭据：%v（%s）", err, w.Body.String())
	}

	body, _ := json.Marshal(map[string]any{"e": gateOut.Gate.Exp, "k": gateOut.Gate.K})
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST 应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	var out struct {
		View appealViewData `json:"view"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("内容响应应是 JSON：%v（%s）", err, w.Body.String())
	}
	v := out.View
	if v.UID != 555 || v.UName != "广告昵称" || v.Msgs != 3 || v.Hits != 1 {
		t.Errorf("账号信息不对：uid=%d name=%q msgs=%d hits=%d", v.UID, v.UName, v.Msgs, v.Hits)
	}
	if !strings.Contains(v.Status, "解禁码") {
		t.Errorf("状态应翻译成中文，得到 %q", v.Status)
	}
	if v.Statement != "我是清白的" || v.AIReason != "资料里有推广话术" {
		t.Errorf("申诉单字段不对：%+v", v)
	}
	if len(v.Limits) == 0 || !strings.Contains(v.Limits[0].Reason, "资料里写着引流") {
		t.Errorf("当前生效限制应带理由：%+v", v.Limits)
	}
	foundPenalty := false
	for _, p := range v.Penalties {
		if strings.Contains(p.Reason, "上一条已改判") || strings.Contains(p.Text, "上次那条") {
			foundPenalty = true
		}
	}
	if !foundPenalty {
		t.Errorf("历史处罚应包含已撤销的记录：%+v", v.Penalties)
	}
	if len(v.History) == 0 || !strings.Contains(v.History[0].Text, "加微信 日入5000") ||
		v.History[0].Chat != "测试群" || !v.History[0].Blocked {
		t.Errorf("群内留底应带群名与「被拦」标记：%+v", v.History)
	}
	if len(v.Logs) == 0 {
		t.Errorf("应带判定流水：%+v", v.Logs)
	}
	if len(v.Checks) != 1 || v.Checks[0].IP != "1.2.3.4" || v.Checks[0].FP != "fphash" ||
		!strings.Contains(v.Checks[0].Flags, "屏幕尺寸为 0") {
		t.Errorf("网页验证记录不对：%+v", v.Checks)
	}
}

// TestJoinNoticePairedEitherOrder：入群服务消息与冷判定禁言谁先到，
// 都能把「XXX 已加入群组」删掉。
func TestJoinNoticePairedEitherOrder(t *testing.T) {
	// 服务消息先到，判定后命中。
	b, fake := testutil.NewTestBot(t, 1)
	noteJoinNotice(b, -100, 555, 50)
	deleteJoinNotice(b, -100, 555)
	if last := fake.LastCall("deleteMessage"); last == nil ||
		last["message_id"] != float64(50) {
		t.Errorf("判定命中时应删掉已知的服务消息，得到 %v", last)
	}

	// 判定先命中，服务消息后到。
	b2, fake2 := testutil.NewTestBot(t, 1)
	deleteJoinNotice(b2, -100, 556)
	noteJoinNotice(b2, -100, 556, 60)
	if last := fake2.LastCall("deleteMessage"); last == nil ||
		last["message_id"] != float64(60) {
		t.Errorf("服务消息后到时应当场删除，得到 %v", last)
	}

	// 没命中就不删：GC 前配对表里留着也不会有动作。
	b3, fake3 := testutil.NewTestBot(t, 1)
	noteJoinNotice(b3, -100, 557, 70)
	if n := fake3.CountCalls("deleteMessage"); n != 0 {
		t.Errorf("没有禁言就不该删服务消息，删了 %d 次", n)
	}
	GCJoinNotices(b3.Shared) // 不 panic 即可
}

// TestAppealViewJSONArraysNeverNull：申诉详情的数组字段必须序列化成 []，
// 不能是 null —— 前端对 null 调 length/map 会抛错并卸载整页
// （线上真实事故：点「查看内容」后白屏）。
func TestAppealViewJSONArraysNeverNull(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	b.Cfg.PublicURL = "https://ad.example.com"
	if err := EnsureWebSecret(b.Shared); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	res, err := b.Store.Write.Exec(`INSERT INTO appeals
		(bot_id,user_id,status,created_at,updated_at) VALUES (?,?,'web',?,?)`,
		b.BotID(), 555, now, now)
	if err != nil {
		t.Fatal(err)
	}
	appealID, _ := res.LastInsertId()
	path := "/_w/apv/" + itoaTest(appealID) + "/" + appealViewSig(b.Shared, appealID)
	handler := WebHandler(b.Shared)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path+"?json=1", nil))
	var gate struct {
		Gate struct {
			Exp int64  `json:"exp"`
			K   string `json:"k"`
		} `json:"gate"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &gate); err != nil || gate.Gate.K == "" {
		t.Fatalf("门槛响应不对：%v（%s）", err, w.Body.String())
	}
	body, _ := json.Marshal(map[string]any{"e": gate.Gate.Exp, "k": gate.Gate.K})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body))))
	var out struct {
		View map[string]json.RawMessage `json:"view"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("内容响应不是 JSON：%v（%s）", err, w.Body.String())
	}
	for _, k := range []string{"limits", "penalties", "history", "history_more",
		"logs", "checks", "strong", "weak"} {
		raw, ok := out.View[k]
		if !ok || string(raw) == "null" {
			t.Errorf("字段 %s 应是数组，得到 %s", k, string(raw))
		}
	}
}
