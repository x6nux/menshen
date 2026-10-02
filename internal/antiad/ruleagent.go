package antiad

// 本文件实现「AI 必封规则发现 Agent」：用 CloudWeGo Eino 的 ReAct Agent
// 驱动一轮工具循环，从 antiad_log 判定流水里总结一条高精度必封正则，
// 以 enabled=0、enforce=0 的候选写入 ad_rules，等主管理员在 Mini App
// 里复核、测试、启用。
//
// 设计取舍：
//   - 运行态是**进程内存**、单飞互斥（全局规则只有一份，同时跑两轮会
//     竞态写库并重复烧钱），见 ruleAgentRT；
//   - 模型只从已启用的复判模型列表里挑，且必须绑定 OpenAI 兼容渠道：
//     Eino 的 OpenAI ChatModel 只会说 /chat/completions，Anthropic /
//     Gemini / Cloudflare / Responses 的协议它不认，宁可在 start 时用
//     中文错误说清楚，也不要在半路拿 404；
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
// 这两个是 var 而不是 const：测试要在不真等 180 秒、不真跑满 12 步的
// 前提下覆盖超时与步数耗尽路径。
var (
	// ruleAgentTimeout 是单轮规则发现的总时限。
	ruleAgentTimeout = 180 * time.Second
	// ruleAgentMaxStep 是 ReAct 图的最大执行步数（模型一次 + 工具一次
	// 各算一步，12 步约等于 6 轮工具循环）。
	ruleAgentMaxStep = 12
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
)

