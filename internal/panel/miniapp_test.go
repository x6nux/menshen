package panel

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"menshen/internal/antiad"
	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/testutil"
)

// signInitData 按 Telegram WebApp 的规则给字段签名，造一份合法 initData。
func signInitData(t *testing.T, token string, fields map[string]string) string {
	t.Helper()
	vals := url.Values{}
	for k, v := range fields {
		vals.Set(k, v)
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+"="+vals.Get(k))
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(strings.Join(lines, "\n")))
	vals.Set("hash", hex.EncodeToString(mac.Sum(nil)))
	return vals.Encode()
}

// miniDo 发一个带鉴权头的 API 请求。
func miniDo(t *testing.T, h http.Handler, token, initData string,
	botID int64, op string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw := []byte("{}")
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(http.MethodPost, "/miniapp/api/"+op,
		strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(miniInitDataHeader, initData)
	req.Header.Set(miniBotIDHeader, strconv.FormatInt(botID, 10))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// miniTestEnv 是 Mini App 测试环境：handler + 固定时间戳。
type miniTestEnv struct {
	t   *testing.T
	h   http.Handler
	sh  *core.Shared
	now int64
}

func newMiniEnv(t *testing.T) *miniTestEnv {
	t.Helper()
	_, b := testutil.NewTestRegistry(t, nil)
	return &miniTestEnv{t: t, h: MiniAppHandler(b.Shared), sh: b.Shared, now: time.Now().Unix()}
}

func (e *miniTestEnv) adminInit() string {
	return signInitData(e.t, testutil.TestToken, map[string]string{
		"auth_date": strconv.FormatInt(e.now, 10), "user": `{"id":777,"username":"tester"}`})
}

// TestValidateMiniInitData 守的是 Mini App 的鉴权：签名不对、过期、
// 没有用户都不放行。
func TestValidateMiniInitData(t *testing.T) {
	const token = testutil.TestToken
	now := time.Now().Unix()
	good := signInitData(t, token, map[string]string{
		"auth_date": strconv.FormatInt(now, 10),
		"user":      `{"id":777,"first_name":"admin","username":"admin_x"}`,
	})
	if uid, username, err := validateMiniInitData(token, good); err != nil || uid != 777 || username != "admin_x" {
		t.Fatalf("合法 initData 应通过并带出 username，uid=%d username=%q err=%v", uid, username, err)
	}

	other := signInitData(t, "999:othertoken", map[string]string{
		"auth_date": strconv.FormatInt(now, 10), "user": `{"id":777}`})
	if _, _, err := validateMiniInitData(token, other); err == nil {
		t.Error("别的 bot 的签名不该通过")
	}

	stale := signInitData(t, token, map[string]string{
		"auth_date": strconv.FormatInt(now-25*3600, 10), "user": `{"id":777}`})
	if _, _, err := validateMiniInitData(token, stale); err == nil {
		t.Error("超过 24 小时的 initData 不该通过")
	}

	if _, _, err := validateMiniInitData(token, good+"&extra=1"); err == nil {
		t.Error("追加字段应导致签名不匹配")
	}
}

// TestMiniAPIAuthAndState：没签名 401、非管理员 403、管理员拿到状态。
func TestMiniAPIAuthAndState(t *testing.T) {
	env := newMiniEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/miniapp/api/state", strings.NewReader("{}"))
	req.Header.Set(miniBotIDHeader, strconv.FormatInt(testutil.TestBotID, 10))
	w := httptest.NewRecorder()
	env.h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("没有 initData 应 401，得到 %d", w.Code)
	}

	outsider := signInitData(t, testutil.TestToken, map[string]string{
		"auth_date": strconv.FormatInt(env.now, 10), "user": `{"id":12345}`})
	w = miniDo(t, env.h, testutil.TestToken, outsider, testutil.TestBotID, "state", nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("非管理员应 403，得到 %d", w.Code)
	}

	w = miniDo(t, env.h, testutil.TestToken, env.adminInit(), testutil.TestBotID, "state", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("主管理员应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	var st map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st["me"].(map[string]any)["main"] != true {
		t.Error("应报告主管理员身份")
	}
	for _, key := range []string{"bots", "chats", "specs", "upstreams", "models", "admins", "gban"} {
		if _, ok := st[key]; !ok {
			t.Errorf("状态里缺少 %s", key)
		}
	}
}

// TestMiniSetPermissions：次级管理员不能改全局，只能改自己 bot 的参数。
func TestMiniSetPermissions(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	testutil.RegisterTestBot(t, sh, testToken2, 43, 888)
	if err := sh.AddAdmin(888, "次管", 777); err != nil {
		t.Fatal(err)
	}
	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}
	sub := signInitData(t, testutil.TestToken, map[string]string{
		"auth_date": strconv.FormatInt(env.now, 10), "user": `{"id":888}`})
	sub43 := signInitData(t, testToken2, map[string]string{
		"auth_date": strconv.FormatInt(env.now, 10), "user": `{"id":888}`})

	w := miniDo(t, env.h, testutil.TestToken, sub, testutil.TestBotID, "set",
		map[string]any{"scope": "global", "key": "antiad_enabled", "value": "1"})
	if w.Code != http.StatusForbidden {
		t.Errorf("次管改全局应 403，得到 %d", w.Code)
	}

	w = miniDo(t, env.h, testToken2, sub43, 43, "set",
		map[string]any{"scope": "bot", "bot_id": 43, "key": "antiad_cold", "value": "1"})
	if w.Code != http.StatusOK {
		t.Errorf("次管改自己 bot 的参数应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	if got := sh.Cache.Snap().BotSettingInt(43, "antiad_cold", 0); got != 1 {
		t.Errorf("参数没落库，得到 %d", got)
	}

	w = miniDo(t, env.h, testutil.TestToken, sub, testutil.TestBotID, "set",
		map[string]any{"scope": "bot", "bot_id": testutil.TestBotID,
			"key": "antiad_cold", "value": "1"})
	if w.Code != http.StatusForbidden {
		t.Errorf("次管改别人的 bot 应 403，得到 %d", w.Code)
	}
}

// TestMiniStateGlobalDefaults：跟随全局的配置项要在输入框占位里显示
// 「30(全局)」这样的全局默认值。次级管理员拿不到完整全局设置（global），
// 但 antiad/both 组的全局默认与两个模型列表键必须单独下发，否则他们的
// 机器人页画不出占位。主管理员同样要有，前端统一从这里取。
func TestMiniStateGlobalDefaults(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	testutil.RegisterTestBot(t, sh, testToken2, 43, 888)
	if err := sh.AddAdmin(888, "次管", 777); err != nil {
		t.Fatal(err)
	}
	if err := sh.PutSetting("antiad_rpm_chat", "42"); err != nil {
		t.Fatal(err)
	}
	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}
	sub := signInitData(t, testToken2, map[string]string{
		"auth_date": strconv.FormatInt(env.now, 10), "user": `{"id":888}`})

	w := miniDo(t, env.h, testToken2, sub, 43, "state", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("次级管理员应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	var st map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if _, ok := st["global"]; ok {
		t.Error("次级管理员不应拿到完整全局设置")
	}
	gd, ok := st["global_defaults"].(map[string]any)
	if !ok {
		t.Fatal("状态里缺少 global_defaults")
	}
	if gd["antiad_rpm_chat"] != "42" {
		t.Errorf("antiad_rpm_chat 全局默认应为 42，得到 %v", gd["antiad_rpm_chat"])
	}
	if _, ok := gd["antiad_so_models"]; !ok {
		t.Error("判定模型的全局默认也要下发，供输入框回显")
	}
	if _, ok := gd["antiad_llm_models"]; !ok {
		t.Error("复判模型的全局默认也要下发，供输入框回显")
	}
	if _, ok := gd["antiad_rule_model"]; !ok {
		t.Error("规则发现模型的全局默认也要下发，供输入框回显")
	}

	// 主管理员也拿得到（前端统一从 global_defaults 取占位值）。
	w = miniDo(t, env.h, testutil.TestToken, env.adminInit(), testutil.TestBotID, "state", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("主管理员应 200，得到 %d", w.Code)
	}
	st = nil
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if gd, ok = st["global_defaults"].(map[string]any); !ok || gd["antiad_rpm_chat"] != "42" {
		t.Error("主管理员也应有 global_defaults")
	}
}

// TestMiniMutations：群组添加/切换、白名单增删、模型新增（仅主管理员）。
func TestMiniMutations(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}
	init := env.adminInit()
	call := func(op string, body any) *httptest.ResponseRecorder {
		return miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, op, body)
	}

	if w := call("chat", map[string]any{"bot_id": testutil.TestBotID,
		"chat_id": -100999, "action": "add"}); w.Code != http.StatusOK {
		t.Fatalf("添加群应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	if _, ok := sh.Cache.Snap().ChatConf(testutil.TestBotID, -100999); !ok {
		t.Fatal("群没落库")
	}
	if w := call("chat", map[string]any{"bot_id": testutil.TestBotID,
		"chat_id": -100999, "action": "update", "dryrun": false}); w.Code != http.StatusOK {
		t.Fatalf("切换应 200，得到 %d", w.Code)
	}
	if c, _ := sh.Cache.Snap().ChatConf(testutil.TestBotID, -100999); c.Dryrun {
		t.Error("演练开关没生效")
	}
	if w := call("chat", map[string]any{"bot_id": testutil.TestBotID,
		"chat_id": -100999, "action": "remove"}); w.Code != http.StatusOK {
		t.Fatal("移除应 200")
	}
	if _, ok := sh.Cache.Snap().ChatConf(testutil.TestBotID, -100999); ok {
		t.Error("群没被移除")
	}

	if w := call("whitelist", map[string]any{"bot_id": testutil.TestBotID,
		"chat_id": -100, "user_id": 555, "hours": 24, "action": "add"}); w.Code != http.StatusOK {
		t.Fatalf("白名单添加应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	if !sh.Cache.Snap().Whitelisted(testutil.TestBotID, -100, 555, env.now) {
		t.Fatal("白名单没生效")
	}
	if w := call("whitelist", map[string]any{"bot_id": testutil.TestBotID,
		"chat_id": -100, "user_id": 555, "action": "remove"}); w.Code != http.StatusOK {
		t.Fatal("白名单移除应 200")
	}
	if sh.Cache.Snap().Whitelisted(testutil.TestBotID, -100, 555, env.now) {
		t.Error("白名单没被移除")
	}

	if w := call("model", map[string]any{"action": "add", "upstream": "up1",
		"model_id": "m1"}); w.Code == http.StatusOK {
		t.Error("上游不存在时新增模型应失败")
	}
	if _, err := sh.Store.Write.Exec(`INSERT INTO upstreams
		(name,base_url,api_key,weight,status,supports_chat,supports_systemone)
		VALUES ('up1','http://x','k',1,1,1,1)`); err != nil {
		t.Fatal(err)
	}
	if err := sh.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	if w := call("model", map[string]any{"action": "add", "upstream": "up1",
		"model_id": "m1", "prompt_price": 0.1, "completion_price": 0.2}); w.Code != http.StatusOK {
		t.Fatalf("新增模型应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	if sh.Cache.Snap().Models["up1/m1"] == nil {
		t.Error("模型没落库")
	}
	// 负价会让开销核算变成负数；超长模型名会撑爆 callback_data（64 字节）。
	if w := call("model", map[string]any{"action": "add", "upstream": "up1",
		"model_id": "m2", "prompt_price": -1}); w.Code != http.StatusBadRequest {
		t.Errorf("负价格应 400，得到 %d：%s", w.Code, w.Body.String())
	}
	if w := call("model", map[string]any{"action": "add", "upstream": "up1",
		"model_id": strings.Repeat("x", 60)}); w.Code != http.StatusBadRequest {
		t.Errorf("超长模型名应 400，得到 %d：%s", w.Code, w.Body.String())
	}
}

// TestClampPage：页码必须有上限。没有上限时 (page-1)*size 会算出巨大的
// OFFSET（极端值还会溢出成负数），一次查询就把库扫一遍。
func TestClampPage(t *testing.T) {
	cases := []struct {
		in   int64
		want int64
	}{{0, 1}, {-5, 1}, {3, 3}, {maxPanelPage + 1, maxPanelPage}, {1 << 62, maxPanelPage}}
	for _, c := range cases {
		if got := clampPage(c.in); got != c.want {
			t.Errorf("clampPage(%d) = %d, 期望 %d", c.in, got, c.want)
		}
	}
	if got := clampPageInt(int(^uint(0) >> 1)); got != maxPanelPage {
		t.Errorf("clampPageInt(MaxInt) = %d, 期望 %d", got, maxPanelPage)
	}
}

// TestMiniBotAntiBanSettingPersists：bot 级「禁言改为封禁」的写入必须落库、
// 进快照、并让该 bot 名下跟随的群立刻改为永久封禁。
// 用户报过「设置了永久封禁却还是按默认时长禁言」，先守住这条链路。
func TestMiniBotAntiBanSettingPersists(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	testutil.EnableAntiad(t, b, -100)
	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}
	init := env.adminInit()

	w := miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "set",
		map[string]any{"scope": "bot", "bot_id": testutil.TestBotID,
			"key": "antiad_ban", "value": "1"})
	if w.Code != http.StatusOK {
		t.Fatalf("写入应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	if got := sh.Cache.Snap().BotSetting(testutil.TestBotID, "antiad_ban"); got != "1" {
		t.Fatalf("bot 覆盖没生效，得到 %q", got)
	}
	c, _ := sh.Cache.Snap().ChatConf(testutil.TestBotID, -100)
	if !sh.Cache.Snap().BanMode(c) {
		t.Error("开启后跟随 bot 的群应改为永久封禁")
	}

	// 状态里要能拿到全局默认，界面才能把「跟随」解析成实际处罚。
	w = miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "state", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("state 应 200，得到 %d", w.Code)
	}
	var st map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if _, ok := st["global_defaults"].(map[string]any)["antiad_mute_minutes"]; !ok {
		t.Error("状态里应包含 antiad_mute_minutes 全局默认值")
	}
	if me, ok := st["me"].(map[string]any); !ok || me["username"] != "tester" {
		t.Errorf("state.me 应带 initData 里的 username，得到 %v", st["me"])
	}
}

// TestMiniGlobalDefaultsAndModels：Mini App 要能改「全局默认参数」与
// 「默认模型」。此前只能设 per-bot 覆盖，各 bot 页里的「全局 XX」没地方改。
func TestMiniGlobalDefaultsAndModels(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}
	init := env.adminInit()

	w := miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "set",
		map[string]any{"scope": "global", "key": "antiad_mute_minutes", "value": "0"})
	if w.Code != http.StatusOK {
		t.Fatalf("全局默认参数应可写，得到 %d：%s", w.Code, w.Body.String())
	}
	if got := sh.Cache.Snap().Setting("antiad_mute_minutes"); got != "0" {
		t.Errorf("全局默认没落上，得到 %q", got)
	}

	// 默认模型列表：必须是已登记且启用的模型。
	for _, n := range []string{"up1/m1", "up1/m2"} {
		if _, err := sh.Store.Write.Exec(`INSERT INTO models (name,prompt_price,
			completion_price,cache_read_price,cache_write_price,enabled)
			VALUES (?,0,0,0,0,1)`, n); err != nil {
			t.Fatal(err)
		}
	}
	if err := sh.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	w = miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "set",
		map[string]any{"scope": "global", "key": "antiad_so_models",
			"value": "up1/m1, up1/m2"})
	if w.Code != http.StatusOK {
		t.Fatalf("默认模型列表应可写，得到 %d：%s", w.Code, w.Body.String())
	}
	if got := sh.Cache.Snap().SettingStrings("antiad_so_models"); !slices.Equal(got, []string{"up1/m1", "up1/m2"}) {
		t.Errorf("默认模型列表没落上，得到 %v", got)
	}
	w = miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "set",
		map[string]any{"scope": "global", "key": "antiad_vision_model", "value": "up1/m1"})
	if w.Code != http.StatusOK {
		t.Fatalf("识图模型应可写，得到 %d：%s", w.Code, w.Body.String())
	}
	if got := sh.Cache.Snap().Setting("antiad_vision_model"); got != "up1/m1" {
		t.Errorf("识图模型没落上，得到 %q", got)
	}
	// 未登记的模型要拒绝。
	w = miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "set",
		map[string]any{"scope": "global", "key": "antiad_vision_model", "value": "nope/m9"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("未登记的识图模型应 400，得到 %d", w.Code)
	}

	// 规则发现模型：单项、可留空；未登记或已停用一律 400。
	w = miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "set",
		map[string]any{"scope": "global", "key": "antiad_rule_model", "value": "up1/m1"})
	if w.Code != http.StatusOK {
		t.Fatalf("规则发现模型应可写，得到 %d：%s", w.Code, w.Body.String())
	}
	if got := sh.Cache.Snap().Setting("antiad_rule_model"); got != "up1/m1" {
		t.Errorf("规则发现模型没落上，得到 %q", got)
	}
	if _, err := sh.Store.Write.Exec(`INSERT INTO models (name,prompt_price,
		completion_price,cache_read_price,cache_write_price,enabled)
		VALUES ('up1/m3',0,0,0,0,0)`); err != nil {
		t.Fatal(err)
	}
	if err := sh.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"未登记": "nope/m9", "已停用": "up1/m3"} {
		w = miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "set",
			map[string]any{"scope": "global", "key": "antiad_rule_model", "value": value})
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s的规则发现模型应 400，得到 %d：%s", name, w.Code, w.Body.String())
		}
	}
	w = miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "set",
		map[string]any{"scope": "global", "key": "antiad_rule_model", "value": ""})
	if w.Code != http.StatusOK {
		t.Fatalf("规则发现模型应可留空，得到 %d：%s", w.Code, w.Body.String())
	}
	if got := sh.Cache.Snap().Setting("antiad_rule_model"); got != "" {
		t.Errorf("留空应把规则发现模型清空，得到 %q", got)
	}
}

