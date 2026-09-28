package panel

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

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
}
