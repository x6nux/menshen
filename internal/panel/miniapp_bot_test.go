package panel

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"menshen/internal/testutil"
)

// TestMiniBotOwner：改归属——只有主管理员能动、目标必须是管理员、
// 主 bot 不改；改完运行中的实例要同步，否则判定豁免、专属联合封禁
// 账本与告警投递还会指向旧归属人。
func TestMiniBotOwner(t *testing.T) {
	reg, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	testutil.RegisterMainTestBot(t, sh, testutil.TestToken, testutil.TestBotID, 777)
	testutil.RegisterTestBot(t, sh, testToken2, 43, 888)
	for _, a := range []struct {
		uid  int64
		note string
	}{{888, "原归属"}, {999, "接手人"}} {
		if err := sh.AddAdmin(a.uid, a.note, 777); err != nil {
			t.Fatal(err)
		}
	}
	reg.LoadAll()
	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}
	init := env.adminInit()
	sub888 := signInitData(t, testToken2, map[string]string{
		"auth_date": strconv.FormatInt(env.now, 10), "user": `{"id":888}`})

	ownerOf := func(botID int64) int64 {
		rec := sh.Cache.Snap().Bots[botID]
		if rec == nil {
			t.Fatalf("bot %d 不存在", botID)
		}
		return rec.OwnerID
	}

	// 次级管理员改不了归属，哪怕是自己的 bot。
	w := miniDo(t, env.h, testToken2, sub888, 43, "bot",
		map[string]any{"bot_id": 43, "action": "owner", "owner_id": 999})
	if w.Code != http.StatusForbidden {
		t.Errorf("次管改归属应 403，得到 %d：%s", w.Code, w.Body.String())
	}
	if got := ownerOf(43); got != 888 {
		t.Errorf("被拒绝后归属应保持 888，得到 %d", got)
	}

	// 目标不是管理员的话，bot 会变成一个谁都管不了的黑盒，必须挡住。
	w = miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "bot",
		map[string]any{"bot_id": 43, "action": "owner", "owner_id": 12345})
	if w.Code != http.StatusBadRequest {
		t.Errorf("改派给非管理员应 400，得到 %d：%s", w.Code, w.Body.String())
	}

	// 主管理员改派成功：快照、数据库、运行中的实例三处都要跟着变。
	w = miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "bot",
		map[string]any{"bot_id": 43, "action": "owner", "owner_id": 999})
	if w.Code != http.StatusOK {
		t.Fatalf("改派应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	if got := ownerOf(43); got != 999 {
		t.Errorf("快照归属应为 999，得到 %d", got)
	}
	var dbOwner int64
	if err := sh.Store.Read.QueryRow(
		`SELECT owner_id FROM bots WHERE bot_id=43`).Scan(&dbOwner); err != nil {
		t.Fatal(err)
	}
	if dbOwner != 999 {
		t.Errorf("库里的归属应为 999，得到 %d", dbOwner)
	}
	if inst, ok := reg.LookupID(43); !ok {
		t.Error("改派不该影响实例存活")
	} else if inst.Owner() != 999 {
		t.Errorf("实例上的归属没同步，得到 %d", inst.Owner())
	}

	// 主 bot 由配置文件定义，归属不可改。
	w = miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "bot",
		map[string]any{"bot_id": testutil.TestBotID, "action": "owner", "owner_id": 999})
	if w.Code != http.StatusBadRequest {
		t.Errorf("改主 bot 归属应 400，得到 %d：%s", w.Code, w.Body.String())
	}

	// 归属候选名单只给主管理员，且包含主管与次管。
	w = miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "state", nil)
	var st map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	opts, ok := st["owner_opts"].([]any)
	if !ok || len(opts) != 3 {
		t.Fatalf("主管理员应看到 3 个归属候选，得到 %v", st["owner_opts"])
	}
	have := map[int64]bool{}
	for _, v := range opts {
		have[int64(v.(map[string]any)["user_id"].(float64))] = true
	}
	for _, uid := range []int64{777, 888, 999} {
		if !have[uid] {
			t.Errorf("归属候选缺少 %d：%v", uid, have)
		}
	}

	w = miniDo(t, env.h, testToken2, sub888, 43, "state", nil)
	st = nil
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if _, ok := st["owner_opts"]; ok {
		t.Error("次级管理员不该拿到归属候选名单")
	}
}

// TestMiniBotRemove：移除 bot——归属人能移自己的、动不了别人的；
// 主 bot 谁都不能移（由配置文件定义，删了下次启动又会被加回来）。
func TestMiniBotRemove(t *testing.T) {
	reg, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	testutil.RegisterMainTestBot(t, sh, testutil.TestToken, testutil.TestBotID, 777)
	testutil.RegisterTestBot(t, sh, testToken2, 43, 888)
	if err := sh.AddAdmin(888, "次管", 777); err != nil {
		t.Fatal(err)
	}
	reg.LoadAll()
	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}
	init := env.adminInit()
	sub888 := signInitData(t, testToken2, map[string]string{
		"auth_date": strconv.FormatInt(env.now, 10), "user": `{"id":888}`})

	// 次管动不了别人的 bot。
	w := miniDo(t, env.h, testToken2, sub888, 43, "bot",
		map[string]any{"bot_id": testutil.TestBotID, "action": "remove"})
	if w.Code != http.StatusForbidden {
		t.Errorf("次管移除别人的 bot 应 403，得到 %d：%s", w.Code, w.Body.String())
	}

	// 主 bot 由配置文件定义，主管理员也移不掉。
	w = miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "bot",
		map[string]any{"bot_id": testutil.TestBotID, "action": "remove"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("移除主 bot 应 400，得到 %d：%s", w.Code, w.Body.String())
	}
	if sh.Cache.Snap().Bots[testutil.TestBotID] == nil {
		t.Error("被拒绝后主 bot 的记录应原样保留")
	}

	// 归属人移除自己的 bot：记录、实例、库里的行都要清掉。
	w = miniDo(t, env.h, testToken2, sub888, 43, "bot",
		map[string]any{"bot_id": 43, "action": "remove"})
	if w.Code != http.StatusOK {
		t.Fatalf("归属人移除自己的 bot 应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	if _, ok := sh.Cache.Snap().Bots[43]; ok {
		t.Error("快照里 bot 没被移除")
	}
	if _, ok := reg.LookupID(43); ok {
		t.Error("实例没被摘掉")
	}
	var n int
	if err := sh.Store.Read.QueryRow(
		`SELECT COUNT(*) FROM bots WHERE bot_id=43`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("库里 bot 记录没被删除，剩 %d 行", n)
	}
}

// TestMiniBotModelsMainOnly：判定模型只有主管理员能配（与 TG 面板的
// a:mb:…:m 分支同一条规则），次管即使管得着自己的 bot 也不行。
func TestMiniBotModelsMainOnly(t *testing.T) {
	reg, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	testutil.RegisterMainTestBot(t, sh, testutil.TestToken, testutil.TestBotID, 777)
	testutil.RegisterTestBot(t, sh, testToken2, 43, 888)
	if err := sh.AddAdmin(888, "次管", 777); err != nil {
		t.Fatal(err)
	}
	reg.LoadAll()
	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}
	sub888 := signInitData(t, testToken2, map[string]string{
		"auth_date": strconv.FormatInt(env.now, 10), "user": `{"id":888}`})

	w := miniDo(t, env.h, testToken2, sub888, 43, "bot", map[string]any{
		"bot_id": 43, "action": "models", "which": "so", "value": "up1/m1"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("次管配模型应 403，得到 %d：%s", w.Code, w.Body.String())
	}
}