// TestMiniStateSections：设置页的合并视图——同一张卡里既有全局项
// （记录保留天数）也有各 bot 的默认值（禁言时长），并按主题分组下发。
func TestMiniStateSections(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	env := &miniTestEnv{t: t, h: MiniAppHandler(b.Shared), now: time.Now().Unix()}
	init := env.adminInit()

	w := miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "state", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("state 应 200，得到 %d", w.Code)
	}
	var st map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	secs, ok := st["sections"].([]any)
	if !ok || len(secs) == 0 {
		t.Fatalf("state 应带 sections，得到 %v", st["sections"])
	}
	keys := map[string]bool{}
	var names []string
	for _, s := range secs {
		m := s.(map[string]any)
		names = append(names, m["name"].(string))
		for _, sp := range m["specs"].([]any) {
			keys[sp.(map[string]any)["key"].(string)] = true
		}
	}
	if names[0] != "处置与分档" {
		t.Errorf("第一个分组应是「处置与分档」，得到 %q", names[0])
	}
	for _, want := range []string{"antiad_mute_minutes", "log_retention_days", "antiad_cold"} {
		if !keys[want] {
			t.Errorf("合并后的分组里缺少 %s（全局项与 bot 默认值应在同一视图）", want)
		}
	}
}

// TestMiniStateTZAndSettingsSet：展示时区要给所有管理员下发（次管没有 global，
// 前端全站时间格式化依赖顶层 tz_name）；settings_set 只给主管理员，且只包含
// 显式写过的键——快照里铺的代码默认值不算「已设置」。
func TestMiniStateTZAndSettingsSet(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	if err := sh.PutSetting("tz_name", "Pacific/Kiritimati"); err != nil {
		t.Fatal(err)
	}
	if err := sh.PutSetting("antiad_mute_minutes", "30"); err != nil {
		t.Fatal(err)
	}
	testutil.RegisterTestBot(t, sh, testToken2, 43, 888)
	if err := sh.AddAdmin(888, "次管", 777); err != nil {
		t.Fatal(err)
	}
	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}

	stateOf := func(token, initData string, botID int64) map[string]any {
		t.Helper()
		w := miniDo(t, env.h, token, initData, botID, "state", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("state 应 200，得到 %d：%s", w.Code, w.Body.String())
		}
		var st map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		return st
	}

	main := stateOf(testutil.TestToken, env.adminInit(), testutil.TestBotID)
	if got := main["tz_name"]; got != "Pacific/Kiritimati" {
		t.Errorf("主管理员 state 的 tz_name 应为 Pacific/Kiritimati，得到 %v", got)
	}
	setList, ok := main["settings_set"].([]any)
	if !ok {
		t.Fatalf("主管理员 state 应带 settings_set，得到 %v", main["settings_set"])
	}
	set := map[string]bool{}
	for _, k := range setList {
		set[k.(string)] = true
	}
	for _, want := range []string{"tz_name", "antiad_mute_minutes"} {
		if !set[want] {
			t.Errorf("settings_set 应包含显式写过的 %s，得到 %v", want, setList)
		}
	}
	if set["log_retention_days"] {
		t.Errorf("settings_set 不应把只有代码默认值的键算成已设置，得到 %v", setList)
	}

	sub := signInitData(t, testToken2, map[string]string{
		"auth_date": strconv.FormatInt(env.now, 10), "user": `{"id":888}`})
	subState := stateOf(testToken2, sub, 43)
	if got := subState["tz_name"]; got != "Pacific/Kiritimati" {
		t.Errorf("次级管理员 state 也应带 tz_name，得到 %v", got)
	}
}

