package panel

import (
	"strings"
	"testing"

	"menshen/internal/antiad"
	"menshen/internal/core"
	"menshen/internal/testutil"
)

// seedWorkerLog 在指定 bot 名下落一条流水。
func seedWorkerLog(t *testing.T, sh *core.Shared, botID, uid int64, action string) int64 {
	t.Helper()
	res, err := sh.Store.Write.Exec(`INSERT INTO antiad_log (chat_id,user_id,message_id,
		text,verdict,confidence,decider,ad_kind,action,reason,created_at,bot_id)
		VALUES (-100,?,7,'广告','ad',0.95,'systemone','scam',?,'',0,?)`, uid, action, botID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func unmuteCalls(f *testutil.FakeTG) int {
	n := 0
	for _, p := range f.Calls("restrictChatMember") {
		if perms, ok := p["permissions"].(map[string]any); ok && perms["can_send_messages"] == true {
			n++
		}
	}
	return n
}

func activeMutes(t *testing.T, sh *core.Shared, uid int64) int {
	t.Helper()
	var n int
	if err := sh.Store.Read.QueryRow(`SELECT COUNT(*) FROM antiad_log
		WHERE user_id=? AND chat_id=-100 AND lifted_at=0
		AND action IN ('muted','deleted_muted','gban_muted')`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestFalsePositiveRunsOnRecordBot：管理员在另一个 bot（典型是不入群的
// 主 bot）的面板上点误判，解禁必须由记录所属的工作 bot 去做——面板所在
// 的 bot 不在群里，restrictChatMember 必然失败。源群里由这条判定派生的
// 联合封禁禁言也要一起解开：先改判再撤名单，否则撤名单时这条判定还算
// 群里另有生效处罚。
func TestFalsePositiveRunsOnRecordBot(t *testing.T) {
	reg, panelBot := testutil.NewTestRegistry(t, dispatch)
	sh := panelBot.Shared
	worker, wfake := testutil.AddRegistryBot(t, reg, sh, 4343, 777)
	testutil.EnableAntiad(t, worker, -100)
	pfake := panelBot.TG.(*testutil.FakeTG)

	id := seedWorkerLog(t, sh, worker.BotID(), 555, "deleted_muted")
	if err := antiad.GbanAdd(sh, 555, "自动判定：测试", -100, worker.BotID()); err != nil {
		t.Fatal(err)
	}
	seedWorkerLog(t, sh, worker.BotID(), 555, "gban_muted")
	pfake.Reset()
	wfake.Reset()

	HandleAdminCallback(panelBot, cb(777, "a:ad:fp:"+itoa(id)))

	if pfake.CountCalls("restrictChatMember") != 0 {
		t.Error("面板所在的 bot 不在群里，不该由它去解禁")
	}
	if unmuteCalls(wfake) == 0 {
		t.Error("应由记录所属的工作 bot 发「权限全开」解除禁言")
	}
	if n := activeMutes(t, sh, 555); n != 0 {
		t.Errorf("误判后本群的禁言流水应全部标记解除，还剩 %d 条", n)
	}
	ans := pfake.LastCall("answerCallbackQuery")
	if ans == nil || strings.Contains(ans["text"].(string), "失败") {
		t.Errorf("回调应由面板 bot 应答且不报失败，得到 %v", ans)
	}
}

// TestReleaseRunsOnRecordBot：解封（判定维持）同理，由记录所属 bot 执行。
func TestReleaseRunsOnRecordBot(t *testing.T) {
	reg, panelBot := testutil.NewTestRegistry(t, dispatch)
	sh := panelBot.Shared
	worker, wfake := testutil.AddRegistryBot(t, reg, sh, 4343, 777)
	testutil.EnableAntiad(t, worker, -100)
	pfake := panelBot.TG.(*testutil.FakeTG)

	id := seedWorkerLog(t, sh, worker.BotID(), 556, "deleted_muted")
	pfake.Reset()
	wfake.Reset()

	HandleAdminCallback(panelBot, cb(777, "a:ad:rel:"+itoa(id)))

	if pfake.CountCalls("restrictChatMember") != 0 || unmuteCalls(wfake) == 0 {
		t.Errorf("解封应由工作 bot 执行：面板 bot %d 次、工作 bot 解禁 %d 次",
			pfake.CountCalls("restrictChatMember"), unmuteCalls(wfake))
	}
	if n := activeMutes(t, sh, 556); n != 0 {
		t.Errorf("解封后禁言流水应标记解除，还剩 %d 条", n)
	}
}
