package core_test

import (
	"errors"
	"testing"

	"menshen/internal/core"
	"menshen/internal/testutil"
)

func ptr[T any](v T) *T { return &v }

// TestChatOps：新群默认演练；空标题不覆盖已有标题；补丁只改出现的字段；
// punish 越界是给操作者看的错误。
func TestChatOps(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	sh, bot := b.Shared, testutil.TestBotID

	if err := sh.AddChat(bot, -100, "群A"); err != nil {
		t.Fatal(err)
	}
	c, ok := sh.Cache.Snap().ChatConf(bot, -100)
	if !ok || !c.Enabled || !c.Dryrun || c.GroupAlert || c.Title != "群A" {
		t.Fatalf("新群应为启用+演练、标题群A，得到 %+v", c)
	}
	if err := sh.AddChat(bot, -100, ""); err != nil {
		t.Fatal(err)
	}
	if c, _ := sh.Cache.Snap().ChatConf(bot, -100); c.Title != "群A" {
		t.Fatalf("空标题不该覆盖已有标题，得到 %q", c.Title)
	}

	n, err := sh.UpdateChats(bot, []int64{-100, -999},
		core.ChatPatch{Dryrun: ptr(false), Punish: ptr(int64(1))})
	if err != nil || n != 1 {
		t.Fatalf("UpdateChats = %d, %v；应只命中名下的 1 个群", n, err)
	}
	c, _ = sh.Cache.Snap().ChatConf(bot, -100)
	if c.Dryrun || c.Punish != 1 || !c.Enabled {
		t.Fatalf("补丁应只改 dryrun 与 punish，得到 %+v", c)
	}

	_, err = sh.UpdateChats(bot, []int64{-100}, core.ChatPatch{Punish: ptr(int64(2))})
	var op *core.OpError
	if !errors.As(err, &op) {
		t.Fatalf("punish 越界应返回 OpError，得到 %v", err)
	}

	if err := sh.RemoveChat(bot, -100); err != nil {
		t.Fatal(err)
	}
	if _, ok := sh.Cache.Snap().ChatConf(bot, -100); ok {
		t.Fatal("移除后群配置仍在")
	}
}

// TestAddChatRejectsMainBot：主 bot 不入群，任何入口都不能给它加生效群。
func TestAddChatRejectsMainBot(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.RegisterMainTestBot(t, b.Shared, testutil.TestToken, testutil.TestBotID, 1)
	var op *core.OpError
	if err := b.Shared.AddChat(testutil.TestBotID, -100, ""); !errors.As(err, &op) {
		t.Fatalf("给主 bot 加群应被拒，得到 %v", err)
	}
}
