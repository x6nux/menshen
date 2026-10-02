package antiad

// 本文件实现「AI 必封规则发现 Agent」：用 CloudWeGo Eino 的 ReAct Agent
// 驱动一轮工具循环，从 antiad_log 判定流水里总结一条高精度必封正则，
// 以 enabled=0、enforce=0 的候选写入 ad_rules，等主管理员在 Mini App
// 里复核、测试、启用。
//
// 设计取舍：
//   - 运行态是**进程内存**、单飞互斥（全局规则只有一份，同时跑两轮会
//     竞态写库并重复烧钱），见 ruleAgentRT；
//   - 模型优先用全局设置 antiad_rule_model 显式指定的那个；没指定时从
//     已启用的复判模型列表里挑第一个。无论哪条路径都要求模型已登记且
//     启用、上游已启用且支持 chat、渠道是 OpenAI 兼容：Eino 的 OpenAI
//     ChatModel 只会说 /chat/completions，Anthropic / Gemini /
//     Cloudflare / Responses 的协议它不认，宁可在 start 时用中文错误
//     说清楚，也不要在半路拿 404；
//   - 工具全部只读 DB 或复用 T-A 的 TestRulePattern / CompileRulePattern，
//     create_rule 的写库防线与面板 miniRuleSave 完全同源（严格编译 +
//     全库测试 fp=0/undone=0），AI 不能绕过防误封闸门；
//   - 步骤日志（模型轮次 + 每次工具调用的参数/结果摘要）在运行中实时可见，
//     Mini App 轮询 agent_status 就能看到 Agent 在干什么。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	einoagent "github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	cbutils "github.com/cloudwego/eino/utils/callbacks"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/upstream"
)

// ---- 可调上限 ----
//
// 这两个是 var 而不是 const：测试要在不真等 30 分钟、不真跑满 1000 步的
// 前提下覆盖超时与步数耗尽路径。
var (
	// ruleAgentTimeout 是单轮规则发现的总时限。放宽到 30 分钟：模型要
	// 反复试跑正则，步数上限给了 1000，时间不够会先被这里掐断。
	ruleAgentTimeout = 1800 * time.Second
	// ruleAgentMaxStep 是 ReAct 图的最大执行步数（模型一次 + 工具一次
	// 各算一步，1000 步约等于 500 轮工具循环）。给足试错空间，真正的
	// 兜底是总时限与连续创建失败保护。
	ruleAgentMaxStep = 1000
)

const (
	// ruleAgentMaxCreateFails 是连续创建失败的上限：命中该值立即结束，
	// 避免模型拿着同一个思路把步数耗尽。
	ruleAgentMaxCreateFails = 3
	// ruleAgentSummaryMax 是单条步骤摘要的字符上限。
	ruleAgentSummaryMax = 300
	// ruleAgentSampleTextMax 是工具返回的样本正文截断长度。
	ruleAgentSampleTextMax = 200
	// ruleAgentReadTextMax 是 read_record 返回的单条正文上限。
	ruleAgentReadTextMax = 2000
	// ruleAgentFindScanLimit 与 TestRulePattern 的全库扫描上限一致：
	// find 与 test_rule 的计数口径要对得上，扫描范围就不能两样。
	ruleAgentFindScanLimit = ruleScanLimit
	// ruleAgentEvidenceMax 是写进 note 的证据 id 上限。
	ruleAgentEvidenceMax = 20
	// ruleAgentEvidenceInputMax 是单次校验处理的输入 id 上限：模型偶尔
	// 会塞一长串 id，逐个查库之前先截断，别让一个工具调用打出一串查询。
	ruleAgentEvidenceInputMax = 50
	// ruleAgentListBannedDefault/Max：list_banned 的默认与上限条数。
	ruleAgentListBannedDefault = 50
	ruleAgentListBannedMax     = 200
	// ruleAgentFindDefault/Max：find 的默认与上限样本条数。
	ruleAgentFindDefault = 50
	ruleAgentFindMax     = 200
	// ruleAgentReadRecordsMax 是 read_records 单次最多读的条数。
	ruleAgentReadRecordsMax = 10
	// ruleAgentListKindsRecent 是 list_kinds 每类附带的最近流水 id 数。
	ruleAgentListKindsRecent = 5
	// ruleAgentListRulesMax 是 list_rules 最多返回的规则数（取最近创建的）。
	ruleAgentListRulesMax = 100
)

// ruleAgentToolNames 是暴露给模型的工具名。两处要用：UnknownToolsHandler
// 的可用清单提示，以及测试断言。
var ruleAgentToolNames = []string{
	"list_kinds", "list_banned", "read_record", "read_records",
	"find", "test_rule", "list_rules", "create_rule",
}

// 规则发现不可用的错误。文案直接回给 Mini App，保持中文。
var (
	errRuleAgentRunning = errors.New("规则发现 Agent 已在运行，请等待本轮结束或先停止")
	errRuleAgentNoModel = errors.New("规则发现需要 OpenAI 兼容渠道的上游：" +
		"当前没有已启用、且绑定了 OpenAI 兼容 chat 上游的复判模型")
	errRuleAgentNoCompat = errors.New("规则发现需要 OpenAI 兼容渠道的上游：" +
		"当前复判模型走的是 Anthropic / Gemini / Cloudflare 等渠道，暂不支持")
	errRuleAgentNoModels = errors.New("规则发现需要 OpenAI 兼容渠道的上游：" +
		"还没有配置任何复判模型（antiad_llm_models）")
)

// ---- 运行状态 ----