// TestMiniAppProfileOKListAndRevoke：复判给的「资料放行」要能在 App 里
// 看到并撤销（它只免资料这一路，不是整号放行，所以单列一张卡）。
func TestMiniAppProfileOKListAndRevoke(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}
	botID := b.BotID()
	now := time.Now().Unix()
	if _, err := b.Store.Write.Exec(`INSERT INTO profile_ok
		(bot_id,user_id,phash,hours,reason,created_at,expires_at)
		VALUES (?,?,?,?,?,?,?)`, botID, 555, "abc", 24, "复判放行：只有资料可疑", now, now+24*3600); err != nil {
		t.Fatal(err)
	}
	if err := sh.Cache.Reload(); err != nil {
		t.Fatal(err)
	}

	w := miniDo(t, env.h, testutil.TestToken, env.adminInit(), botID, "state", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("state 应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	var st struct {
		ProfileOK []struct {
			UserID int64 `json:"user_id"`
			Hours  int64 `json:"hours"`
		} `json:"profile_ok"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.ProfileOK) != 1 || st.ProfileOK[0].UserID != 555 ||
		st.ProfileOK[0].Hours != 24 {
		t.Fatalf("状态里应带上资料放行清单，得到 %+v", st.ProfileOK)
	}

	// 撤销。
	w = miniDo(t, env.h, testutil.TestToken, env.adminInit(), botID, "whitelist",
		map[string]any{"action": "unprofile", "bot_id": botID, "user_id": 555})
	if w.Code != http.StatusOK {
		t.Fatalf("撤销应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	var n int
	if err := b.Store.Read.QueryRow(`SELECT COUNT(*) FROM profile_ok`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("撤销后不该还有行，剩 %d", n)
	}
}

// TestMiniAppUserDossier：Mini App 的用户页接口要给出资料卡（昵称/用户名/
// ID/简介/发言统计）与判定记录，且默认只列被处置过的（filter=all 才全列）。
func TestMiniAppUserDossier(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}
	botID := b.BotID()
	fake := b.TG.(*testutil.FakeTG)
	fake.Resp["getChat"] = `{"ok":true,"result":{"first_name":"白展堂",` +
		`"username":"tgdsBZT","bio":"我的TG频道 @tgds100"}}`

	paid := seedMiniLog(t, b, 555, "广告原文", "deleted_muted")
	clean := seedMiniLog(t, b, 555, "日常闲聊", "none")

	// 默认：只看被处置过的。
	w := miniDo(t, env.h, testutil.TestToken, env.adminInit(), botID, "user",
		map[string]any{"user_id": 555, "bot_id": botID, "filter": "act"})
	if w.Code != http.StatusOK {
		t.Fatalf("user 接口应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	var out struct {
		UserID    int64  `json:"user_id"`
		Name      string `json:"name"`
		Username  string `json:"username"`
		Bio       string `json:"bio"`
		Total     int64  `json:"total"`
		Processed int64  `json:"processed"`
		Logs      []struct {
			ID int64 `json:"id"`
		} `json:"logs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.UserID != 555 || out.Name != "白展堂" || out.Username != "tgdsBZT" ||
		!strings.Contains(out.Bio, "tgds100") {
		t.Errorf("资料卡字段不对：%+v", out)
	}
	if out.Total != 2 || out.Processed != 1 {
		t.Errorf("统计应为 共2/处置1，得到 共%d/处置%d", out.Total, out.Processed)
	}
	if len(out.Logs) != 1 || out.Logs[0].ID != paid {
		t.Errorf("默认只该给被处置过的 #%d，得到 %+v", paid, out.Logs)
	}

	// filter=all：两条都列。
	w = miniDo(t, env.h, testutil.TestToken, env.adminInit(), botID, "user",
		map[string]any{"user_id": 555, "bot_id": botID, "filter": "all"})
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Logs) != 2 {
		t.Errorf("filter=all 应列出两条，得到 %+v", out.Logs)
	}
	_ = clean
}