// ruleAgentToolNames 是暴露给模型的工具名。两处要用：UnknownToolsHandler
// 的可用清单提示，以及测试断言。
var ruleAgentToolNames = []string{
	"list_banned", "read_record", "find", "test_rule", "create_rule",
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
	mu            sync.Mutex
	running       bool
	startedAt     int64
	finishedAt    int64
	result        string
	errText       string
	createdRuleID int64
	steps         []RuleAgentStep
	seq           int
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
	ruleAgentRT.createdRuleID = 0
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
//	 "error":string,"created_rule_id":int,
//	 "steps":[{"n":int,"at":int,"kind":string,"name":string,"summary":string}]}
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
	return map[string]any{
		"running":         ruleAgentRT.running,
		"started_at":      ruleAgentRT.startedAt,
		"finished_at":     ruleAgentRT.finishedAt,
		"result":          ruleAgentRT.result,
		"error":           errText,
		"created_rule_id": ruleAgentRT.createdRuleID,
		"steps":           steps,
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

// pickRuleAgentModel 从全局复判模型列表（snap.ModelsFor(0)）里挑第一个
// 满足「模型已启用 + 有支持 chat 的上游 + 上游是 OpenAI Completions
// 渠道」的模型。返回模型全名（含上游前缀）与命中的上游。
func pickRuleAgentModel(snap *store.Snapshot) (string, *upstream.Upstream, error) {
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
	return einoopenai.NewChatModel(context.Background(), cfg)
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

	mu            sync.Mutex
	createdRuleID int64
	createFails   int
	lastCreateErr string
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
	created, fails, lastErr := r.createdRuleID, r.createFails, r.lastCreateErr
	r.mu.Unlock()

	ctxErr := r.ctx.Err()
	var result, errText string
	switch {
	case created != 0:
		result = fmt.Sprintf("发现完成：已写入候选规则 #%d（默认未启用、未强制），"+
			"请在必封规则列表里复核测试后决定是否启用。", created)
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
	if created != 0 {
		ruleAgentRT.createdRuleID = created
	}
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
	Limit  int    `json:"limit" jsonschema:"description=返回条数，默认 20，最大 50"`
	Offset int    `json:"offset" jsonschema:"description=OFFSET 分页偏移，默认 0"`
	Kind   string `json:"kind" jsonschema:"description=只看某个广告类型（如 scam/porn），空串表示全部"`
}

// readRecordArgs 是 read_record 的参数。
type readRecordArgs struct {
	ID int64 `json:"id" jsonschema:"required,description=判定流水的 id（来自 list_banned / find 的结果）"`
}

// findArgs 是 find 的参数。
type findArgs struct {
	Pattern string `json:"pattern" jsonschema:"required,description=RE2 正则草稿（宽进：只要能编译就能试跑）"`
	Scope   string `json:"scope" jsonschema:"description=banned（默认）样本优先展示已确认广告并附正常消息参照；all 按时间倒序展示全部命中"`
	Limit   int    `json:"limit" jsonschema:"description=样本条数，默认 20，最大 50"`
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

// buildRuleAgentTools 构造五个只读/候选写入工具。工具返回字符串而不是
// Go error：失败信息要留给模型自我纠正，不该让整个图崩掉。
func buildRuleAgentTools(sh *core.Shared, run *ruleAgentRun) ([]tool.BaseTool, error) {
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
			"fp（命中正常消息/被撤销的误判）、undone、neutral 与误封样本。"+
			"fp 或 undone 大于 0 时绝对不能创建规则。",
		func(ctx context.Context, in testRuleArgs) (string, error) {
			out := run.testRule(ctx, in.Pattern)
			run.toolStep("test_rule", in, out)
			return out, nil
		})
	if err != nil {
		return nil, err
	}

	create, err := toolutils.InferTool("create_rule",
		"创建一条候选必封规则（enabled=0、enforce=0，等主管理员复核）。"+
			"会做严格校验与全库测试；命中任何正常消息（fp>0 或 undone>0）或已存在相同正则都会被拒绝。"+
			"创建成功即结束本轮。",
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
		wrapRuleTool(run, "list_banned", list),
		wrapRuleTool(run, "read_record", read),
		wrapRuleTool(run, "find", find),
		wrapRuleTool(run, "test_rule", test),
		wrapRuleTool(run, "create_rule", create),
	}, nil
}

// ---- 工具实现 ----

// listBanned 返回最近的已确认广告流水摘要。
func (r *ruleAgentRun) listBanned(in listBannedArgs) string {
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
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

// readRecord 返回一条流水的完整信息（正文截断 2000 字）。
func (r *ruleAgentRun) readRecord(id int64) string {
	if id <= 0 {
		return "错误：id 必须为正整数"
	}
	row, ok := LoadAdLog(r.sh.Store, id)
	if !ok {
		return fmt.Sprintf("错误：没有 id=%d 的判定记录", id)
	}
	out, err := json.Marshal(map[string]any{
		"id": row.ID, "bot_id": row.BotID, "chat_id": row.ChatID,
		"user_id": row.UserID, "user_name": row.UserName,
		"message_id": row.MessageID, "verdict": row.Verdict,
		"confidence": row.Confidence, "decider": row.Decider,
		"kind": row.Kind, "action": row.Action, "reason": row.Reason,
		"created_at": row.CreatedAt,
		"text":       core.TruncateRunes(row.Text, ruleAgentReadTextMax),
	})
	if err != nil {
		return "错误：记录序列化失败：" + err.Error()
	}
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
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}

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
	out, _ := json.Marshal(map[string]any{
		"scanned": scanned, "matched": matched,
		"tp": tp, "fp": fp, "undone": undone, "neutral": neutral,
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
			"通过：命中 %d 条已确认广告、0 条正常消息，可以创建。", res.TP)
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
	// 本轮已经创建过规则：直接拒绝。SetReturnDirectly 正常生效时模型
	// 不会再有机会调用，但兜底要有，避免极端情况下一次运行写多条。
	r.mu.Lock()
	createdID := r.createdRuleID
	r.mu.Unlock()
	if createdID != 0 {
		return fmt.Sprintf("本轮已创建规则 #%d，不再重复创建；请直接总结收尾。", createdID)
	}

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
		 created_at,created_by)
		VALUES (?,?,?,?, 'ai', 0, 0, ?,?,?,?,?, ?,?)`,
		name, in.Pattern, category, note,
		res.TP, res.FP, res.Undone, res.Scanned, now, now, r.uid)
	if err != nil {
		return fail("创建失败：写库失败（" + err.Error() + "）。")
	}
	id, _ := result.LastInsertId()

	r.mu.Lock()
	r.createdRuleID = id
	r.mu.Unlock()
	// 实时反映到状态里，面板轮询能看到 created_rule_id。
	ruleAgentRT.mu.Lock()
	ruleAgentRT.createdRuleID = id
	ruleAgentRT.mu.Unlock()

	// 写库后重建快照：虽然候选规则不参与判定（enabled=0），但面板的
	// 规则列表走快照，早一步可见。
	if err := r.sh.Cache.Reload(); err != nil {
		slog.Error("规则发现：候选规则写库后刷新缓存失败", "规则", id, "err", err)
	}
	// 成功即结束本轮，不再让模型继续调工具烧钱。
	_ = react.SetReturnDirectly(ctx)

	msg := fmt.Sprintf("创建成功：候选规则 #%d《%s》已写入（enabled=0、enforce=0，等待主管理员复核）。"+
		"本轮测试：命中广告 %d 条、正常消息 0 条、全库扫描 %d 条。",
		id, name, res.TP, res.Scanned)
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
const ruleAgentSystemPrompt = `你是「门神」反广告系统的规则发现工程师，任务不是聊天，而是从历史判定流水里总结出高精度的「必封正则规则」。

可用语料：antiad_log 判定流水。verdict='ad' 是已确认广告，verdict='clean'/'none' 是正常消息，action='undone' 是被管理员撤销的误判。所有结论必须有流水证据，不得凭想象编造正则。

硬性约束（违反即创建失败）：
1. 正则必须是 Go RE2 语法：不支持环视（(?=...)/(?!...)）、反向引用（\1）、条件表达式等 PCRE 特性；
2. 正则不得匹配空文本（例如 a*、.* 一律不能作为规则，会命中纯图/贴纸消息）；
3. 不得命中任何正常聊天：create_rule 之前必须 test_rule，且 fp=0、undone=0；
4. 优先锚定广告特有的组合形态：联系方式（微信/QQ/telegram/whatsapp 等 + 账号串）、招聘/贷款/博彩等话术组合、引流域名与短链形态；不要用「加微信」「看主页」这类正常聊天也会大量出现的裸词；
5. 每条规则必须可解释：name 说明打击的广告形态，note 写明理由，evidence_ids 填你实际读过或命中的流水 id；
6. 建议用 (?i) 忽略大小写，用边界、字符类与量词收紧匹配；宁可少覆盖一点，也不能误封。

工作流（严格按顺序，不要跳步）：
1. list_banned 查看最近的广告流水，挑出最具代表性的一条；
2. read_record 读完整正文，观察它区别于正常消息的组合特征；
3. find 用正则草稿试跑，看命中什么、误伤什么；反复收缩直到只命中广告；
4. test_rule 用正式口径验证：fp>0 或 undone>0 时绝对不要 create_rule，回到第 3 步改进；
5. create_rule 创建候选规则（默认不启用，等主管理员复核）。创建成功即结束本轮。

如果 create_rule 被拒绝：仔细阅读返回的误封样本，收缩正则后重新 test_rule；连续 3 次失败本轮会被终止。一次运行只需要产出一条最扎实的规则，完成后用一句中文总结：创建了什么、依据哪些证据。`

// ruleAgentUserPrompt 是每轮的启动指令。
const ruleAgentUserPrompt = `请开始一轮规则发现：先查看最近的广告流水，选一个典型形态，按 list_banned → read_record → find → test_rule → create_rule 的流程产出一条高精度候选规则。`
