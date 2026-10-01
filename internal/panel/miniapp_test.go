package panel

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"menshen/internal/antiad"
	"menshen/internal/core"
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
	now int64
}

func newMiniEnv(t *testing.T) *miniTestEnv {
	t.Helper()
	_, b := testutil.NewTestRegistry(t, nil)
	return &miniTestEnv{t: t, h: MiniAppHandler(b.Shared), now: time.Now().Unix()}
}

func (e *miniTestEnv) adminInit() string {
	return signInitData(e.t, testutil.TestToken, map[string]string{
		"auth_date": strconv.FormatInt(e.now, 10), "user": `{"id":777}`})
}

// TestValidateMiniInitData 守的是 Mini App 的鉴权：签名不对、过期、
// 没有用户都不放行。
func TestValidateMiniInitData(t *testing.T) {
	const token = testutil.TestToken
	now := time.Now().Unix()
	good := signInitData(t, token, map[string]string{
		"auth_date": strconv.FormatInt(now, 10),
		"user":      `{"id":777,"first_name":"admin"}`,
	})
	if uid, err := validateMiniInitData(token, good); err != nil || uid != 777 {
		t.Fatalf("合法 initData 应通过，uid=%d err=%v", uid, err)
	}

	other := signInitData(t, "999:othertoken", map[string]string{
		"auth_date": strconv.FormatInt(now, 10), "user": `{"id":777}`})
	if _, err := validateMiniInitData(token, other); err == nil {
		t.Error("别的 bot 的签名不该通过")
	}

	stale := signInitData(t, token, map[string]string{
		"auth_date": strconv.FormatInt(now-25*3600, 10), "user": `{"id":777}`})
	if _, err := validateMiniInitData(token, stale); err == nil {
		t.Error("超过 24 小时的 initData 不该通过")
	}

	if _, err := validateMiniInitData(token, good+"&extra=1"); err == nil {
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

// TestMiniAppGlobalTogglesValidJS：三个全局开关的 onclick 必须是合法调用。
// 曾经生成 act('set',{...value:1,'已切换')——对象字面量缺右括号，且 value
// 传的是数字（服务端只认字符串），三个开关在 App 里完全点不动。
func TestMiniAppGlobalTogglesValidJS(t *testing.T) {
	for _, key := range []string{"antiad_enabled", "alert_copy_main", "gban_enabled"} {
		open := "key:\\'" + key + "\\',value:\\'"
		if !strings.Contains(miniAppHTML, open) {
			t.Errorf("%s 的按钮应把 value 拼成带引号的字符串，缺 %q", key, open)
		}
		if !strings.Contains(miniAppHTML, "\\'},\\'已切换\\')") {
			t.Error("开关按钮应在 okMsg 之前闭合对象字面量（value:'…'},'已切换'）")
		}
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
}

// TestMiniAppChatDetailShowsEffectivePunish：群详情要显示实际会执行的处罚
// 与时长——只写「禁言」时，很容易以为永久封禁已经生效。
func TestMiniAppChatDetailShowsEffectivePunish(t *testing.T) {
	for _, want := range []string{
		"实际执行",
		"跟随 bot 设置",
		"muteOptLabel(c.bot_id)",
		"永久禁言",
		"要改成永久禁言",
	} {
		if !strings.Contains(miniAppHTML, want) {
			t.Errorf("群详情缺少 %q", want)
		}
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

// TestMiniAppListSearch：名单类列表要能搜。名单长起来之后只能靠肉眼翻，
// 想确认「某个人在不在名单里」得从头滑到尾。搜索只重绘列表容器，
// 不动输入框本身（整体 render() 会丢焦点与输入法组合）。
func TestMiniAppListSearch(t *testing.T) {
	for _, want := range []string{
		"function filterRows(",
		// 全局组、专属组、白名单三处都要有搜索框与列表容器。
		`id="gb_q"`, `id="gb_list"`, `id="gb_count"`,
		`id="ow_q"`, `id="ow_list"`, `id="ow_count"`,
		`id="wl_q"`, `id="wl_list"`, `id="wl_count"`,
		// 搜索走 oninput（即时过滤），且不整页重绘。
		`oninput="filterRows(`,
	} {
		if !strings.Contains(miniAppHTML, want) {
			t.Errorf("Mini App 缺少 %q", want)
		}
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

// TestMiniAppUserViewWired：用户页要有入口（记录列表与详情里的 uid 可点）与
// 默认只看处置记录的过滤变量。
func TestMiniAppUserViewWired(t *testing.T) {
	for _, want := range []string{
		"function viewUser(", "loadUserLogs(", "USERF = 'act'",
		"'user:'", "api('user',", "只看被处置过的", "全部判定记录",
	} {
		if !strings.Contains(miniAppHTML, want) {
			t.Errorf("Mini App 缺少 %q", want)
		}
	}
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