// seedMiniLog 落一条流水（Mini App 测试用）。
func seedMiniLog(t *testing.T, b *core.Bot, uid int64, text, action string) int64 {
	t.Helper()
	res, err := b.Store.Write.Exec(`INSERT INTO antiad_log (chat_id,user_id,message_id,text,
		verdict,confidence,decider,ad_kind,action,reason,created_at,bot_id)
		VALUES (-100,?,7,?,'ad',0.95,'systemone','scam',?,'',0,?)`, uid, text, action, b.BotID())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if action == "none" {
		if _, err := b.Store.Write.Exec(`UPDATE antiad_log SET verdict='clean' WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// TestMiniAppBackfillAction：群组卡片上的「补全历史入群时间」要能手动触发
// （绕过自动触发的 24 小时冷却），并把结果交给私聊通知。
func TestMiniAppBackfillAction(t *testing.T) {
	reg, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}
	botID := b.BotID()
	chatID := int64(-100)
	testutil.EnableAntiad(t, b, chatID)
	b.Cfg.TGAPIID, b.Cfg.TGAPIHash = 2040, "hash"

	// 换掉脚本执行器：只记录被调用。
	type call struct {
		chat     int64
		username string
	}
	calls := make(chan call, 4)
	oldRunner := antiad.SwapBackfillRunner(func(_ *core.Bot, chat int64, username string) ([]antiad.BackfillRow, error) {
		calls <- call{chat, username}
		return nil, nil
	})
	defer antiad.SwapBackfillRunner(oldRunner)
	_ = reg

	w := miniDo(t, env.h, testutil.TestToken, env.adminInit(), botID, "chat",
		map[string]any{"bot_id": botID, "chat_id": chatID, "action": "backfill"})
	if w.Code != http.StatusOK {
		t.Fatalf("补全应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	select {
	case c := <-calls:
		if c.chat != chatID {
			t.Errorf("应带上群 id，得到 %d", c.chat)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("应触发补全任务")
	}

	// 缺配置时要给出明确原因，而不是静默成功。
	b.Cfg.TGAPIID, b.Cfg.TGAPIHash = 0, ""
	w = miniDo(t, env.h, testutil.TestToken, env.adminInit(), botID, "chat",
		map[string]any{"bot_id": botID, "chat_id": chatID, "action": "backfill"})
	if w.Code == http.StatusOK {
		t.Error("没配 tg_api_id 时应报错，而不是假装已开始")
	}
}

// TestMiniUserWithoutBodyBotID：用户页不带 bot_id 时要用 X-Bot-Id 头兜底。
// 以前这里读的是 w.Header().Get("")（永远空串），botID 一直是 0 —— 所有人
// （包括主管理员）都会被判「无权查看该 bot 的数据」。
func TestMiniUserWithoutBodyBotID(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	if _, err := sh.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,created_at,bot_id)
		VALUES (-100,555,7,'广告','ad',0.9,'llm','scam','deleted_muted','',?,?)`,
		time.Now().Unix(), b.BotID()); err != nil {
		t.Fatal(err)
	}
	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}
	w := miniDo(t, env.h, testutil.TestToken, env.adminInit(), b.BotID(), "user",
		map[string]any{"user_id": 555})
	if w.Code != http.StatusOK {
		t.Fatalf("不带 bot_id 时应靠 X-Bot-Id 头通过，得到 %d：%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"user_id":555`) {
		t.Errorf("应返回该用户的资料：%s", w.Body.String())
	}
}

// TestMiniAppTodoCounts：工作台待办（state.todo）的口径——未结申诉按权限
// 与「未结」状态集合统计，演练群与停用 bot 按可见范围统计；申诉结案后
// 未结计数要跟着减少。
func TestMiniAppTodoCounts(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	testutil.RegisterTestBot(t, sh, testToken2, 43, 888)
	if err := sh.AddAdmin(888, "次管", 777); err != nil {
		t.Fatal(err)
	}
	// bot 43 停用；两个 bot 各挂一个正式群与一个演练群。
	if _, err := sh.Store.Write.Exec(`UPDATE bots SET enabled=0 WHERE bot_id=43`); err != nil {
		t.Fatal(err)
	}
	seedChat := func(botID, chatID, dryrun int64) {
		if _, err := sh.Store.Write.Exec(`INSERT INTO bot_chats
			(bot_id,chat_id,title,enabled,dryrun,group_alert,created_at)
			VALUES (?,?,?,1,?,0,0)`, botID, chatID, "测试群", dryrun); err != nil {
			t.Fatal(err)
		}
	}
	seedChat(testutil.TestBotID, -100, 0)
	seedChat(testutil.TestBotID, -101, 1)
	seedChat(43, -200, 0)
	seedChat(43, -201, 1)
	// 申诉：主 bot 名下一张未结一张已结，43 名下一张未结。
	seedAppeal := func(botID int64, status string) {
		if _, err := sh.Store.Write.Exec(`INSERT INTO appeals
			(bot_id,user_id,status,created_at,updated_at) VALUES (?,?,?,0,0)`,
			botID, 555, status); err != nil {
			t.Fatal(err)
		}
	}
	seedAppeal(testutil.TestBotID, "noweb")
	seedAppeal(testutil.TestBotID, "lifted")
	seedAppeal(43, "statement")
	if err := sh.Cache.Reload(); err != nil {
		t.Fatal(err)
	}

	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}
	todoOf := func(token, initData string, botID int64) map[string]any {
		t.Helper()
		w := miniDo(t, env.h, token, initData, botID, "state", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("state 应 200，得到 %d：%s", w.Code, w.Body.String())
		}
		var st map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		todo, ok := st["todo"].(map[string]any)
		if !ok {
			t.Fatalf("state 缺少 todo：%v", st)
		}
		return todo
	}
	num := func(todo map[string]any, key string) int64 {
		t.Helper()
		f, ok := todo[key].(float64)
		if !ok {
			t.Fatalf("todo.%s 不是数字：%v", key, todo[key])
		}
		return int64(f)
	}

	// 主管理员：两个 bot 可见（43 停用），两张未结申诉，两个演练群。
	todo := todoOf(testutil.TestToken, env.adminInit(), testutil.TestBotID)
	if got := num(todo, "open_appeals"); got != 2 {
		t.Errorf("未结申诉应为 2，得到 %d", got)
	}
	if got := num(todo, "dryrun_chats"); got != 2 {
		t.Errorf("演练群应为 2，得到 %d", got)
	}
	if got := num(todo, "disabled_bots"); got != 1 {
		t.Errorf("停用 bot 应为 1，得到 %d", got)
	}

	// 次级管理员：只看得到自己名下的 43——一张未结申诉、一个演练群、一个停用 bot。
	sub := signInitData(t, testToken2, map[string]string{
		"auth_date": strconv.FormatInt(env.now, 10), "user": `{"id":888}`})
	todo = todoOf(testToken2, sub, 43)
	if got := num(todo, "open_appeals"); got != 1 {
		t.Errorf("次管可见的未结申诉应为 1，得到 %d", got)
	}
	if got := num(todo, "dryrun_chats"); got != 1 {
		t.Errorf("次管可见的演练群应为 1，得到 %d", got)
	}
	if got := num(todo, "disabled_bots"); got != 1 {
		t.Errorf("次管可见的停用 bot 应为 1，得到 %d", got)
	}

	// 申诉结案后未结计数减少。
	if _, err := sh.Store.Write.Exec(
		`UPDATE appeals SET status='lifted' WHERE bot_id=? AND status='noweb'`,
		testutil.TestBotID); err != nil {
		t.Fatal(err)
	}
	todo = todoOf(testutil.TestToken, env.adminInit(), testutil.TestBotID)
	if got := num(todo, "open_appeals"); got != 1 {
		t.Errorf("结案后未结申诉应为 1，得到 %d", got)
	}
	if got := num(todo, "dryrun_chats"); got != 2 {
		t.Errorf("结案不应影响演练群计数，得到 %d", got)
	}
}

