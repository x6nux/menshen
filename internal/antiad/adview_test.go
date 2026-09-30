package antiad

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"menshen/internal/testutil"
)

func itoaTest(n int64) string { return strconv.FormatInt(n, 10) }

// TestViewPageGateAndContent：GET 只给警示与按钮，POST 验签且未过期才渲染；
// 原文里的脚本被模板转义。
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

	// GET：门槛页，不含原文。
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET 应 200，得到 %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "查看内容") || !strings.Contains(body, `name="k"`) {
		t.Errorf("门槛页应给出查看按钮与签名表单:\n%s", body)
	}
	if strings.Contains(body, "买号") {
		t.Error("GET 不该渲染原文")
	}

	// POST：签名有效才渲染，脚本被转义。
	exp := time.Now().Add(time.Minute).Unix()
	form := url.Values{"e": {itoaTest(exp)},
		"k": {logViewPostSig(b.Shared, id, exp)}}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST 应 200，得到 %d", w.Code)
	}
	body = w.Body.String()
	if !strings.Contains(body, "&lt;script&gt;") || strings.Contains(body, "<script>alert") {
		t.Errorf("原文应被模板转义:\n%s", body)
	}
	if !strings.Contains(body, "买号") {
		t.Error("验签通过后应能看到原文")
	}

	// 过期表单：404。
	old := time.Now().Add(-time.Minute).Unix()
	form = url.Values{"e": {itoaTest(old)}, "k": {logViewPostSig(b.Shared, id, old)}}
	req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("过期表单应 404，得到 %d", w.Code)
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
	exp := time.Now().Add(time.Minute).Unix()
	form := url.Values{"e": {itoaTest(exp)},
		"k": {logViewPostSig(b.Shared, appealID, exp)}}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	WebHandler(b.Shared).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST 应 200，得到 %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`class="side"`, `class="main"`,
		"账号信息", "判定时昵称", "广告昵称", "群内发言：3 条", "历史命中：1 次",
		"当前生效限制", "进群资料审核", "资料里写着引流",
		"历史处罚", "上一条已改判",
		"群内留底发言", "加微信 日入5000", "被拦", "测试群",
		"判定流水",
		"网页验证记录", "1.2.3.4", "fphash", "屏幕尺寸为 0",
		"我是清白的", "已发解禁码",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("申诉详情页应包含 %q\n%s", want, body)
		}
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
