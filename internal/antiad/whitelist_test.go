package antiad

import (
	"testing"

	"menshen/internal/testutil"
	"menshen/internal/tg"
)

func adwMsg(chatID, from int64, arg string, reply *tg.Message) *tg.Message {
	text := "/white"
	if arg != "" {
		text += " " + arg
	}
	m := testutil.GroupMsg(chatID, from, 901, text)
	m.ReplyToMessage = reply
	return m
}

// TestAdwWhitelistsAndExempts：群管理员回复某人发 /white，此人在本群免检；
// 别的群不受影响。
func TestAdwWhitelistsAndExempts(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	soN, _ := fakeAIWith(t, b, soReply("ad", 0.95, "scam", "message"), llmReply(true, 0.95, "scam", "message"))

	HandleGroupMessage(b, adwMsg(-100, 1, "", testutil.GroupMsg(-100, 42, 5, "x")))
	if !isGroupWhitelisted(b, -100, 42) {
		t.Fatal("/white 后应在本群白名单里")
	}
	if isGroupWhitelisted(b, -200, 42) {
		t.Error("白名单是按群的")
	}

	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 6, "日入过万"))
	waitIdle(t, b)
	if soN.Load() != 0 {
		t.Error("白名单里的人不该送检")
	}
}

// TestAdwByIDAndChannel：也能直接给 ID，频道是负 ID。
func TestAdwByIDAndChannel(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	HandleGroupMessage(b, adwMsg(-100, 1, "-1009", nil))
	if !isGroupWhitelisted(b, -100, -1009) {
		t.Error("/white 应接受频道 ID")
	}
}

// TestAdwRequiresAdminSilently：白名单等于对此人关掉反广告，普通成员不能用；
// 与 /ban 一致静默忽略，不向非授权者暴露该命令存在。
func TestAdwRequiresAdminSilently(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	HandleGroupMessage(b, adwMsg(-100, 42, "42", nil))
	if isGroupWhitelisted(b, -100, 42) {
		t.Error("普通成员不能把自己加进白名单")
	}
	if fake.CountCalls("sendMessage") != 0 {
		t.Error("非授权者应被静默忽略")
	}
}

// TestCleanupKeepsWhitelisted：白名单是管理员的明确决定，不随不发言过期。
func TestCleanupKeepsWhitelisted(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	if err := setGroupWhitelist(b, -100, 42, true); err != nil {
		t.Fatal(err)
	}
	b.Store.Write.Exec(`UPDATE group_members SET first_seen=0, last_msg_at=0, joined_at=0`)
	CleanupData(b.Shared)
	if !isGroupWhitelisted(b, -100, 42) {
		t.Error("清理不该删掉白名单行")
	}
}