// TestMiniAppLogsTotal：记录列表要给出「当前筛选下的总数」，供前端无限
// 滚动判断还有没有下一页；total 的口径必须与列表的 WHERE 一致，且不随
// 分页变化。
func TestMiniAppLogsTotal(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}
	init := env.adminInit()
	seed := func(verdict, action, text string) {
		if _, err := sh.Store.Write.Exec(`INSERT INTO antiad_log
			(bot_id,chat_id,user_id,message_id,text,verdict,confidence,decider,
			 ad_kind,action,reason,created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			testutil.TestBotID, -100, 555, 7, text, verdict, 0.9, "so",
			"", action, "", env.now); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 25; i++ {
		seed("ad", "deleted_muted", "加微信买号")
	}
	for i := 0; i < 3; i++ {
		seed("clean", "none", "日常闲聊")
	}
	seed("skipped", "skipped", "加微信但被护栏拦下")

	call := func(body map[string]any) map[string]any {
		t.Helper()
		w := miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "logs", body)
		if w.Code != http.StatusOK {
			t.Fatalf("logs 应 200，得到 %d：%s", w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	total := func(out map[string]any) int64 {
		t.Helper()
		f, ok := out["total"].(float64)
		if !ok {
			t.Fatalf("logs 响应缺少 total：%v", out)
		}
		return int64(f)
	}

	if got := total(call(map[string]any{"verdict": "deleted"})); got != 25 {
		t.Errorf("已删除筛选的 total 应为 25，得到 %d", got)
	}
	if got := total(call(map[string]any{"verdict": "clean"})); got != 3 {
		t.Errorf("正常筛选的 total 应为 3，得到 %d", got)
	}
	if got := total(call(map[string]any{"verdict": "skipped"})); got != 1 {
		t.Errorf("跳过筛选的 total 应为 1，得到 %d", got)
	}
	if got := total(call(map[string]any{"q": "微信"})); got != 26 {
		t.Errorf("搜索「微信」的 total 应为 26，得到 %d", got)
	}
	// 与分页无关：翻页后仍是同一批筛选下的总数。
	p1 := call(map[string]any{"verdict": "deleted", "page": 1})
	p2 := call(map[string]any{"verdict": "deleted", "page": 2})
	if total(p1) != total(p2) || total(p1) != 25 {
		t.Errorf("total 不应随页码变化：第 1 页 %d，第 2 页 %d", total(p1), total(p2))
	}
	if rows := p1["logs"].([]any); len(rows) != 20 {
		t.Errorf("第 1 页应为 20 行，得到 %d", len(rows))
	}
	if rows := p2["logs"].([]any); len(rows) != 5 {
		t.Errorf("第 2 页应为 5 行，得到 %d", len(rows))
	}
}

// TestMiniAppChatBulkUpdate：群组批量更新——一个请求只碰一个 bot 的群
// （bot_id 必填，防跨 bot 误伤同 chat_id 的行），fields 里没出现的键不改，
// 上限 100 个。
func TestMiniAppChatBulkUpdate(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	testutil.RegisterTestBot(t, sh, testToken2, 43, 888)
	if err := sh.AddAdmin(888, "次管", 777); err != nil {
		t.Fatal(err)
	}
	seedChat := func(botID, chatID, enabled, dryrun, punish int64) {
		if _, err := sh.Store.Write.Exec(`INSERT INTO bot_chats
			(bot_id,chat_id,title,enabled,dryrun,group_alert,punish,created_at)
			VALUES (?,?,?,?,?,0,?,0)`,
			botID, chatID, "测试群", enabled, dryrun, punish); err != nil {
			t.Fatal(err)
		}
	}
	seedChat(testutil.TestBotID, -100, 1, 0, 0)
	seedChat(testutil.TestBotID, -101, 1, 1, 1)
	// 另一个 bot 名下有同一个 chat_id：批量更新不能跨 bot 误伤。
	seedChat(43, -100, 1, 0, 0)
	if err := sh.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}
	init := env.adminInit()
	sub := signInitData(t, testToken2, map[string]string{
		"auth_date": strconv.FormatInt(env.now, 10), "user": `{"id":888}`})
	conf := func(botID, chatID int64) store.BotChat {
		t.Helper()
		c, ok := sh.Cache.Snap().ChatConf(botID, chatID)
		if !ok {
			t.Fatalf("群 %d/%d 不在快照里", botID, chatID)
		}
		return c
	}

	// 缺 bot_id → 400（而不是落到权限检查报 403）。
	w := miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "chat",
		map[string]any{"action": "bulk_update", "chat_ids": []int64{-100},
			"fields": map[string]any{"dryrun": true}})
	if w.Code != http.StatusBadRequest {
		t.Errorf("缺 bot_id 应 400，得到 %d：%s", w.Code, w.Body.String())
	}

	// 次管批量改别人的 bot → 403。
	w = miniDo(t, env.h, testToken2, sub, 43, "chat",
		map[string]any{"action": "bulk_update", "bot_id": testutil.TestBotID,
			"chat_ids": []int64{-100}, "fields": map[string]any{"dryrun": true}})
	if w.Code != http.StatusForbidden {
		t.Errorf("次管批量改别人的 bot 应 403，得到 %d：%s", w.Code, w.Body.String())
	}

	// 主管理员批量：目标 bot 的群生效，不存在的 chat_id 跳过，
	// 另一个 bot 的同 chat_id 不受影响。
	w = miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "chat",
		map[string]any{"action": "bulk_update", "bot_id": testutil.TestBotID,
			"chat_ids": []int64{-100, -101, -999},
			"fields":   map[string]any{"dryrun": true}})
	if w.Code != http.StatusOK {
		t.Fatalf("批量更新应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["note"] != "已更新 2 个群" {
		t.Errorf("成功提示应为「已更新 2 个群」，得到 %v", out["note"])
	}
	// 「没出现的键不改」：只带 dryrun 时 enabled/punish 保持原样。
	if c := conf(testutil.TestBotID, -100); !c.Dryrun || !c.Enabled || c.Punish != 0 {
		t.Errorf("主 bot 的 -100 应只改 dryrun，得到 %+v", c)
	}
	if c := conf(testutil.TestBotID, -101); !c.Dryrun || !c.Enabled || c.Punish != 1 {
		t.Errorf("主 bot 的 -101 应保持 enabled/punish，得到 %+v", c)
	}
	if c := conf(43, -100); c.Dryrun || !c.Enabled || c.Punish != 0 {
		t.Errorf("bot 43 的 -100 不该被跨 bot 误改，得到 %+v", c)
	}

	// 多个字段一起改：enabled/punish 同时落库。
	w = miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "chat",
		map[string]any{"action": "bulk_update", "bot_id": testutil.TestBotID,
			"chat_ids": []int64{-100},
			"fields":   map[string]any{"enabled": false, "punish": 1}})
	if w.Code != http.StatusOK {
		t.Fatalf("批量更新应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	if c := conf(testutil.TestBotID, -100); c.Enabled || c.Punish != 1 {
		t.Errorf("enabled/punish 应同时落库，得到 %+v", c)
	}
	// punish 越界 → 400，且不能落库。
	w = miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "chat",
		map[string]any{"action": "bulk_update", "bot_id": testutil.TestBotID,
			"chat_ids": []int64{-100}, "fields": map[string]any{"punish": 2}})
	if w.Code != http.StatusBadRequest {
		t.Errorf("punish 越界应 400，得到 %d：%s", w.Code, w.Body.String())
	}
	if c := conf(testutil.TestBotID, -100); c.Punish != 1 {
		t.Errorf("越界的 punish 不该落库，得到 %+v", c)
	}

	// 空数组与超过 100 个都拒绝。
	w = miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "chat",
		map[string]any{"action": "bulk_update", "bot_id": testutil.TestBotID,
			"chat_ids": []int64{}, "fields": map[string]any{"dryrun": true}})
	if w.Code != http.StatusBadRequest {
		t.Errorf("空 chat_ids 应 400，得到 %d", w.Code)
	}
	big := make([]int64, 101)
	for i := range big {
		big[i] = int64(-1000 - i)
	}
	w = miniDo(t, env.h, testutil.TestToken, init, testutil.TestBotID, "chat",
		map[string]any{"action": "bulk_update", "bot_id": testutil.TestBotID,
			"chat_ids": big, "fields": map[string]any{"dryrun": true}})
	if w.Code != http.StatusBadRequest {
		t.Errorf("超过 100 个 chat_ids 应 400，得到 %d", w.Code)
	}

	// 次管批量改自己名下的 bot：放行并落库。
	w = miniDo(t, env.h, testToken2, sub, 43, "chat",
		map[string]any{"action": "bulk_update", "bot_id": 43,
			"chat_ids": []int64{-100}, "fields": map[string]any{"group_alert": true}})
	if w.Code != http.StatusOK {
		t.Fatalf("次管批量改自己的 bot 应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	if c := conf(43, -100); !c.GroupAlert {
		t.Errorf("group_alert 应落库，得到 %+v", c)
	}
}

// TestMiniAppServesEmbeddedApp：/miniapp 由嵌入的前端产物托管。
// 已 npm --prefix web run build 时验证真实产物；产物缺失（全新克隆，webdist
// 里只有 .gitkeep）时验证 503 占位页（不跳过整个测试，两条路径都要有回归）。
func TestMiniAppServesEmbeddedApp(t *testing.T) {
	env := newMiniEnv(t)
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		env.h.ServeHTTP(w, req)
		return w
	}

	// API 只收 POST：GET 必须 405，不能落入 SPA 回退。
	if w := get("/miniapp/api"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /miniapp/api 应 405，得到 %d", w.Code)
	}

	dist, ok := miniAppDistFS()
	if !ok {
		// 无产物（untagged 构建，或带 tag 但没跑 npm run build）：
		// 入口页给 503 构建提示，其余前端路由没有页面可回退。
		w := get("/miniapp")
		if w.Code != http.StatusServiceUnavailable ||
			!strings.Contains(w.Body.String(), "前端未构建") {
			t.Fatalf("无产物时 /miniapp 应 503 占位页，得到 %d：%s", w.Code, w.Body.String())
		}
		if w := get("/miniapp/xxx"); w.Code != http.StatusNotFound {
			t.Errorf("无产物时 SPA 回退应 404，得到 %d", w.Code)
		}
		if w := get("/miniapp/telegram-web-app.js"); w.Code != http.StatusNotFound {
			t.Errorf("无产物时产物根文件也应 404，得到 %d", w.Code)
		}
		return
	}

	w := get("/miniapp")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="root"`) {
		t.Fatalf("/miniapp 应 200 且含 id=\"root\"，得到 %d：%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("入口页应 no-store，得到 %q", got)
	}
	// 入口页必须引用自托管的 SDK；漏掉 script 标签时 Telegram 里会永远
	// 停在引导页（线上踩过一次，这里守住）。
	if !strings.Contains(w.Body.String(), `src="/miniapp/telegram-web-app.js"`) {
		t.Error("入口页应引用自托管的 telegram-web-app.js（/miniapp/telegram-web-app.js）")
	}

	// 真实存在的 assets 文件 200 且长期强缓存；Content-Type 由扩展名给出。
	assets, err := fs.Glob(dist, "assets/*")
	if err != nil || len(assets) == 0 {
		t.Fatalf("产物里应有 assets/* 文件：%v %v", assets, err)
	}
	w = get("/miniapp/" + assets[0])
	if w.Code != http.StatusOK || w.Body.Len() == 0 {
		t.Fatalf("GET /miniapp/%s 应 200 且有内容，得到 %d", assets[0], w.Code)
	}
	if got := w.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("assets 应 immutable 缓存，得到 %q", got)
	}
	if got := w.Header().Get("Content-Type"); got == "" {
		t.Error("assets 应带 Content-Type")
	}
	// 自托管 SDK：产物根下的普通文件要能直接取到，且不能被 SPA 回退成 HTML。
	w = get("/miniapp/telegram-web-app.js")
	if w.Code != http.StatusOK || w.Body.Len() == 0 {
		t.Fatalf("GET /miniapp/telegram-web-app.js 应 200 且有内容，得到 %d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
		t.Errorf("SDK 应返回 JS Content-Type，得到 %q", got)
	}
	if got := w.Header().Get("Cache-Control"); !strings.Contains(got, "max-age=3600") {
		t.Errorf("产物根文件应短缓存，得到 %q", got)
	}

	// 目录与缺失一律 404，禁止目录列举。
	if w := get("/miniapp/assets/"); w.Code != http.StatusNotFound {
		t.Errorf("目录 /miniapp/assets/ 应 404，得到 %d", w.Code)
	}
	if w := get("/miniapp/assets/nope.js"); w.Code != http.StatusNotFound {
		t.Errorf("缺失资源应 404，得到 %d", w.Code)
	}

	// 未匹配的前端路由回退 index（刷新/外链不白屏）。
	w = get("/miniapp/xxx")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="root"`) {
		t.Errorf("SPA 回退应 200 且含 id=\"root\"，得到 %d", w.Code)
	}
}

// TestMiniAppRouteBoundaries：路由规范化、HEAD 与 405 Allow 的边界。
// 资产相关子用例只在有产物时跑（untagged 或未构建时没有真实资产可指），
// 页面/方法边界在两条路径下都跑。
func TestMiniAppRouteBoundaries(t *testing.T) {
	env := newMiniEnv(t)
	do := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		w := httptest.NewRecorder()
		env.h.ServeHTTP(w, req)
		return w
	}

	// 页面路由只收 GET/HEAD，405 要带 Allow。
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/miniapp"},
		{http.MethodDelete, "/miniapp/xxx"},
	} {
		w := do(tc.method, tc.path)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s 应 405，得到 %d", tc.method, tc.path, w.Code)
		}
		if allow := w.Header().Get("Allow"); !strings.Contains(allow, "GET") ||
			!strings.Contains(allow, "HEAD") {
			t.Errorf("%s %s 的 Allow 应含 GET/HEAD，得到 %q", tc.method, tc.path, allow)
		}
	}

	// API 只收 POST：GET 405 且 Allow 只有 POST。
	if w := do(http.MethodGet, "/miniapp/api"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /miniapp/api 应 405，得到 %d", w.Code)
	} else if allow := w.Header().Get("Allow"); allow != http.MethodPost {
		t.Errorf("GET /miniapp/api 的 Allow 应为 POST，得到 %q", allow)
	}
	// 空 op 的 POST：鉴权通过后仍是未知操作 404，不能落入 SPA 回退。
	if w := miniDo(t, env.h, testutil.TestToken, env.adminInit(),
		testutil.TestBotID, "", nil); w.Code != http.StatusNotFound {
		t.Errorf("POST /miniapp/api/ 应 404，得到 %d：%s", w.Code, w.Body.String())
	}

	// HEAD 页面：只写头不写 body（入口页有无产物都成立）。
	if w := do(http.MethodHead, "/miniapp"); w.Body.Len() != 0 {
		t.Errorf("HEAD /miniapp 不应有 body，得到 %d 字节", w.Body.Len())
	}

	dist, ok := miniAppDistFS()
	if !ok {
		// 无产物：没有真实资产可指，页面/方法边界已覆盖。
		return
	}
	assets, err := fs.Glob(dist, "assets/*")
	if err != nil || len(assets) == 0 {
		t.Logf("产物里没有 assets/*（%v %v），跳过资产子用例", assets, err)
		return
	}
	asset := assets[0]

	// .. 与编码的 .. 都不能逃出 assets（含 .. 的路径一律 404）。
	for _, bad := range []string{
		"/miniapp/assets/../index.html",
		"/miniapp/assets/%2e%2e%2findex.html",
	} {
		if w := do(http.MethodGet, bad); w.Code != http.StatusNotFound {
			t.Errorf("GET %s 应 404，得到 %d", bad, w.Code)
		}
	}

	// // 归一：/miniapp//assets/x 就是 /miniapp/assets/x。
	if w := do(http.MethodGet, "/miniapp//"+asset); w.Code != http.StatusOK {
		t.Errorf("GET /miniapp//%s 应 200，得到 %d", asset, w.Code)
	}

	// HEAD 资产：200、body 空、immutable。
	w := do(http.MethodHead, "/miniapp/"+asset)
	if w.Code != http.StatusOK {
		t.Errorf("HEAD /miniapp/%s 应 200，得到 %d", asset, w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("HEAD /miniapp/%s 不应有 body，得到 %d 字节", asset, w.Body.Len())
	}
	if got := w.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("HEAD 资产应 immutable，得到 %q", got)
	}

	// 资产只收 GET/HEAD。
	w = do(http.MethodPost, "/miniapp/assets/x")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /miniapp/assets/x 应 405，得到 %d", w.Code)
	}
	if allow := w.Header().Get("Allow"); !strings.Contains(allow, "GET") ||
		!strings.Contains(allow, "HEAD") {
		t.Errorf("资产 405 的 Allow 应含 GET/HEAD，得到 %q", allow)
	}

	// HEAD SPA 回退：只写头不写 body。
	w = do(http.MethodHead, "/miniapp/xxx")
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Errorf("HEAD SPA 回退应 200 且无 body，得到 %d/%d 字节", w.Code, w.Body.Len())
	}
}

// TestMiniAppUpstreamTest：上游连通性测试（upstream action=test）的三条路径
// 与权限 —— 无可用模型 400、请求失败 200 + ok:false、成功 200 + latency/model；
// 次级管理员与非管理员沿用 miniUpstream 的主管理员限制，一律 403。
func TestMiniAppUpstreamTest(t *testing.T) {
	env := newMiniEnv(t)
	sh := env.sh

	testutil.RegisterTestBot(t, sh, testToken2, 43, 888)
	if err := sh.AddAdmin(888, "次管", 777); err != nil {
		t.Fatal(err)
	}
	sub := signInitData(t, testutil.TestToken, map[string]string{
		"auth_date": strconv.FormatInt(env.now, 10), "user": `{"id":888}`})
	if w := miniDo(t, env.h, testutil.TestToken, sub, testutil.TestBotID, "upstream",
		map[string]any{"action": "test", "id": 1}); w.Code != http.StatusForbidden {
		t.Errorf("次级管理员测试上游应 403，得到 %d：%s", w.Code, w.Body.String())
	}
	outsider := signInitData(t, testutil.TestToken, map[string]string{
		"auth_date": strconv.FormatInt(env.now, 10), "user": `{"id":12345}`})
	if w := miniDo(t, env.h, testutil.TestToken, outsider, testutil.TestBotID, "upstream",
		map[string]any{"action": "test", "id": 1}); w.Code != http.StatusForbidden {
		t.Errorf("非管理员测试上游应 403，得到 %d：%s", w.Code, w.Body.String())
	}

	// 假上游：mode 0 返回最小 chat completion，1 返回 500。收到的请求
	// （路径/鉴权头/请求体）投进带缓冲 channel，测试端断言不会卡住 handler；
	// hits 单独计数，用于断言单次请求（连通测试没有重试）。
	var mode atomic.Int32
	var hits atomic.Int32
	type capturedReq struct{ path, auth, body string }
	got := make(chan capturedReq, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		raw, _ := io.ReadAll(r.Body)
		got <- capturedReq{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: string(raw)}
		if mode.Load() == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"pong"}}]}`)
	}))
	defer srv.Close()

	res, err := sh.Store.Write.Exec(`INSERT INTO upstreams
		(name,base_url,api_key,weight,status,supports_chat,supports_systemone)
		VALUES ('t6up',?,'sk-test',1,1,1,0)`, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	upID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if err := sh.Cache.Reload(); err != nil {
		t.Fatal(err)
	}

	call := func() *httptest.ResponseRecorder {
		t.Helper()
		return miniDo(t, env.h, testutil.TestToken, env.adminInit(),
			testutil.TestBotID, "upstream", map[string]any{"action": "test", "id": upID})
	}
	decode := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("响应不是 JSON：%v（%s）", err, w.Body.String())
		}
		return out
	}

	// 没有登记模型：400 + 明确的中文错误。
	w := call()
	if w.Code != http.StatusBadRequest {
		t.Fatalf("无可用模型应 400，得到 %d：%s", w.Code, w.Body.String())
	}
	if msg, _ := decode(w)["error"].(string); msg != "该上游还没有登记可用模型" {
		t.Errorf("无可用模型的错误文案不对：%q", msg)
	}

	// 只有停用的模型也算「没有可用模型」。
	if _, err := sh.Store.Write.Exec(`INSERT INTO models
		(name,prompt_price,completion_price,cache_read_price,cache_write_price,enabled)
		VALUES ('t6up/m1',0,0,0,0,0)`); err != nil {
		t.Fatal(err)
	}
	if err := sh.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	if w := call(); w.Code != http.StatusBadRequest {
		t.Fatalf("只有停用模型应 400，得到 %d：%s", w.Code, w.Body.String())
	}

	if _, err := sh.Store.Write.Exec(
		`UPDATE models SET enabled=1 WHERE name='t6up/m1'`); err != nil {
		t.Fatal(err)
	}
	if err := sh.Cache.Reload(); err != nil {
		t.Fatal(err)
	}

	// 上游 500：HTTP 200 + ok:false + 可读原因，且请求确实走了
	// 判定链路的路径、鉴权头与模型名（剥掉上游前缀）。
	mode.Store(1)
	w = call()
	if w.Code != http.StatusOK {
		t.Fatalf("上游 500 时应 200 + ok:false，得到 %d：%s", w.Code, w.Body.String())
	}
	out := decode(w)
	if out["ok"] != false {
		t.Errorf("上游 500 时 ok 应为 false，得到 %v", out["ok"])
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "500") {
		t.Errorf("错误文案应含 HTTP 状态，得到 %q", msg)
	}
	req := <-got
	if req.path != "/v1/chat/completions" {
		t.Errorf("请求路径应是 /v1/chat/completions，得到 %q", req.path)
	}
	if req.auth != "Bearer sk-test" {
		t.Errorf("鉴权头应带上游 api_key，得到 %q", req.auth)
	}
	if !strings.Contains(req.body, `"model":"m1"`) {
		t.Errorf("请求体应带剥掉前缀的模型 ID，得到 %s", req.body)
	}
	// 失败路径也必须停在 aiAttempt：走 aiCall 会累计失败计数并可能触发
	// 上游告警，连通性测试不该污染判定链路的健康度。
	if n := sh.AIFailStreak.Load(); n != 0 {
		t.Errorf("连通测试不应计入判定链路的失败计数，AIFailStreak=%d", n)
	}

	// 成功：200 + ok:true + latency_ms + 「上游名/模型ID」文案，且只发一次请求。
	hits.Store(0)
	mode.Store(0)
	w = call()
	if w.Code != http.StatusOK {
		t.Fatalf("成功时应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	out = decode(w)
	if out["ok"] != true {
		t.Errorf("成功时 ok 应为 true，得到 %v（错误：%v）", out["ok"], out["error"])
	}
	if out["model"] != "t6up/m1" {
		t.Errorf("model 应是上游名/模型ID，得到 %v", out["model"])
	}
	if _, ok := out["latency_ms"].(float64); !ok {
		t.Errorf("latency_ms 应是数字，得到 %T", out["latency_ms"])
	}
	req = <-got
	if req.path != "/v1/chat/completions" || req.auth != "Bearer sk-test" {
		t.Errorf("成功路径的请求不对：%+v", req)
	}
	if !strings.Contains(req.body, `"messages"`) {
		t.Errorf("请求体应带 messages，得到 %s", req.body)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("成功用例假上游应只收到 1 次请求（无重试），得到 %d", n)
	}
}

// TestMiniUpstreamKinds：渠道类型走 MiniApp 的增改两条路径 —— 未知类型
// 拒绝；chat-only 类型强制只开 chat；Cloudflare 默认主判定；state 下发 kind。
func TestMiniUpstreamKinds(t *testing.T) {
	env := newMiniEnv(t)
	sh := env.sh
	call := func(body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		return miniDo(t, env.h, testutil.TestToken, env.adminInit(),
			testutil.TestBotID, "upstream", body)
	}

	// 未知类型直接拒绝，不能把协议层不认识的 kind 写进库。
	if w := call(map[string]any{"action": "add", "name": "bad", "base_url": "http://x",
		"api_key": "k", "kind": "cohere"}); w.Code != http.StatusBadRequest {
		t.Errorf("未知渠道类型应 400，得到 %d：%s", w.Code, w.Body.String())
	}

	// Gemini 是 chat-only：客户端传 supports_systemone=true 也不作数。
	if w := call(map[string]any{"action": "add", "name": "gem",
		"base_url": "https://generativelanguage.googleapis.com/v1beta", "api_key": "gk",
		"kind": "gemini", "supports_systemone": true}); w.Code != http.StatusOK {
		t.Fatalf("新增 Gemini 上游应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	var kind string
	var chat, so int64
	if err := sh.Store.Read.QueryRow(`SELECT kind,supports_chat,supports_systemone
		FROM upstreams WHERE name='gem'`).Scan(&kind, &chat, &so); err != nil {
		t.Fatalf("Gemini 上游没落库: %v", err)
	}
	if kind != "gemini" || chat != 1 || so != 0 {
		t.Errorf("Gemini 能力应强制 1/0，得到 kind=%q chat=%d so=%d", kind, chat, so)
	}

	// Cloudflare 不传能力开关：按类型默认（主判定）。
	if w := call(map[string]any{"action": "add", "name": "cfx",
		"base_url": "https://api.cloudflare.com/client/v4/accounts/acc",
		"api_key":  "cf", "kind": "cloudflare"}); w.Code != http.StatusOK {
		t.Fatalf("新增 Cloudflare 上游应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	if err := sh.Store.Read.QueryRow(`SELECT kind,supports_chat,supports_systemone
		FROM upstreams WHERE name='cfx'`).Scan(&kind, &chat, &so); err != nil {
		t.Fatalf("Cloudflare 上游没落库: %v", err)
	}
	if kind != "cloudflare" || chat != 0 || so != 1 {
		t.Errorf("Cloudflare 默认能力应 0/1，得到 kind=%q chat=%d so=%d", kind, chat, so)
	}

	// 改类型：Gemini 切到 Cloudflare 时能力按类型重置为 0/1。
	var id int64
	if err := sh.Store.Read.QueryRow(
		`SELECT id FROM upstreams WHERE name='gem'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if w := call(map[string]any{"action": "update", "id": id,
		"kind": "cloudflare"}); w.Code != http.StatusOK {
		t.Fatalf("切换类型应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	if err := sh.Store.Read.QueryRow(`SELECT kind,supports_chat,supports_systemone
		FROM upstreams WHERE id=?`, id).Scan(&kind, &chat, &so); err != nil {
		t.Fatal(err)
	}
	if kind != "cloudflare" || chat != 0 || so != 1 {
		t.Errorf("切换后能力应重置 0/1，得到 kind=%q chat=%d so=%d", kind, chat, so)
	}

	// 只改 base_url（kind 原样回传）时能力开关必须保留：前端每次保存都会
	// 回传当前 kind，服务端不能因此把它重置成默认值。
	var cfxID int64
	if err := sh.Store.Read.QueryRow(
		`SELECT id FROM upstreams WHERE name='cfx'`).Scan(&cfxID); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.Store.Write.Exec(
		`UPDATE upstreams SET supports_chat=1 WHERE id=?`, cfxID); err != nil {
		t.Fatal(err)
	}
	if w := call(map[string]any{"action": "update", "id": cfxID, "kind": "cloudflare",
		"base_url": "https://api.cloudflare.com/client/v4/accounts/acc2"}); w.Code != http.StatusOK {
		t.Fatalf("更新 base_url 应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	if err := sh.Store.Read.QueryRow(`SELECT kind,supports_chat,supports_systemone
		FROM upstreams WHERE id=?`, cfxID).Scan(&kind, &chat, &so); err != nil {
		t.Fatal(err)
	}
	if kind != "cloudflare" || chat != 1 || so != 1 {
		t.Errorf("原样回传 kind 不该重置能力，得到 kind=%q chat=%d so=%d", kind, chat, so)
	}

	// state 下发 kind，前端据此渲染类型与能力开关。
	w := miniDo(t, env.h, testutil.TestToken, env.adminInit(), testutil.TestBotID, "state", nil)
	var st struct {
		Upstreams []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"upstreams"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("state 不是 JSON: %v", err)
	}
	kinds := map[string]string{}
	for _, u := range st.Upstreams {
		kinds[u.Name] = u.Kind
	}
	if kinds["cfx"] != "cloudflare" || kinds["gem"] != "cloudflare" {
		t.Errorf("state 下发的 kind 不对: %v", kinds)
	}
}

// TestMiniAppUpstreamTestCloudflare：CF 渠道的连通测试走 /ai/run？——
// 未开 chat 时测主判定（Clef），请求体 model 只带最后一段。
func TestMiniAppUpstreamTestCloudflare(t *testing.T) {
	env := newMiniEnv(t)
	sh := env.sh

	type capturedReq struct{ path, auth, body string }
	got := make(chan capturedReq, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got <- capturedReq{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: string(raw)}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"result":{"model":"clef","answers":{"ok":{"type":"noul","noul":0.99}},`+
			`"usage":{"input_tokens":5,"output_tokens":1}},"success":true,"errors":[],"messages":[]}`)
	}))
	defer srv.Close()

	res, err := sh.Store.Write.Exec(`INSERT INTO upstreams
		(name,base_url,api_key,weight,status,supports_chat,supports_systemone,kind)
		VALUES ('cfu',?,'cf-key',1,1,0,1,'cloudflare')`,
		srv.URL+"/client/v4/accounts/acc123")
	if err != nil {
		t.Fatal(err)
	}
	upID, _ := res.LastInsertId()
	if _, err := sh.Store.Write.Exec(`INSERT INTO models
		(name,prompt_price,completion_price,cache_read_price,cache_write_price,enabled)
		VALUES ('cfu/@cf/cloudflare/clef',0.24,0,0,0,1)`); err != nil {
		t.Fatal(err)
	}
	if err := sh.Cache.Reload(); err != nil {
		t.Fatal(err)
	}

	w := miniDo(t, env.h, testutil.TestToken, env.adminInit(), testutil.TestBotID,
		"upstream", map[string]any{"action": "test", "id": upID})
	if w.Code != http.StatusOK {
		t.Fatalf("CF 连通测试应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if out["ok"] != true || out["model"] != "cfu/@cf/cloudflare/clef" {
		t.Errorf("连通测试结果不对: %v（错误：%v）", out, out["error"])
	}
	req := <-got
	if req.path != "/client/v4/accounts/acc123/ai/run/@cf/cloudflare/clef" {
		t.Errorf("请求路径 = %q", req.path)
	}
	if req.auth != "Bearer cf-key" {
		t.Errorf("鉴权头 = %q", req.auth)
	}
	if !strings.Contains(req.body, `"model":"clef"`) {
		t.Errorf("请求体 model 应只带最后一段: %s", req.body)
	}
}

// TestMiniRulesCRUD：AI 必封规则只有主管理员能维护；保存自动跑全库测试；
// 从未测过与检出误封（last_fp>0）的规则都不允许开强制，重跑测试干净后
// 才允许；改 pattern 会把 enforce 归零；列表字段齐全；写操作后快照
// 立即生效；停用同时清 enforce；匹配空文本的 pattern 写入口拒绝。
func TestMiniRulesCRUD(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	testutil.RegisterTestBot(t, sh, testToken2, 43, 888)
	if err := sh.AddAdmin(888, "次管", 777); err != nil {
		t.Fatal(err)
	}
	env := &miniTestEnv{t: t, h: MiniAppHandler(sh), now: time.Now().Unix()}
	sub := signInitData(t, testToken2, map[string]string{
		"auth_date": strconv.FormatInt(env.now, 10), "user": `{"id":888}`})

	// 次管：403。
	w := miniDo(t, env.h, testToken2, sub, 43, "rules",
		map[string]any{"action": "list"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("次管访问必封规则应 403，得到 %d：%s", w.Code, w.Body.String())
	}

	mainDo := func(body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		return miniDo(t, env.h, testutil.TestToken, env.adminInit(),
			testutil.TestBotID, "rules", body)
	}
	decode := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("响应不是 JSON：%v（%s）", err, w.Body.String())
		}
		return out
	}
	snapRule := func(pattern string) (store.AdRuleRec, bool) {
		t.Helper()
		for _, r := range sh.Cache.Snap().AdRules {
			if r.Pattern == pattern {
				return r, true
			}
		}
		return store.AdRuleRec{}, false
	}

	// 造一条命中该正则的「正常」流水：保存后的自动测试必须把它算成误封。
	if _, err := sh.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,created_at,bot_id)
		VALUES (-100,555,1,'minirule测试词正常聊天','clean',0.9,'so','none','none','',?,?)`,
		env.now, testutil.TestBotID); err != nil {
		t.Fatal(err)
	}

	// 保存：自动测试应报 1 条误封。
	w = mainDo(map[string]any{"action": "save", "name": "Mini 规则",
		"pattern": "minirule测试词", "category": "promo", "note": "说明"})
	if w.Code != http.StatusOK {
		t.Fatalf("主管理员保存应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	saved := decode(w)
	id := int64(saved["id"].(float64))
	if id == 0 || saved["ok"] != true {
		t.Fatalf("保存响应不对：%v", saved)
	}
	test, _ := saved["test"].(map[string]any)
	if test == nil || test["fp"].(float64) != 1 || test["tp"].(float64) != 0 {
		t.Fatalf("保存自动测试结果不对：%v", test)
	}

	// 重复正则与非法正则都要拒绝。
	w = mainDo(map[string]any{"action": "save", "name": "重复",
		"pattern": "minirule测试词"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("重复正则应 400，得到 %d：%s", w.Code, w.Body.String())
	}
	w = mainDo(map[string]any{"action": "save", "name": "坏正则", "pattern": "["})
	if w.Code != http.StatusBadRequest {
		t.Errorf("非法正则应 400，得到 %d：%s", w.Code, w.Body.String())
	}
	// 能匹配空文本的 pattern（`a*`）编译得过，但会命中所有消息，写入口拒绝。
	w = mainDo(map[string]any{"action": "save", "name": "空文本", "pattern": "a*"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("匹配空文本的规则应 400，得到 %d：%s", w.Code, w.Body.String())
	} else if msg, _ := decode(w)["error"].(string); !strings.Contains(msg, "空文本") {
		t.Errorf("错误文案应说明空文本：%q", msg)
	}

	// 启用后快照立即生效。
	if w = mainDo(map[string]any{"action": "toggle", "id": id,
		"enabled": true}); w.Code != http.StatusOK {
		t.Fatalf("启用应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	if r, ok := snapRule("minirule测试词"); !ok || !r.Enabled || r.Enforce {
		t.Errorf("启用后快照应立即变为 enabled 且未 enforce：%+v ok=%v", r, ok)
	}

	// 从未测过的规则不允许开强制：把测试时间戳清零模拟 AI 直写的候选。
	if _, err := sh.Store.Write.Exec(
		`UPDATE ad_rules SET last_tested_at=0 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	w = mainDo(map[string]any{"action": "enforce", "id": id, "enforce": true})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("未测过的规则开强制应 400，得到 %d：%s", w.Code, w.Body.String())
	}
	if msg, _ := decode(w)["error"].(string); !strings.Contains(msg, "全库测试") {
		t.Errorf("未测过的错误文案应提到全库测试：%q", msg)
	}
	// 重跑一次测试（fp 仍为 1），恢复「测过」状态。
	if w = mainDo(map[string]any{"action": "test", "id": id}); w.Code != http.StatusOK {
		t.Fatalf("重跑测试应 200，得到 %d：%s", w.Code, w.Body.String())
	}

	// 有误封时不允许开强制。
	w = mainDo(map[string]any{"action": "enforce", "id": id, "enforce": true})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("检出误封时开强制应 400，得到 %d：%s", w.Code, w.Body.String())
	}
	if msg, _ := decode(w)["error"].(string); !strings.Contains(msg, "疑似误封") {
		t.Errorf("错误文案应说明误封：%q", msg)
	}

	// 把那条误封改成已确认广告，重跑测试后 last_fp 归零。
	if _, err := sh.Store.Write.Exec(`UPDATE antiad_log
		SET verdict='ad', action='deleted'
		WHERE text='minirule测试词正常聊天'`); err != nil {
		t.Fatal(err)
	}
	w = mainDo(map[string]any{"action": "test", "id": id})
	if w.Code != http.StatusOK {
		t.Fatalf("重跑测试应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	test, _ = decode(w)["test"].(map[string]any)
	if test["fp"].(float64) != 0 || test["tp"].(float64) != 1 {
		t.Fatalf("重跑测试结果不对：%v", test)
	}
	// 覆盖率契约：这一轮只有 1 条已确认广告且被命中 → 分母 1、覆盖率 1。
	if test["ads_total"].(float64) != 1 || test["coverage"].(float64) != 1 {
		t.Fatalf("测试响应应带覆盖率：%v", test)
	}
	kinds, _ := test["kinds"].([]any)
	if len(kinds) != 1 {
		t.Fatalf("测试响应应带按类型细分：%v", test["kinds"])
	}
	// 这条流水的 ad_kind 是 'none'（见测试里 INSERT 的列值），不是 promo。
	if kind, _ := kinds[0].(map[string]any)["kind"].(string); kind != "none" {
		t.Fatalf("按类型细分应为 none：%v", kinds[0])
	}
	// 写回落库：列表与详情不用重测就能显示覆盖率。
	var lastAdsTotal int64
	var lastKinds string
	if err := sh.Store.Read.QueryRow(
		`SELECT last_ads_total,last_kinds FROM ad_rules WHERE id=?`,
		id).Scan(&lastAdsTotal, &lastKinds); err != nil {
		t.Fatal(err)
	}
	if lastAdsTotal != 1 || !strings.Contains(lastKinds, `"kind":"none"`) {
		t.Errorf("覆盖率未写回：ads=%d kinds=%s", lastAdsTotal, lastKinds)
	}

	// 测试干净后允许开强制。
	if w = mainDo(map[string]any{"action": "enforce", "id": id,
		"enforce": true}); w.Code != http.StatusOK {
		t.Fatalf("测试干净后开强制应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	var en, enf, hits, scanned int64
	if err := sh.Store.Read.QueryRow(`SELECT enabled,enforce,hits,last_scanned
		FROM ad_rules WHERE id=?`, id).Scan(&en, &enf, &hits, &scanned); err != nil {
		t.Fatal(err)
	}
	if en != 1 || enf != 1 || scanned != 1 {
		t.Errorf("落库状态不对：enabled=%d enforce=%d scanned=%d",
			en, enf, scanned)
	}
	if r, ok := snapRule("minirule测试词"); !ok || !r.Enabled || !r.Enforce {
		t.Errorf("Reload 后快照应为启用+强制：%+v ok=%v", r, ok)
	}

	// 列表字段齐全。
	w = mainDo(map[string]any{"action": "list"})
	if w.Code != http.StatusOK {
		t.Fatalf("列表应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	rules, _ := decode(w)["rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("应有 1 条规则，得到 %d", len(rules))
	}
	row, _ := rules[0].(map[string]any)
	for _, k := range []string{"id", "name", "pattern", "category", "note",
		"source", "enabled", "enforce", "hits", "last_matched", "last_tp",
		"last_fp", "last_undone", "last_scanned", "last_tested_at",
		"last_ads_total", "last_kinds", "created_at"} {
		if _, ok := row[k]; !ok {
			t.Errorf("列表缺少字段 %s：%v", k, row)
		}
	}
	if row["last_ads_total"].(float64) != 1 {
		t.Errorf("列表应带覆盖率分母：%v", row["last_ads_total"])
	}
	if row["source"] != "ai" || row["enabled"] != true || row["enforce"] != true {
		t.Errorf("列表内容不对：%v", row)
	}

	// 改 pattern 必须把 enforce 归零：新 pattern 的全库测试还没跑，
	// 旧 pattern 的 fp=0 不能沿用。先造一条会被新 pattern 命中的正常流水。
	if _, err := sh.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,created_at,bot_id)
		VALUES (-100,556,2,'mini改版测试词正常','clean',0.9,'so','none','none','',?,?)`,
		env.now, testutil.TestBotID); err != nil {
		t.Fatal(err)
	}
	w = mainDo(map[string]any{"action": "save", "id": id, "name": "Mini 规则",
		"pattern": "mini改版测试词", "category": "promo", "note": "说明"})
	if w.Code != http.StatusOK {
		t.Fatalf("改 pattern 保存应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	test, _ = decode(w)["test"].(map[string]any)
	if test["fp"].(float64) != 1 {
		t.Fatalf("新 pattern 应测出 1 条误封：%v", test)
	}
	if err := sh.Store.Read.QueryRow(
		`SELECT enforce FROM ad_rules WHERE id=?`, id).Scan(&enf); err != nil {
		t.Fatal(err)
	}
	if enf != 0 {
		t.Errorf("改 pattern 后 enforce 应归零，得到 %d", enf)
	}
	if r, ok := snapRule("mini改版测试词"); !ok || r.Enforce {
		t.Errorf("Reload 后快照里 enforce 应为 0：%+v ok=%v", r, ok)
	}

	// 停用同时清掉 enforce。
	if w = mainDo(map[string]any{"action": "toggle", "id": id,
		"enabled": false}); w.Code != http.StatusOK {
		t.Fatalf("停用应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	if err := sh.Store.Read.QueryRow(`SELECT enabled,enforce FROM ad_rules
		WHERE id=?`, id).Scan(&en, &enf); err != nil {
		t.Fatal(err)
	}
	if en != 0 || enf != 0 {
		t.Errorf("停用应同时清掉 enforce，得到 enabled=%d enforce=%d", en, enf)
	}
	if r, ok := snapRule("mini改版测试词"); !ok || r.Enabled || r.Enforce {
		t.Errorf("停用后快照应为未启用未强制：%+v ok=%v", r, ok)
	}

	// 删除。
	if w = mainDo(map[string]any{"action": "remove", "id": id}); w.Code != http.StatusOK {
		t.Fatalf("删除应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	var n int
	if err := sh.Store.Read.QueryRow(
		`SELECT COUNT(*) FROM ad_rules WHERE id=?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("规则没有被删除")
	}
	if _, ok := snapRule("mini改版测试词"); ok {
		t.Error("删除后快照里不应还有这条规则")
	}
}
