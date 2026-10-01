package antiad

import (
	"testing"
	"time"

	"menshen/internal/testutil"
)

// TestUserDossierScopeByBot：工作 bot 的资料统计只覆盖自己名下的群（多租户
// 下不能把别人的群数据算进来）；主 bot 是平台级视图，统计按全局算。
func TestUserDossierScopeByBot(t *testing.T) {
	reg, w1 := testutil.NewTestRegistry(t, nil)
	sh := w1.Shared
	w2, _ := testutil.AddRegistryBot(t, reg, sh, 5555, 777)
	testutil.EnableAntiad(t, w1, -100)
	testutil.EnableAntiad(t, w2, -200)
	// 主 bot（不入群、不判定，但 /user 走它时看全局）。
	const mainID = 9999
	testutil.RegisterMainTestBot(t, sh, "9999:AAEEfaketoken_ForUnitTestsOnly1234567", mainID, 777)
	if err := sh.Cache.Reload(); err != nil {
		t.Fatal(err)
	}

	now := time.Now().Unix()
	seed := func(botID, chatID, msgID int64) {
		if _, err := w1.Store.Write.Exec(`INSERT INTO antiad_log
			(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
			 action,reason,created_at,bot_id)
			VALUES (?,555,?,'加微信','ad',0.95,'llm','scam','deleted_muted','x',?,?)`,
			chatID, msgID, now, botID); err != nil {
			t.Fatal(err)
		}
		if _, err := w1.Store.Write.Exec(`INSERT INTO group_messages
			(chat_id,message_id,user_id,text,at) VALUES (?,?,555,'加微信',?)`,
			chatID, msgID, now); err != nil {
			t.Fatal(err)
		}
		if _, err := w1.Store.Write.Exec(`INSERT INTO group_members
			(chat_id,user_id,joined_at,first_seen,msg_count,last_msg_at,ad_hits)
			VALUES (?,555,?,?,5,?,1)`, chatID, now-60, now-60, now); err != nil {
			t.Fatal(err)
		}
	}
	seed(w1.BotID(), -100, 7) // 工作 bot 一号：自己的群
	seed(w2.BotID(), -200, 8) // 工作 bot 二号：另一个群

	// 工作 bot 视角：只有自己那一份。
	d := LoadUserDossier(sh, w1.BotID(), 555)
	if d.Total != 1 || d.Processed != 1 {
		t.Errorf("工作 bot 只该算自己的流水：共%d/处置%d", d.Total, d.Processed)
	}
	if d.ChatCount != 1 || d.Kept != 1 || d.Msgs != 5 {
		t.Errorf("工作 bot 只该算自己的群：群%d 留底%d 发言%d", d.ChatCount, d.Kept, d.Msgs)
	}
	if logs := LoadUserLogs(sh, w1.BotID(), 555, false, 10, 0); len(logs) != 1 {
		t.Errorf("工作 bot 只该列出自己那条，得到 %d 条", len(logs))
	}

	// 主 bot 视角：全局。
	d = LoadUserDossier(sh, mainID, 555)
	if d.Total != 2 || d.Processed != 2 {
		t.Errorf("主 bot 该算全局流水：共%d/处置%d", d.Total, d.Processed)
	}
	if d.ChatCount != 2 || d.Kept != 2 || d.Msgs != 10 {
		t.Errorf("主 bot 该算全局的群：群%d 留底%d 发言%d", d.ChatCount, d.Kept, d.Msgs)
	}
	if logs := LoadUserLogs(sh, mainID, 555, false, 10, 0); len(logs) != 2 {
		t.Errorf("主 bot 该列出两条，得到 %d 条", len(logs))
	}
	// 默认过滤对本视角同样生效：把两条都标成未处置就都看不到。
	if _, err := w1.Store.Write.Exec(`UPDATE antiad_log SET action='none'`); err != nil {
		t.Fatal(err)
	}
	if d := LoadUserDossier(sh, mainID, 555); d.Processed != 0 {
		t.Errorf("全标成未处置后处置数应为 0，得到 %d", d.Processed)
	}
}