// RuleAgentStep 是一条步骤日志。Kind 取值 "model"（模型轮次）或
// "tool"（一次工具调用）；Summary 里放参数/结果的摘要（≤300 字符）。
type RuleAgentStep struct {
	N       int    `json:"n"`
	At      int64  `json:"at"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

// ruleAgentRuntime 是一轮规则发现的进程级运行态。
//
// 只允许存在一轮：规则是全局一份，两轮并发会重复调模型、竞态写 ad_rules，
// 面板上的「运行中/已完成」也会互相覆盖。
type ruleAgentRuntime struct {
	mu             sync.Mutex
	running        bool
	startedAt      int64
	finishedAt     int64
	result         string
	errText        string
	createdRuleIDs []int64
	steps          []RuleAgentStep
	seq            int
	// cancel 只在 running 期间非空，StopRuleDiscovery 用它中止本轮。
	cancel context.CancelFunc
}

var ruleAgentRT ruleAgentRuntime

// StartRuleDiscovery 启动一轮规则发现，**立即返回**，实际执行在后台。
//
// 单飞：已在运行返回中文错误；模型选择/客户端构建失败也返回错误，且不会
// 改变运行态。uid 写入 ad_rules.created_by，用于追溯是谁触发的。
func StartRuleDiscovery(sh *core.Shared, uid int64) error {
	if sh == nil {
		return errors.New("规则发现不可用：服务未初始化")
	}

	// 全程持锁：模型选择只读内存快照，构建 Eino 客户端也不发网络请求，
	// 放在锁内才能保证「检查空闲 → 占住运行态」之间没有竞态缝隙。
	ruleAgentRT.mu.Lock()
	defer ruleAgentRT.mu.Unlock()
	if ruleAgentRT.running {
		return errRuleAgentRunning
	}

	snap := sh.Cache.Snap()
	modelName, up, err := pickRuleAgentModel(snap)
	if err != nil {
		return err
	}
	chat, err := newRuleAgentChatModel(sh, up, modelName)
	if err != nil {
		return fmt.Errorf("初始化规则发现模型失败：%w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), ruleAgentTimeout)
	ruleAgentRT.running = true
	ruleAgentRT.startedAt = time.Now().Unix()
	ruleAgentRT.finishedAt = 0
	ruleAgentRT.result = ""
	ruleAgentRT.errText = ""
	ruleAgentRT.createdRuleIDs = nil
	ruleAgentRT.steps = nil
	ruleAgentRT.seq = 0
	ruleAgentRT.cancel = cancel

	slog.Info("规则发现：开始运行", "模型", modelName, "上游", upName(up), "操作者", uid)
	go runRuleDiscovery(ctx, sh, uid, chat, modelName)
	return nil
}

// RuleAgentStatus 返回 Mini App 的序列化状态。契约（T-C 前端依赖）：
//
//	{"running":bool,"started_at":int,"finished_at":int,"result":string,
//	 "error":string,"created_rule_id":int,"created_rule_ids":[int],
//	 "steps":[{"n":int,"at":int,"kind":string,"name":string,"summary":string}]}
//
// created_rule_id 是本轮第一条（旧前端兼容），created_rule_ids 是全部；
// 无创建时返回空切片而不是 null。
func RuleAgentStatus(sh *core.Shared) map[string]any {
	ruleAgentRT.mu.Lock()
	defer ruleAgentRT.mu.Unlock()
	steps := make([]map[string]any, 0, len(ruleAgentRT.steps))
	for _, s := range ruleAgentRT.steps {
		steps = append(steps, map[string]any{
			"n": s.N, "at": s.At, "kind": s.Kind, "name": s.Name,
			"summary": s.Summary,
		})
	}
	errText := ruleAgentRT.errText
	if ruleAgentRT.running {
		// 运行中不暴露上一轮的旧错误，前端只看 running 与实时步骤。
		errText = ""
	}
	ids := append([]int64{}, ruleAgentRT.createdRuleIDs...)
	var first int64
	if len(ids) > 0 {
		first = ids[0]
	}
	return map[string]any{
		"running":          ruleAgentRT.running,
		"started_at":       ruleAgentRT.startedAt,
		"finished_at":      ruleAgentRT.finishedAt,
		"result":           ruleAgentRT.result,
		"error":            errText,
		"created_rule_id":  first,
		"created_rule_ids": ids,
		"steps":            steps,
	}
}

// StopRuleDiscovery 请求中止当前一轮。没有在跑返回 false。
//
// 只是发 cancel 信号：结束状态由后台 goroutine 统一写（Running=false、
// Result/Error 落定），避免取消者与执行者各写一半。
func StopRuleDiscovery(sh *core.Shared) bool {
	ruleAgentRT.mu.Lock()
	defer ruleAgentRT.mu.Unlock()
	if !ruleAgentRT.running || ruleAgentRT.cancel == nil {
		return false
	}
	ruleAgentRT.cancel()
	return true
}

// ---- 模型选择 ----

// ruleAgentModelHint 是显式模型配置出错时给管理员的指路文案。
const ruleAgentModelHint = "请在「全局设置 → 默认模型 → 规则发现模型」里改选，" +
	"或清空它以回退到复判模型列表的第一个 OpenAI 兼容模型"

// pickRuleAgentModel 选择本轮规则发现的模型。
//
// 优先读全局设置 antiad_rule_model（形如 <上游名>/<模型ID>）：非空时必须
// 指向一个「已登记且启用、绑定上游已启用、支持 chat、渠道 OpenAI 兼容」
// 的模型，任何一条不满足都在 start 阶段返回明确的中文错误；留空则回退到
// 复判模型列表（snap.ModelsFor(0)）里第一个满足同样条件的模型。
func pickRuleAgentModel(snap *store.Snapshot) (string, *upstream.Upstream, error) {
	if name := strings.TrimSpace(snap.Setting("antiad_rule_model")); name != "" {
		return pickConfiguredRuleAgentModel(snap, name)
	}
	return pickFallbackRuleAgentModel(snap)
}

// pickConfiguredRuleAgentModel 校验显式指定的规则发现模型。每条错误都
// 带上 ruleAgentModelHint，管理员照着文案就能找到该改哪个设置项。
func pickConfiguredRuleAgentModel(snap *store.Snapshot,
	name string) (string, *upstream.Upstream, error) {

	fail := func(reason string) (string, *upstream.Upstream, error) {
		return "", nil, fmt.Errorf("规则发现模型配置无效：%s；%s", reason, ruleAgentModelHint)
	}
	// 模型必须登记且启用：设置里可能残留已删除/停用的名字。
	m := snap.Models[name]
	if m == nil {
		return fail(fmt.Sprintf("模型 %s 不在「模型定价」里", name))
	}
	if !m.Enabled {
		return fail(fmt.Sprintf("模型 %s 已被停用", name))
	}
	// 规则发现只认绑定了具体上游的模型：没有前缀就无法确定路由。
	upName := m.UpstreamName()
	if upName == "" {
		return fail(fmt.Sprintf("模型 %s 缺少「上游名/」前缀，无法确定绑定上游", name))
	}
	var u *upstream.Upstream
	for _, cand := range snap.Upstreams {
		if cand.Name == upName {
			u = cand
			break
		}
	}
	if u == nil {
		return fail(fmt.Sprintf("模型 %s 绑定的上游 %s 不存在", name, upName))
	}
	if u.Status != 1 {
		return fail(fmt.Sprintf("模型 %s 绑定的上游 %s 已停用", name, upName))
	}
	if !u.Supports(upstream.EPChat) {
		return fail(fmt.Sprintf("模型 %s 绑定的上游 %s 未开启 chat 能力", name, upName))
	}
	if k := u.EffectiveKind(); k != upstream.KindOpenAI {
		return fail(fmt.Sprintf("模型 %s 绑定的上游 %s 是 %s 渠道，规则发现只支持 OpenAI 兼容渠道",
			name, upName, k.Label()))
	}
	return name, u, nil
}

// pickFallbackRuleAgentModel 从全局复判模型列表（snap.ModelsFor(0)）里挑
// 第一个满足「模型已启用 + 有支持 chat 的上游 + 上游是 OpenAI Completions
// 渠道」的模型。返回模型全名（含上游前缀）与命中的上游。
func pickFallbackRuleAgentModel(snap *store.Snapshot) (string, *upstream.Upstream, error) {
	_, llmModels := snap.ModelsFor(0)
	if len(llmModels) == 0 {
		return "", nil, errRuleAgentNoModels
	}
	sawNonCompat := false
	for _, name := range llmModels {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		// 模型必须登记且启用：列表里可能残留已删除/停用的名字。
		if m := snap.Models[name]; m == nil || !m.Enabled {
			continue
		}
		for _, u := range upstreamFor(snap, upstream.EPChat, name) {
			if u.EffectiveKind() != upstream.KindOpenAI {
				sawNonCompat = true
				continue
			}
			return name, u, nil
		}
	}
	if sawNonCompat {
		return "", nil, errRuleAgentNoCompat
	}
	return "", nil, errRuleAgentNoModel
}

// newRuleAgentChatModel 按上游配置构建 Eino 的 OpenAI ChatModel。
//
// base_url 约定与判定链路一致：只填到域名/网关前缀，路径由客户端补。
// 本项目自己的 URL 拼接是 base + "/v1/chat/completions"，而 Eino 客户端
// 只补 "/chat/completions"，所以这里显式接上 "/v1"（已经带 /v1 的配置
// 不再重复加）。temperature=0：规则发现要的是稳定复现，不是创造力。
func newRuleAgentChatModel(sh *core.Shared, u *upstream.Upstream,
	fullName string) (model.ToolCallingChatModel, error) {

	_, modelID := upstream.SplitModelName(fullName)
	if modelID == "" {
		return nil, fmt.Errorf("模型 %q 缺少模型 ID", fullName)
	}
	base := strings.TrimRight(u.BaseURL, "/")
	if base == "" {
		return nil, fmt.Errorf("上游 %s 未配置 base_url", upName(u))
	}
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	zero := float32(0)
	cfg := &einoopenai.ChatModelConfig{
		APIKey:      u.APIKey,
		BaseURL:     base,
		Model:       modelID,
		Temperature: &zero,
	}
	if sh.AIClient != nil {
		// 复用判定链路的客户端：同样的代理配置、连接池与 100 秒总超时。
		cfg.HTTPClient = sh.AIClient
	} else {
		cfg.Timeout = 60 * time.Second
	}
	m, err := einoopenai.NewChatModel(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	// 包一层网络重试：上游偶发超时/5xx 不该让整轮发现直接失败。
	return withModelRetry(m), nil
}

// ---- 执行 ----

// ruleAgentRun 是一轮执行的运行期上下文。依赖字段只读；计数与结束标记
// 由 mu 保护（工具可能在多轮里被并发调用）。
type ruleAgentRun struct {
	sh    *core.Shared
	uid   int64
	model string // 模型全名，仅用于日志/结果说明
	// ctx 只用于 finish 判定「超时/手动停止」：上游错误经过 Eino 的层层
	// 包装后，单看 error 链未必能分辨是哪种结束，ctx 状态最可靠。
	ctx context.Context

	mu             sync.Mutex
	createdRuleIDs []int64
	createFails    int
	lastCreateErr  string
}

// runRuleDiscovery 在后台跑完一轮，并负责把结束状态写回全局运行态。
func runRuleDiscovery(ctx context.Context, sh *core.Shared, uid int64,
	chat model.ToolCallingChatModel, modelName string) {

	run := &ruleAgentRun{sh: sh, uid: uid, model: modelName, ctx: ctx}
	tools, err := buildRuleAgentTools(sh, run)
	if err != nil {
		run.finish(nil, fmt.Errorf("构建规则发现工具失败: %w", err))
		return
	}

	// ReAct Agent：原生 tool calling + 多轮工具循环。MaxStep 同时兜住
	// 模型不收敛（一直调工具不写规则）的情况。
	ag, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: chat,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: tools,
			// 顺序执行让步骤日志的顺序与模型意图一致，也避免并发工具
			// 同时写全局步骤序号。
			ExecuteSequentially: true,
			// 模型偶尔会调用不存在的工具；不接这个回调时 ToolsNode 会
			// 直接返回错误把图掐死，模型连纠正工具名的机会都没有。
			UnknownToolsHandler: func(ctx context.Context, name, input string) (string, error) {
				msg := fmt.Sprintf("没有名为 %s 的工具，可用工具：%s。请改用可用工具重试。",
					name, strings.Join(ruleAgentToolNames, "、"))
				run.toolFailStep(name, msg)
				return msg, nil
			},
		},
		MaxStep: ruleAgentMaxStep,
	})
	if err != nil {
		run.finish(nil, fmt.Errorf("构建规则发现 Agent 失败: %w", err))
		return
	}

	// 模型轮次回调：每次模型输出记一条步骤（请求了哪些工具/最终回答）。
	// 工具调用本身在工具实现里记录（参数与结果最准确）。
	handler := cbutils.NewHandlerHelper().ChatModel(&cbutils.ModelCallbackHandler{
		OnEnd: func(ctx context.Context, _ *callbacks.RunInfo,
			out *model.CallbackOutput) context.Context {
			run.recordModelStep(out)
			return ctx
		},
		OnError: func(ctx context.Context, _ *callbacks.RunInfo, err error) context.Context {
			ruleAgentAppendStep("model", "模型失败", "模型请求失败："+err.Error())
			return ctx
		},
	}).Handler()

	_, genErr := ag.Generate(ctx,
		[]*schema.Message{
			schema.SystemMessage(ruleAgentSystemPrompt),
			schema.UserMessage(ruleAgentUserPrompt),
		},
		einoagent.WithComposeOptions(compose.WithCallbacks(handler)))
	run.finish(genErr, nil)
}

// finish 统一落定运行态：Running 置回 false，写 Result/Error。
// 成功/失败/超时/步数耗尽/手动停止都必须经过这里。
func (r *ruleAgentRun) finish(genErr, setupErr error) {
	r.mu.Lock()
	created := append([]int64{}, r.createdRuleIDs...)
	fails, lastErr := r.createFails, r.lastCreateErr
	r.mu.Unlock()

	ctxErr := r.ctx.Err()
	var result, errText string
	switch {
	case len(created) > 0:
		ids := make([]string, 0, len(created))
		for _, id := range created {
			ids = append(ids, fmt.Sprintf("#%d", id))
		}
		result = fmt.Sprintf("发现完成：本轮写入 %d 条候选规则（%s），均未启用、未强制；"+
			"请在必封规则列表里逐条复核测试后决定是否启用。",
			len(created), strings.Join(ids, "、"))
	case fails >= ruleAgentMaxCreateFails:
		errText = fmt.Sprintf("连续 %d 次创建候选规则被拒绝，已提前结束", fails)
		result = fmt.Sprintf("已停止：连续 %d 次调用 create_rule 都被拒绝（规则会命中正常消息），"+
			"本轮未能产出高精度正则。最后一次原因：%s", fails, lastErr)
	case setupErr != nil:
		errText = "规则发现启动失败"
		result = "运行失败：" + setupErr.Error()
	case ctxErr == context.DeadlineExceeded:
		errText = fmt.Sprintf("运行超时（%s）", ruleAgentTimeout)
		result = fmt.Sprintf("超时结束：规则发现超过 %s 没有收敛，未创建规则。", ruleAgentTimeout)
	case ctxErr == context.Canceled:
		errText = "已手动停止"
		result = "已手动停止：本轮规则发现被中止，未创建规则。"
	case errors.Is(genErr, compose.ErrExceedMaxSteps):
		errText = fmt.Sprintf("步数耗尽（最多 %d 步）", ruleAgentMaxStep)
		result = fmt.Sprintf("步数耗尽：模型在 %d 步内没有产出可创建的规则，已结束。"+
			"可以调小试错范围或换更强的模型再试。", ruleAgentMaxStep)
	case genErr != nil:
		errText = "规则发现运行失败"
		result = "运行失败：" + core.TruncateRunes(genErr.Error(), 300)
	default:
		result = fmt.Sprintf("运行结束：模型 %s 没有创建规则。"+
			"请查看步骤日志了解它在哪一步停下。", r.model)
	}

	ruleAgentRT.mu.Lock()
	ruleAgentRT.running = false
	ruleAgentRT.finishedAt = time.Now().Unix()
	ruleAgentRT.result = result
	ruleAgentRT.errText = errText
	ruleAgentRT.createdRuleIDs = append([]int64{}, created...)
	ruleAgentRT.cancel = nil
	ruleAgentRT.mu.Unlock()

	if errText != "" {
		slog.Warn("规则发现：本轮结束", "结果", result, "错误", errText)
	} else {
		slog.Info("规则发现：本轮结束", "结果", result)
	}
}

// recordModelStep 把一次模型输出记成步骤：有工具调用就列工具与参数，
// 没有就是最终回答。
func (r *ruleAgentRun) recordModelStep(out *model.CallbackOutput) {
	if out == nil || out.Message == nil {
		return
	}
	msg := out.Message
	if len(msg.ToolCalls) > 0 {
		parts := make([]string, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			parts = append(parts, fmt.Sprintf("%s(%s)", tc.Function.Name,
				core.TruncateRunes(tc.Function.Arguments, 160)))
		}
		ruleAgentAppendStep("model", "模型决策", "请求调用工具："+strings.Join(parts, "；"))
		return
	}
	if c := strings.TrimSpace(msg.Content); c != "" {
		ruleAgentAppendStep("model", "模型收尾", "最终回答："+core.TruncateRunes(c, 240))
	}
}

// ruleAgentAppendStep 追加一条步骤日志，序号自增，摘要硬截断到 300 字符。
func ruleAgentAppendStep(kind, name, summary string) {
	ruleAgentRT.mu.Lock()
	defer ruleAgentRT.mu.Unlock()
	ruleAgentRT.seq++
	ruleAgentRT.steps = append(ruleAgentRT.steps, RuleAgentStep{
		N: ruleAgentRT.seq, At: time.Now().Unix(), Kind: kind,
		Name:    core.TruncateRunes(name, 60),
		Summary: core.TruncateRunes(summary, ruleAgentSummaryMax),
	})
}

// toolStep 在工具实现里记录一次调用的参数与结果摘要。
func (r *ruleAgentRun) toolStep(name string, args any, result string) {
	argsJSON, err := json.Marshal(args)
	if err != nil {
		argsJSON = []byte(fmt.Sprint(args))
	}
	summary := fmt.Sprintf("参数：%s；结果：%s",
		core.TruncateRunes(string(argsJSON), 120),
		core.TruncateRunes(result, 200))
	ruleAgentAppendStep("tool", name, summary)
}

// toolFailStep 记录一次没走到工具实现的失败调用（参数解析失败、工具名
// 不存在）。此时拿不到参数，模型请求里的参数在 model 步骤里，两下一凑
// 就能看出模型发了什么、错在哪。
func (r *ruleAgentRun) toolFailStep(name, msg string) {
	ruleAgentAppendStep("tool", name, "调用失败："+msg)
}

// wrapRuleTool 给工具套一层错误兜底：InferTool 在参数 JSON 解析失败时
// 返回 Go error，ToolsNode 若不拦截会直接让整张图报错退出 —— 模型连
// 「参数写错了」都看不到。包装后错误变成一条中文工具结果回给模型，同时
// 记进步骤日志，让模型自己纠正参数继续跑。
func wrapRuleTool(run *ruleAgentRun, name string, t tool.BaseTool) tool.BaseTool {
	return toolutils.WrapToolWithErrorHandler(t, func(ctx context.Context, err error) string {
		msg := fmt.Sprintf("工具 %s 调用失败：参数或执行出错，请检查参数格式后重试。原始错误：%s",
			name, core.TruncateRunes(err.Error(), 200))
		run.toolFailStep(name, msg)
		return msg
	})
}

// ---- 工具 ----

// listBannedArgs 是 list_banned 的参数。
type listBannedArgs struct {
	Limit  int    `json:"limit" jsonschema:"description=返回条数，默认 50，最大 200"`
	Offset int    `json:"offset" jsonschema:"description=OFFSET 分页偏移，默认 0"`
	Kind   string `json:"kind" jsonschema:"description=只看某个广告类型（如 scam/porn），空串表示全部"`
}

// readRecordArgs 是 read_record 的参数。
type readRecordArgs struct {
	ID int64 `json:"id" jsonschema:"required,description=判定流水的 id（来自 list_banned / find 的结果）"`
}

// readRecordsArgs 是 read_records 的参数。
type readRecordsArgs struct {
	IDs []int64 `json:"ids" jsonschema:"required,description=要读取的判定流水 id 列表（最多 10 个，去重后截断）"`
}

// listKindsArgs / listRulesArgs 无参数；InferTool 需要占位类型。
type listKindsArgs struct{}
type listRulesArgs struct{}

// findArgs 是 find 的参数。
type findArgs struct {
	Pattern string `json:"pattern" jsonschema:"required,description=RE2 正则草稿（宽进：只要能编译就能试跑）"`
	Scope   string `json:"scope" jsonschema:"description=banned（默认）样本优先展示已确认广告并附正常消息参照；all 按时间倒序展示全部命中"`
	Limit   int    `json:"limit" jsonschema:"description=样本条数，默认 50，最大 200"`
}

// testRuleArgs 是 test_rule 的参数。
type testRuleArgs struct {
	Pattern string `json:"pattern" jsonschema:"required,description=要全库验证的 RE2 正则"`
}

// createRuleArgs 是 create_rule 的参数。
type createRuleArgs struct {
	Name        string  `json:"name" jsonschema:"required,description=规则中文名称，不超过 60 字，说明打击什么广告形态"`
	Category    string  `json:"category" jsonschema:"description=分类（如 contact/domain/promo），不超过 20 字"`
	Pattern     string  `json:"pattern" jsonschema:"required,description=RE2 正则；不得匹配空文本；必须通过全库测试（fp=0 且 undone=0）"`
	Note        string  `json:"note" jsonschema:"description=中文理由与证据说明，不超过 300 字"`
	EvidenceIDs []int64 `json:"evidence_ids" jsonschema:"description=支持该规则的判定流水 id 列表，来自 list_banned / read_record / find"`
}

// buildRuleAgentTools 构造八个只读/候选写入工具。工具返回字符串而不是
// Go error：失败信息要留给模型自我纠正，不该让整个图崩掉。
func buildRuleAgentTools(sh *core.Shared, run *ruleAgentRun) ([]tool.BaseTool, error) {
	kindsTool, err := toolutils.InferTool("list_kinds",
		"按广告类型（ad_kind）查看历史已确认广告的分布：每类总数与最近流水 id。"+
			"开始发现前先用它决定先覆盖哪些类型，再用 read_records 批量读样本。",
		func(ctx context.Context, _ listKindsArgs) (string, error) {
			out := run.listKinds()
			run.toolStep("list_kinds", map[string]any{}, out)
			return out, nil
		})
	if err != nil {
		return nil, err
	}

	list, err := toolutils.InferTool("list_banned",
		"查看最近的已确认广告判定流水（verdict='ad'）：id、广告类型、处置、群号与正文摘要。"+
			"挑选值得总结的形态；可用 offset 翻更早的流水。",
		func(ctx context.Context, in listBannedArgs) (string, error) {
			out := run.listBanned(in)
			run.toolStep("list_banned", in, out)
			return out, nil
		})
	if err != nil {
		return nil, err
	}

	read, err := toolutils.InferTool("read_record",
		"读取一条判定流水的完整正文与判定信息（正文最多 2000 字）。"+
			"id 来自 list_banned 或 find 的命中样本。",
		func(ctx context.Context, in readRecordArgs) (string, error) {
			out := run.readRecord(in.ID)
			run.toolStep("read_record", in, out)
			return out, nil
		})
	if err != nil {
		return nil, err
	}

	readMany, err := toolutils.InferTool("read_records",
		"一次读取多条判定流水的完整正文（最多 10 个 id，去重截断）。"+
			"id 来自 list_kinds 的 recent_ids、list_banned 或 find 的命中样本。",
		func(ctx context.Context, in readRecordsArgs) (string, error) {
			out := run.readRecords(in.IDs)
			run.toolStep("read_records", in, out)
			return out, nil
		})
	if err != nil {
		return nil, err
	}

	find, err := toolutils.InferTool("find",
		"用宽松正则扫描全部历史判定流水（最多 10 万条），返回命中统计与样本，不会创建任何规则。"+
			"这是写规则前的试跑工具：反复收缩正则，直到只命中广告、不碰正常消息。",
		func(ctx context.Context, in findArgs) (string, error) {
			out := run.findMatches(ctx, in)
			run.toolStep("find", in, out)
			return out, nil
		})
	if err != nil {
		return nil, err
	}

	test, err := toolutils.InferTool("test_rule",
		"用与保存入口完全一致的口径全库测试一条正则：返回 tp（命中广告）、"+
			"fp（命中正常消息/被撤销的误判）、undone、neutral、覆盖率（总体与"+
			"按 ad_kind 细分）与误封样本。fp 或 undone 大于 0 时绝对不能创建规则。",
		func(ctx context.Context, in testRuleArgs) (string, error) {
			out := run.testRule(ctx, in.Pattern)
			run.toolStep("test_rule", in, out)
			return out, nil
		})
	if err != nil {
		return nil, err
	}

	listRulesTool, err := toolutils.InferTool("list_rules",
		"查看现有必封规则（最近 100 条）：pattern、启用/强制状态与最近覆盖率。"+
			"用来避免重复创建，并找还没有规则覆盖的广告类型。",
		func(ctx context.Context, _ listRulesArgs) (string, error) {
			out := run.listRules()
			run.toolStep("list_rules", map[string]any{}, out)
			return out, nil
		})
	if err != nil {
		return nil, err
	}

	create, err := toolutils.InferTool("create_rule",
		"创建一条候选必封规则（enabled=0、enforce=0，等主管理员复核）。"+
			"会做严格校验与全库测试；命中任何正常消息（fp>0 或 undone>0）或已存在相同正则都会被拒绝。"+
			"创建成功后继续找下一个未覆盖形态，没有新形态时再总结收尾。",
		func(ctx context.Context, in createRuleArgs) (string, error) {
			out := run.createRule(ctx, in)
			run.toolStep("create_rule", in, out)
			return out, nil
		})
	if err != nil {
		return nil, err
	}

	// 每个工具都套错误兜底：参数畸形、执行出错都转成工具结果串回给模型，
	// 而不是让 ToolsNode 掐死整张图。
	return []tool.BaseTool{
		wrapRuleTool(run, "list_kinds", kindsTool),
		wrapRuleTool(run, "list_banned", list),
		wrapRuleTool(run, "read_record", read),
		wrapRuleTool(run, "read_records", readMany),
		wrapRuleTool(run, "find", find),
		wrapRuleTool(run, "test_rule", test),
		wrapRuleTool(run, "list_rules", listRulesTool),
		wrapRuleTool(run, "create_rule", create),
	}, nil
}

// ---- 工具实现 ----

// listBanned 返回最近的已确认广告流水摘要。
func (r *ruleAgentRun) listBanned(in listBannedArgs) string {
	limit := in.Limit
	if limit <= 0 {
		limit = ruleAgentListBannedDefault
	}
	if limit > ruleAgentListBannedMax {
		limit = ruleAgentListBannedMax
	}
	offset := in.Offset
	if offset < 0 {
		offset = 0
	}
	kind := strings.TrimSpace(in.Kind)

	q := `SELECT id,ad_kind,action,chat_id,created_at,text FROM antiad_log
		WHERE verdict='ad'`
	args := []any{}
	if kind != "" {
		q += ` AND ad_kind=?`
		args = append(args, kind)
	}
	q += ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := r.sh.Store.Read.Query(q, args...)
	if err != nil {
		return "错误：读取判定流水失败：" + err.Error()
	}
	defer rows.Close()

	type item struct {
		ID     int64  `json:"id"`
		Kind   string `json:"kind"`
		Action string `json:"action"`
		ChatID int64  `json:"chat_id"`
		At     int64  `json:"at"`
		Text   string `json:"text"`
	}
	items := []item{}
	for rows.Next() {
		var it item
		var text string
		if err := rows.Scan(&it.ID, &it.Kind, &it.Action, &it.ChatID,
			&it.At, &text); err != nil {
			return "错误：解析判定流水失败：" + err.Error()
		}
		it.Text = core.TruncateRunes(text, ruleAgentSampleTextMax)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return "错误：读取判定流水失败：" + err.Error()
	}
	out, _ := json.Marshal(map[string]any{
		"rows": items, "next_offset": offset + len(items),
		"hint": "action='undone' 表示该判罚已被管理员撤销（可能误判），分析形态时要排除。"})
	return string(out)
}

// readRecordPayload 读一条流水的完整载荷；失败返回错误文案。
func (r *ruleAgentRun) readRecordPayload(id int64) (map[string]any, string) {
	if id <= 0 {
		return nil, "id 必须为正整数"
	}
	row, ok := LoadAdLog(r.sh.Store, id)
	if !ok {
		return nil, fmt.Sprintf("没有 id=%d 的判定记录", id)
	}
	return map[string]any{
		"id": row.ID, "bot_id": row.BotID, "chat_id": row.ChatID,
		"user_id": row.UserID, "user_name": row.UserName,
		"message_id": row.MessageID, "verdict": row.Verdict,
		"confidence": row.Confidence, "decider": row.Decider,
		"kind": row.Kind, "action": row.Action, "reason": row.Reason,
		"created_at": row.CreatedAt,
		"text":       core.TruncateRunes(row.Text, ruleAgentReadTextMax),
	}, ""
}

// readRecord 返回一条流水的完整信息（正文截断 2000 字）。
func (r *ruleAgentRun) readRecord(id int64) string {
	payload, errMsg := r.readRecordPayload(id)
	if errMsg != "" {
		return "错误：" + errMsg
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return "错误：记录序列化失败：" + err.Error()
	}
	return string(out)
}

// readRecords 批量读：去重后截断到 10 条；被截断与重复计入 ignored；
// 无效/不存在的 id 以 error 条目返回，不算 ignored。
func (r *ruleAgentRun) readRecords(ids []int64) string {
	if len(ids) == 0 {
		return "错误：ids 必须是非空数组"
	}
	seen := make(map[int64]bool, len(ids))
	uniq := make([]int64, 0, len(ids))
	ignored := 0
	for _, id := range ids {
		if seen[id] {
			ignored++
			continue
		}
		seen[id] = true
		if len(uniq) >= ruleAgentReadRecordsMax {
			ignored++
			continue
		}
		uniq = append(uniq, id)
	}
	records := make([]map[string]any, 0, len(uniq))
	for _, id := range uniq {
		if payload, errMsg := r.readRecordPayload(id); errMsg != "" {
			records = append(records, map[string]any{"id": id, "error": errMsg})
		} else {
			records = append(records, payload)
		}
	}
	out, _ := json.Marshal(map[string]any{"records": records, "ignored": ignored})
	return string(out)
}

// listKinds 按 ad_kind 聚合已确认广告：总数 + 每类最近 5 个流水 id。
func (r *ruleAgentRun) listKinds() string {
	rows, err := r.sh.Store.Read.Query(`SELECT id,verdict,action,ad_kind
		FROM antiad_log ORDER BY id DESC LIMIT ?`, ruleAgentFindScanLimit)
	if err != nil {
		return "错误：读取判定流水失败：" + err.Error()
	}
	defer rows.Close()

	var scanned int64
	totals := map[string]int64{}
	recent := map[string][]int64{}
	for rows.Next() {
		var id int64
		var verdict, action, kind string
		if err := rows.Scan(&id, &verdict, &action, &kind); err != nil {
			return "错误：解析判定流水失败：" + err.Error()
		}
		scanned++
		if verdict != "ad" || action == "undone" {
			continue
		}
		totals[kind]++
		if len(recent[kind]) < ruleAgentListKindsRecent {
			recent[kind] = append(recent[kind], id)
		}
	}
	if err := rows.Err(); err != nil {
		return "错误：读取判定流水失败：" + err.Error()
	}

	kinds := make([]map[string]any, 0, len(totals))
	var adsTotal int64
	for kind, total := range totals {
		adsTotal += total
		kinds = append(kinds, map[string]any{
			"kind": kind, "total": total, "recent_ids": recent[kind]})
	}
	sort.Slice(kinds, func(i, j int) bool {
		ti, tj := kinds[i]["total"].(int64), kinds[j]["total"].(int64)
		if ti != tj {
			return ti > tj
		}
		return kinds[i]["kind"].(string) < kinds[j]["kind"].(string)
	})
	out, _ := json.Marshal(map[string]any{
		"scanned": scanned, "ads_total": adsTotal, "kinds": kinds,
		"hint": "先按 total 从大到小覆盖；recent_ids 可用 read_records 批量读全文。",
	})
	return string(out)
}

// listRules 返回最近 100 条规则（id 降序取、升序返回）与最近覆盖率。
func (r *ruleAgentRun) listRules() string {
	rows, err := r.sh.Store.Read.Query(`SELECT id,name,pattern,category,enabled,
		enforce,last_tested_at,last_tp,last_fp,last_ads_total,last_kinds
		FROM ad_rules ORDER BY id DESC LIMIT ?`, ruleAgentListRulesMax)
	if err != nil {
		return "错误：读取必封规则失败：" + err.Error()
	}
	defer rows.Close()

	rules := []map[string]any{}
	for rows.Next() {
		var (
			id, en, enf, tested, tp, fp, adsTotal int64
			name, pattern, category, kindsRaw     string
		)
		if err := rows.Scan(&id, &name, &pattern, &category, &en, &enf,
			&tested, &tp, &fp, &adsTotal, &kindsRaw); err != nil {
			return "错误：解析必封规则失败：" + err.Error()
		}
		rules = append(rules, map[string]any{
			"id": id, "name": name, "pattern": pattern, "category": category,
			"enabled": en == 1, "enforce": enf == 1, "last_tested_at": tested,
			"last_tp": tp, "last_fp": fp, "last_ads_total": adsTotal,
			"coverage":   round4(ruleCoverage(tp, adsTotal)),
			"last_kinds": ParseRuleKindsJSON(kindsRaw),
		})
	}
	if err := rows.Err(); err != nil {
		return "错误：读取必封规则失败：" + err.Error()
	}
	// 降序取最近 100 条，反转为升序返回，保证确定性。
	slices.Reverse(rules)
	out, _ := json.Marshal(map[string]any{
		"rules": rules,
		"hint":  "pattern 已存在的形态不要重复创建；优先补覆盖率为 0 或偏低的类型。",
	})
	return string(out)
}

// ruleAgentSample 是工具返回给模型的命中样本 JSON 形状。
type ruleAgentSample struct {
	ID      int64  `json:"id"`
	Verdict string `json:"verdict"`
	Action  string `json:"action"`
	Kind    string `json:"kind"`
	ChatID  int64  `json:"chat_id"`
	At      int64  `json:"at"`
	Text    string `json:"text"`
}

// round4 把覆盖率四舍五入到 4 位小数：给模型的 JSON 省 token。
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

// ruleCoverage 计算覆盖率；分母为 0 时 0。口径与 RuleTestResult.Coverage()
// 相同（verdict='ad' 且 action<>'undone' 为分母），改动时两处要一起改。
func ruleCoverage(matched, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(matched) / float64(total)
}

// ruleKindsAgentJSON 把类型细分转成工具 JSON：带每类覆盖率（4 位小数）。
func ruleKindsAgentJSON(kinds []RuleKindStat) []map[string]any {
	out := make([]map[string]any, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, map[string]any{
			"kind": k.Kind, "total": k.Total, "matched": k.Matched,
			"coverage": round4(k.Coverage()),
		})
	}
	return out
}

// findMatches 用宽松编译扫描全库，返回命中统计与样本。
//
// 计数分类与 TestRulePattern 完全同口径（action='undone' 计入 fp/undone，
// verdict=clean/none 计入 fp，skipped/error 计入 neutral）；by_verdict 是
// 命中行的原始 verdict 分布，用来补足 TP/FP/Neutral 之外的信息。
// scope=banned 时样本优先给已确认广告，再用正常消息作参照；scope=all
// 按时间倒序给全部命中。
//
// 扫描每 ruleScanCtxCheckEvery 行检查一次 ctx：stop 后不必等十万行扫完。
func (r *ruleAgentRun) findMatches(ctx context.Context, in findArgs) string {
	re, err := compileRuleLenient(in.Pattern)
	if err != nil {
		return "错误：" + err.Error()
	}
	scope := strings.TrimSpace(in.Scope)
	if scope == "" {
		scope = "banned"
	}
	if scope != "banned" && scope != "all" {
		return `错误：scope 只能是 banned 或 all`
	}
	limit := in.Limit
	if limit <= 0 {
		limit = ruleAgentFindDefault
	}
	if limit > ruleAgentFindMax {
		limit = ruleAgentFindMax
	}

	acc := newRuleKindAcc()
	rows, err := r.sh.Store.Read.Query(`SELECT id,verdict,action,ad_kind,
		chat_id,created_at,text FROM antiad_log
		ORDER BY id DESC LIMIT ?`, ruleAgentFindScanLimit)
	if err != nil {
		return "错误：扫描判定流水失败：" + err.Error()
	}
	defer rows.Close()

	var (
		scanned, matched, tp, fp, undone, neutral int64
		ads, refs, all                            []ruleAgentSample
	)
	byVerdict := map[string]int64{
		"ad": 0, "clean": 0, "none": 0, "skipped": 0, "error": 0,
	}
	for rows.Next() {
		var (
			s               ruleAgentSample
			verdict, action string
			text            string
		)
		if err := rows.Scan(&s.ID, &verdict, &action, &s.Kind, &s.ChatID,
			&s.At, &text); err != nil {
			return "错误：解析判定流水失败：" + err.Error()
		}
		scanned++
		if scanned%ruleScanCtxCheckEvery == 0 && ctx.Err() != nil {
			return "已停止：本轮扫描被取消，未统计完整结果。"
		}
		// 已确认广告：覆盖率分母与类型分母（与 TestRulePattern 同口径）。
		if verdict == "ad" && action != "undone" {
			acc.ad(s.Kind)
		}
		if !re.MatchString(text) {
			continue
		}
		matched++
		byVerdict[verdict]++
		s.Verdict, s.Action = verdict, action
		s.Text = core.TruncateRunes(text, ruleAgentSampleTextMax)

		// 与 TestRulePattern 的分类逐条对齐，计数口径不能分叉。
		switch {
		case action == "undone":
			undone++
			fp++
		case verdict == "ad":
			tp++
			acc.hit(s.Kind)
		case verdict == "clean" || verdict == "none":
			fp++
		case verdict == "skipped" || verdict == "error":
			neutral++
		}

		if verdict == "ad" && action != "undone" {
			if len(ads) < limit {
				ads = append(ads, s)
			}
		} else if len(refs) < limit {
			refs = append(refs, s)
		}
		if len(all) < limit {
			all = append(all, s)
		}
	}
	if err := rows.Err(); err != nil {
		return "错误：扫描判定流水失败：" + err.Error()
	}

	samples := all
	if scope == "banned" {
		samples = ads
		for _, s := range refs {
			if len(samples) >= limit {
				break
			}
			samples = append(samples, s)
		}
	}
	adsTotal, kinds := acc.result()
	out, _ := json.Marshal(map[string]any{
		"scanned": scanned, "matched": matched,
		"tp": tp, "fp": fp, "undone": undone, "neutral": neutral,
		"ads_total": adsTotal, "coverage": round4(ruleCoverage(tp, adsTotal)),
		"kinds":      ruleKindsAgentJSON(kinds),
		"by_verdict": byVerdict, "scope": scope, "samples": samples,
	})
	return string(out)
}

// testRule 调 T-A 的全库测试（可取消版本）做正式口径验证，并给出
// 明确的「能不能创建」结论。
func (r *ruleAgentRun) testRule(ctx context.Context, pattern string) string {
	res, err := testRulePatternCtx(ctx, r.sh, pattern)
	if err != nil {
		if errors.Is(err, errRuleScanStopped) {
			return "测试失败：已停止（本轮扫描被取消）。"
		}
		return "测试失败：" + err.Error()
	}
	out := map[string]any{
		"scanned": res.Scanned, "matched": res.Matched,
		"tp": res.TP, "fp": res.FP, "undone": res.Undone, "neutral": res.Neutral,
		"ads_total": res.AdsTotal, "coverage": round4(res.Coverage()),
		"kinds":          ruleKindsAgentJSON(res.Kinds),
		"fp_samples":     ruleSamplesJSON(res.FPSamples, 10),
		"undone_samples": ruleSamplesJSON(res.UndoneSamples, 5),
	}
	switch {
	case res.FP > 0 || res.Undone > 0:
		out["can_create"] = false
		out["message"] = fmt.Sprintf(
			"不可创建：该正则命中 %d 条正常消息（fp=%d，其中被撤销的误判 undone=%d）。"+
				"命中正常聊天即禁止创建，请按 fp_samples/undone_samples 收缩正则后重新 test_rule。",
			res.FP, res.FP, res.Undone)
	case res.Matched == 0:
		out["can_create"] = true
		out["message"] = "可以创建，但该正则没有命中任何历史记录，缺少证据支撑；" +
			"建议先用 find 检查是否写得太窄。"
	default:
		out["can_create"] = true
		out["message"] = fmt.Sprintf(
			"通过：命中 %d 条已确认广告（覆盖率 %.1f%%）、0 条正常消息，可以创建。",
			res.TP, res.Coverage()*100)
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// createRule 严格校验 + 全库测试 + 落库，是 AI 写入 ad_rules 的唯一入口。
//
// 与面板 miniRuleSave 同一道防误封闸门：CompileRulePattern 拒绝空/超长/
// 不可编译/匹配空文本；TestRulePattern 检出 fp/undone 一律不写库。
// 连续失败达到上限时调用 SetReturnDirectly 让 ReAct 图立即收尾。
func (r *ruleAgentRun) createRule(ctx context.Context, in createRuleArgs) string {
	fail := func(msg string) string {
		r.mu.Lock()
		r.createFails++
		r.lastCreateErr = msg
		fails := r.createFails
		r.mu.Unlock()
		if fails >= ruleAgentMaxCreateFails {
			// 到达上限：通知 ReAct 本轮直接返回，不再浪费模型轮次。
			// 失败也不影响结束判定（finish 会看 createFails）。
			_ = react.SetReturnDirectly(ctx)
		}
		return msg
	}

	name := strings.TrimSpace(in.Name)
	if name == "" {
		return fail("创建失败：规则名称不能为空。")
	}
	if len([]rune(name)) > 60 {
		return fail("创建失败：规则名称超过 60 字，请缩短。")
	}
	if _, err := CompileRulePattern(in.Pattern); err != nil {
		return fail("创建失败：正则未通过严格校验（" + err.Error() + "）。")
	}

	// 同一条正则在库里只允许存在一份，避免重复命中产生双份流水。
	var dup int64
	if err := r.sh.Store.Read.QueryRow(
		`SELECT COUNT(*) FROM ad_rules WHERE pattern=?`, in.Pattern).Scan(&dup); err != nil {
		return fail("创建失败：查重失败（" + err.Error() + "）。")
	}
	if dup > 0 {
		return fail("创建失败：已存在完全相同的正则规则，请勿重复创建。")
	}

	res, err := testRulePatternCtx(ctx, r.sh, in.Pattern)
	if err != nil {
		// 扫描被取消不算一次「创建失败」：这不是模型的正则写得不好，
		// 计数与连续失败保护别被取消动作污染。
		if errors.Is(err, errRuleScanStopped) {
			return "创建失败：已停止（本轮扫描被取消）。"
		}
		return fail("创建失败：全库测试失败（" + err.Error() + "）。")
	}
	if res.FP > 0 || res.Undone > 0 {
		var sb strings.Builder
		fmt.Fprintf(&sb, "创建被拒绝：该正则命中 %d 条正常消息（fp=%d，含被撤销 %d 条），"+
			"命中正常聊天即禁止创建。误封样本：", res.FP, res.FP, res.Undone)
		n := 0
		for _, s := range append(append([]RuleSample{}, res.FPSamples...),
			res.UndoneSamples...) {
			if n >= 5 {
				break
			}
			fmt.Fprintf(&sb, "[id=%d verdict=%s action=%s text=%q] ",
				s.ID, s.Verdict, s.Action, core.TruncateRunes(s.Text, 80))
			n++
		}
		sb.WriteString("请分析这些样本，收缩正则后重新 test_rule 验证。")
		return fail(sb.String())
	}

	// 证据 id 只保留库里真实存在、且 verdict='ad' 的：模型可能引用不存在
	// 的 id，或把正常消息当证据；把无效 id 写进 note 会污染规则的可解释性。
	validEvidence, ignoredEvidence := r.filterEvidence(in.EvidenceIDs)

	category := core.TruncateRunes(strings.TrimSpace(in.Category), 20)
	note := strings.TrimSpace(in.Note)
	if ids := formatRuleEvidence(validEvidence); ids != "" {
		if note != "" {
			note += " "
		}
		note += "证据记录：" + ids
	}
	note = core.TruncateRunes(note, 300)
	now := time.Now().Unix()

	// 候选规则：enabled=0、enforce=0，last_* 写本轮测试结果 —— 面板
	// 打开强制的前提数据在创建时就已经具备，管理员不必先跑一次测试。
	result, err := r.sh.Store.Write.Exec(`INSERT INTO ad_rules
		(name,pattern,category,note,source,enabled,enforce,
		 last_tp,last_fp,last_undone,last_scanned,last_tested_at,
		 last_ads_total,last_kinds,
		 created_at,created_by)
		VALUES (?,?,?,?, 'ai', 0, 0, ?,?,?,?,?, ?,?, ?,?)`,
		name, in.Pattern, category, note,
		res.TP, res.FP, res.Undone, res.Scanned, now,
		res.AdsTotal, RuleKindsJSON(res.Kinds), now, r.uid)
	if err != nil {
		return fail("创建失败：写库失败（" + err.Error() + "）。")
	}
	id, _ := result.LastInsertId()

	r.mu.Lock()
	r.createdRuleIDs = append(r.createdRuleIDs, id)
	r.createFails = 0 // 连续失败计数：成功即清零
	r.mu.Unlock()
	// 实时反映到状态里，面板轮询能看到 created_rule_ids。
	ruleAgentRT.mu.Lock()
	ruleAgentRT.createdRuleIDs = append(ruleAgentRT.createdRuleIDs, id)
	ruleAgentRT.mu.Unlock()

	// 写库后重建快照：虽然候选规则不参与判定（enabled=0），但面板的
	// 规则列表走快照，早一步可见。
	if err := r.sh.Cache.Reload(); err != nil {
		slog.Error("规则发现：候选规则写库后刷新缓存失败", "规则", id, "err", err)
	}
	// 成功不结束本轮：模型继续用 list_kinds / list_rules 找下一个未覆盖形态。
	msg := fmt.Sprintf("创建成功：候选规则 #%d《%s》已写入（enabled=0、enforce=0，等待主管理员复核）。"+
		"本轮测试：命中广告 %d 条、正常消息 0 条、覆盖率 %.1f%%（全库扫描 %d 条）。"+
		"请继续用 list_kinds / list_rules 找下一个未覆盖形态；没有新形态时用中文总结收尾。",
		id, name, res.TP, res.Coverage()*100, res.Scanned)
	if ignoredEvidence > 0 {
		msg += fmt.Sprintf("已忽略 %d 个无效、重复或非广告的证据 id。", ignoredEvidence)
	}
	return msg
}

// filterEvidence 校验证据 id：只保留 antiad_log 里真实存在且
// verdict='ad' 的行，去重并限量 ruleAgentEvidenceMax；同时返回被忽略的
// 数量（不存在、非广告、重复、超出上限都算）。
//
// action='undone' 的行 verdict 仍是 'ad'，按「只保留 verdict='ad'」的
// 口径保留 —— 它作为「当时被判为广告」的记录本身是成立的。
func (r *ruleAgentRun) filterEvidence(ids []int64) (valid []int64, ignored int) {
	seen := make(map[int64]bool, len(ids))
	for i, id := range ids {
		if i >= ruleAgentEvidenceInputMax {
			ignored += len(ids) - i
			break
		}
		if id <= 0 || seen[id] {
			ignored++
			continue
		}
		seen[id] = true
		if len(valid) >= ruleAgentEvidenceMax {
			ignored++
			continue
		}
		var one int64
		if err := r.sh.Store.Read.QueryRow(
			`SELECT 1 FROM antiad_log WHERE id=? AND verdict='ad'`, id).Scan(&one); err != nil {
			ignored++
			continue
		}
		valid = append(valid, id)
	}
	return valid, ignored
}

// formatRuleEvidence 把证据流水 id 拼成简短说明。note 总长 300 字的硬
// 上限下，只列前 10 个，避免把名称与理由挤掉。
func formatRuleEvidence(ids []int64) string {
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ids))
	for i, id := range ids {
		if i >= 10 {
			parts = append(parts, "…")
			break
		}
		if id > 0 {
			parts = append(parts, fmt.Sprintf("#%d", id))
		}
	}
	return strings.Join(parts, " ")
}

// ruleSamplesJSON 把测试结果里的样本转成 JSON 友好的形状（截取前 max 条）。
func ruleSamplesJSON(list []RuleSample, max int) []ruleAgentSample {
	if len(list) > max {
		list = list[:max]
	}
	out := make([]ruleAgentSample, 0, len(list))
	for _, s := range list {
		out = append(out, ruleAgentSample{
			ID: s.ID, Verdict: s.Verdict, Action: s.Action, Kind: s.Kind,
			ChatID: s.ChatID, At: s.CreatedAt, Text: s.Text,
		})
	}
	return out
}

// ---- 提示词 ----

// ruleAgentSystemPrompt 是规则发现 Agent 的中文系统提示词。
const ruleAgentSystemPrompt = `你是「门神」反广告系统的规则发现工程师，任务不是聊天，而是从历史判定流水里总结出多条高精度的「必封正则规则」，尽量覆盖不同的广告类型与形态。

可用语料：antiad_log 判定流水。verdict='ad' 是已确认广告，verdict='clean'/'none' 是正常消息，action='undone' 是被管理员撤销的误判。所有结论必须有流水证据，不得凭想象编造正则。

硬性约束（违反即创建失败）：
1. 正则必须是 Go RE2 语法：不支持环视（(?=...)/(?!...)）、反向引用（\1）、条件表达式等 PCRE 特性；
2. 正则不得匹配空文本（例如 a*、.* 一律不能作为规则，会命中纯图/贴纸消息）；
3. 不得命中任何正常聊天：create_rule 之前必须 test_rule，且 fp=0、undone=0；
4. 优先锚定广告特有的组合形态：联系方式（微信/QQ/telegram/whatsapp 等 + 账号串）、招聘/贷款/博彩等话术组合、引流域名与短链形态；不要用「加微信」「看主页」这类正常聊天也会大量出现的裸词；
5. 每条规则必须可解释：name 说明打击的广告形态，note 写明理由，evidence_ids 填你实际读过或命中的流水 id；
6. 建议用 (?i) 忽略大小写，用边界、字符类与量词收紧匹配；宁可少覆盖一点，也不能误封；
7. 不要创建与现有规则 pattern 完全相同的规则（create_rule 会拒绝）。

覆盖率说明：find 与 test_rule 会返回覆盖率（该正则命中的已确认广告 ÷ 全库已确认广告）与按 ad_kind 的细分。覆盖率不是创建门槛，但优先做「覆盖某类型较大比例」的形态；覆盖率过低说明规则太窄，考虑合并同类话术或换更有代表性的形态。

工作流（按顺序循环，不要跳步）：
1. list_kinds 看广告类型分布（total 从大到小）；
2. list_rules 看已有 pattern 与覆盖率，列出还没有被覆盖的类型/形态；
3. 针对一个未覆盖形态：list_banned（可按 kind 过滤、可翻 offset；分批读，不要一次拉满）→
   read_records 批量读全文（一次最多 10 条），观察它区别于正常消息的组合特征；
4. find 用正则草稿试跑，看命中什么、误伤什么、覆盖率多少；反复收缩直到只命中广告；
5. test_rule 用正式口径验证：fp>0 或 undone>0 时绝对不要 create_rule，回到第 4 步改进；
6. create_rule 创建候选规则（默认不启用，等主管理员复核）；
7. 创建成功后不要停，回到第 1/2 步找下一个未覆盖形态；没有新的高精度形态、或步数/时间预算将尽时，用中文总结收尾。

如果 create_rule 被拒绝：仔细阅读返回的误封样本，收缩正则后重新 test_rule；连续 3 次失败本轮会被终止。收尾总结请列出：本轮创建了哪些规则、分别覆盖什么形态、依据哪些证据。`

// ruleAgentUserPrompt 是每轮的启动指令。
const ruleAgentUserPrompt = `请开始一轮规则发现：先用 list_kinds 看广告类型分布，再用 list_rules 看已有覆盖与空白；然后对每个未覆盖的高精度形态按 list_banned → read_records → find → test_rule → create_rule 的流程产出候选规则。创建成功后不要停，继续找下一个形态；没有新形态或预算将尽时总结收尾。`
