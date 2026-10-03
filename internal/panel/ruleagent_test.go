package panel

// 规则发现 Agent 的面板接口测试：三个 agent_* 动作都只有主管理员能用，
// agent_status 的返回是 T-C 前端依赖的冻结契约。

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

// TestMiniRulesAgentOps：权限（次管 403）、空态结构、无兼容上游的 400，
// 以及配好假上游后能启动并跑完一轮。
func TestMiniRulesAgentOps(t *testing.T) {
	env := newMiniEnv(t)
	sh := env.sh

	// 次管访问三个动作都应 403（miniRules 开头的主管理员闸门）。
	testutil.RegisterTestBot(t, sh, testToken2, 43, 888)
	if err := sh.AddAdmin(888, "次管", 777); err != nil {
		t.Fatal(err)
	}
	sub43 := signInitData(t, testToken2, map[string]string{
		"auth_date": strconv.FormatInt(env.now, 10), "user": `{"id":888}`})
	for _, action := range []string{"agent_start", "agent_status", "agent_stop"} {
		w := miniDo(t, env.h, testToken2, sub43, 43, "rules",
			map[string]any{"action": action})
		if w.Code != http.StatusForbidden {
			t.Errorf("次管调用 %s 应 403，得到 %d：%s", action, w.Code, w.Body.String())
		}
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
	agentOf := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("应 200，得到 %d：%s", w.Code, w.Body.String())
		}
		agent, _ := decode(w)["agent"].(map[string]any)
		if agent == nil {
			t.Fatalf("响应缺少 agent 对象：%s", w.Body.String())
		}
		return agent
	}

	// 空态：契约字段齐全、steps_count 是数字、running=false。
	agent := agentOf(mainDo(map[string]any{"action": "agent_status"}))
	for _, k := range []string{"running", "started_at", "finished_at", "result",
		"error", "created_rule_id", "created_rule_ids", "steps_count",
		"target_log_id"} {
		if _, ok := agent[k]; !ok {
			t.Errorf("agent 状态缺少字段 %s：%v", k, agent)
		}
	}
	if agent["running"] != false {
		t.Errorf("空态 running 应为 false：%v", agent)
	}
	if _, ok := agent["steps_count"].(float64); !ok {
		t.Errorf("steps_count 应是数字：%v", agent["steps_count"])
	}

	// 没在跑时 stop 是空操作。
	out := decode(mainDo(map[string]any{"action": "agent_stop"}))
	if out["ok"] != true || out["stopped"] != false {
		t.Errorf("空态 stop 应返回 ok+stopped=false：%v", out)
	}

	// 没有可用的 OpenAI 兼容上游：start 返回 400 中文错误。
	w := mainDo(map[string]any{"action": "agent_start"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("无兼容上游时 start 应 400，得到 %d：%s", w.Code, w.Body.String())
	}
	if msg, _ := decode(w)["error"].(string); !strings.Contains(msg, "OpenAI 兼容") {
		t.Errorf("错误文案应说明需要 OpenAI 兼容渠道：%q", msg)
	}

	// 配一个立即回文本的 OpenAI 兼容假上游：agent 一轮即收尾。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant",` +
			`"content":"没有发现值得创建的新形态。"}}]}`))
	}))
	t.Cleanup(srv.Close)
	if _, err := sh.Store.Write.Exec(`INSERT INTO upstreams
		(name,base_url,api_key,weight,status,supports_chat,supports_systemone)
		VALUES ('pai',?,'k',1,1,1,0)`, srv.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.Store.Write.Exec(`INSERT INTO models
		(name,prompt_price,completion_price,cache_read_price,cache_write_price,enabled)
		VALUES ('pai/llm',0,0,0,0,1)`); err != nil {
		t.Fatal(err)
	}
	if err := sh.PutSetting("antiad_llm_model", "pai/llm"); err != nil {
		t.Fatal(err)
	}

	// 指定记录模式：不存在的记录 id 在启动前被拒绝（证明 record_id 已接线）。
	if w := mainDo(map[string]any{"action": "agent_start", "record_id": 999999}); w.Code != http.StatusBadRequest {
		t.Fatalf("不存在的记录应 400，得到 %d：%s", w.Code, w.Body.String())
	} else if msg, _ := decode(w)["error"].(string); !strings.Contains(msg, "没有 id=999999") {
		t.Errorf("错误文案应说明记录不存在：%q", msg)
	}

	if w := mainDo(map[string]any{"action": "agent_start"}); w.Code != http.StatusOK {
		t.Fatalf("配好上游后 start 应 200，得到 %d：%s", w.Code, w.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		agent = agentOf(mainDo(map[string]any{"action": "agent_status"}))
		if agent["running"] == false {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent 10 秒内没有跑完：%v", agent)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if agent["result"] == "" {
		t.Errorf("跑完后 result 不应为空：%v", agent)
	}
	if agent["error"] != "" {
		t.Errorf("假上游正常应答不应报错：%v", agent["error"])
	}
	if got, _ := agent["steps_count"].(float64); got <= 0 {
		t.Errorf("跑完后 steps_count 应大于 0：%v", agent["steps_count"])
	}
}
