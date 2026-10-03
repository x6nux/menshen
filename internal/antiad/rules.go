package antiad

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
)

// 规则校验错误。文案直接回给 Mini App，保持中文。
var (
	errRuleEmpty        = errors.New("规则不能为空")
	errRuleTooLong      = fmt.Errorf("规则过长（最多 %d 个字符）", rulePatternMax)
	errRuleMatchesEmpty = errors.New("规则不能匹配空文本（会命中所有消息）")
	// errRuleScanStopped 表示全库扫描被调用方的 context 取消。规则发现
	// Agent 被停止时，不必等整库扫完才结束。
	errRuleScanStopped = errors.New("已停止")
)

// ruleScanCtxCheckEvery 是扫描循环检查 context 的频率（每多少行一次）。
// 1000 行在本地 SQLite 上只是毫秒级，取消延迟足够短，也不至于每行
// 都碰一次 ctx（那会让热循环多一次原子读）。
const ruleScanCtxCheckEvery = 1000

// ---- AI 必封规则：全局候选正则 ----
//
// 规则由 AI 从历史封禁记录里总结（见 T-B 的 Agent），主管理员在 Mini App
// 里测试、启用与强制。三层语义：
//   - enabled=0：完全不参与判定；
//   - enabled=1 且 enforce=0：命中立即删除 + 临时禁言，**跳过 systemone**，
//     规则以 matched_rules 作为强证据直接交大模型复判定案（复判正常会
//     自动解除临时禁言）；没有复判模型时按处置矩阵定案；
//   - enabled=1 且 enforce=1：命中即最高档处置，不花 AI 的钱。
//
// 防误封：任何保存都先跑全库测试（TestRulePattern），enforce 只允许在
// 最近一轮测试 last_fp=0 且 last_undone=0 时打开（服务端强制）。

// rulePatternMax 是规则正则的长度上限（字符数）。超长正则既难读，
// 也可能是灾难性回溯的温床，直接在写入口拒掉。
const rulePatternMax = 500

// ruleScanLimit 是单轮全库测试最多扫描的流水条数。防误封只需要覆盖足够的
// 历史，而不是无限翻旧账；100000 条足以覆盖任何正常群的判定历史。
const ruleScanLimit = 100000

// 测试样本的上限与单条正文的截断长度。样本是给人复核的：够了就行，
// 不把整库内容搬进一次 HTTP 响应。
const (
	ruleTPSamples     = 30
	ruleFPSamples     = 30
	ruleUndoneSamples = 10
	ruleSampleTextMax = 200
)

// MatchRules 返回所有「已启用且正则在 text 中命中」的规则，按 id 升序。
//
// 只做纯内存匹配：enforce 规则的门在同步段上，不能有数据库往返。
// Re 为 nil 的行不会进快照（编译失败已在加载时跳过）。
func MatchRules(snap *store.Snapshot, text string) []store.AdRuleRec {
	if snap == nil {
		return nil
	}
	var hits []store.AdRuleRec
	for _, r := range snap.AdRules {
		if !r.Enabled || r.Re == nil {
			continue
		}
		if r.Re.MatchString(text) {
			hits = append(hits, r)
		}
	}
	// 快照按 id 升序载入，这里再显式排一次：顺序是 API 契约的一部分，
	// 不该依赖加载实现。
	sort.Slice(hits, func(i, j int) bool { return hits[i].ID < hits[j].ID })
	return hits
}

// MatchedRule 是一条命中的**非强制**必封规则，作为强证据注入判定载荷
// （adState.MatchedRules）。enforce 规则不进这里：命中即处置，轮不到 AI。
type MatchedRule struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category,omitempty"`
	Note     string `json:"note,omitempty"`
}

// MatchedRuleInfos 返回命中的非 enforce 规则证据；没有命中返回 nil。
//
// 与 known_ad_patterns（形态摘要，提示词明确说它只是参考、不是判决）不同：
// 这些是主管理员启用、且通过全库零误封测试的高精度规则，提示词要求模型
// 按**强证据**对待；但规则是纯模式匹配、不看语境，模型仍可结合上下文
// 推翻（引用他人广告做批评/警示、正常讨论里恰好同形等）。
func MatchedRuleInfos(snap *store.Snapshot, text string) []MatchedRule {
	var out []MatchedRule
	for _, r := range MatchRules(snap, text) {
		if r.Enforce {
			continue
		}
		out = append(out, MatchedRule{
			ID: r.ID, Name: r.Name, Category: r.Category, Note: r.Note,
		})
	}
	return out
}

// matchedRuleIDs 取证据规则的 id 列表（日志用）。
func matchedRuleIDs(rules []MatchedRule) []int64 {
	out := make([]int64, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.ID)
	}
	return out
}

// ruleVerdict 把命中的非强制规则合成规则级初判：置信度取 1（规则在启用
// 前经过全库零误封测试），分类取规则的分类，decider 用 rule:<id> 以便
// 流水识别来源。多条命中时以最老的一条为准（MatchRules 已按 id 升序），
// 理由里列出全部规则名。
func ruleVerdict(rules []MatchedRule) adVerdict {
	names := make([]string, 0, len(rules))
	for _, r := range rules {
		names = append(names, "《"+r.Name+"》")
	}
	return adVerdict{
		IsAd: true, Confidence: 1, Kind: rules[0].Category, Scope: "message",
		Decider: "rule:" + fmt.Sprintf("%d", rules[0].ID),
		Reason:  "命中必封规则" + strings.Join(names, "、"),
	}
}

// BumpRuleHits 记一次规则命中（计数 + 最近命中时刻）。
//
// 失败只记 Warn 不影响判定：计数是给管理员看的运营数据，不能反过来
// 让一条已经确定要处置的消息晚到或被放行。
func BumpRuleHits(sh *core.Shared, id int64) {
	if sh == nil || id == 0 {
		return
	}
	if _, err := sh.Store.Write.Exec(`UPDATE ad_rules
		SET hits = hits + 1, last_matched = ? WHERE id = ?`,
		time.Now().Unix(), id); err != nil {
		slog.Warn("反广告：必封规则命中计数失败", "rule", id, "err", err)
	}
}

// RuleSample 是全库测试里的一条命中样本（正文已截断）。
type RuleSample struct {
	ID        int64
	Verdict   string
	Action    string
	Kind      string
	UserID    int64
	ChatID    int64
	CreatedAt int64
	Text      string
}

// RuleTestResult 是一条规则跑全库测试的结果。
//
// 分类口径（见 TestRulePattern）：
//   - TP：verdict='ad' 的历史判定；
//   - FP：verdict in ('clean','none')，以及 action='undone'（被撤销的误判）；
//   - Undone：action='undone'，同时计入 FP —— 它本来就是被判错后撤销的；
//   - Neutral：verdict in ('skipped','error')，当时没判出结论，不算误封，
//     但要让管理员看见命中里有多少是这类。
//
// TP+FP+Neutral = Matched（未列入口径的 verdict 只计 Matched，不分类）。
type RuleTestResult struct {
	Pattern string
	Scanned int64
	Matched int64
	TP      int64
	FP      int64
	Undone  int64
	Neutral int64

	// AdsTotal 是扫描窗口内已确认广告总数（verdict='ad' 且
	// action<>'undone'），覆盖率分母；Kinds 是同一批按 ad_kind 的细分。
	AdsTotal int64
	Kinds    []RuleKindStat

	TPSamples     []RuleSample
	FPSamples     []RuleSample
	UndoneSamples []RuleSample
}

// RuleKindStat 是按 ad_kind 细分的覆盖率：Matched/Total 是该类型的召回。
type RuleKindStat struct {
	Kind    string `json:"kind"`
	Total   int64  `json:"total"`
	Matched int64  `json:"matched"`
}

// Coverage 返回该类型的覆盖率；Total=0 时 0。
func (s RuleKindStat) Coverage() float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.Matched) / float64(s.Total)
}

// Coverage 返回总体覆盖率：命中的已确认广告 ÷ 已确认广告总数。
func (r RuleTestResult) Coverage() float64 {
	if r.AdsTotal == 0 {
		return 0
	}
	return float64(r.TP) / float64(r.AdsTotal)
}

// CompileRulePattern 是**写入口**的严格校验：空串、超长、编译失败，
// 以及能匹配空文本的规则一律拒绝。
//
// 最后一类是致命的：群里的纯图/贴纸消息正文为空，一个 `a*` 这样的规则
// 会命中所有消息，enforce 打开后整群被最高档处置。`test` 试跑仍走宽松的
// compileRuleLenient —— 人工试跑这类草稿正是它的用途。
//
// T-B 的 create_rule 也复用这个函数，保证 AI 写库的规则同样过这道闸。
func CompileRulePattern(pattern string) (*regexp.Regexp, error) {
	re, err := compileRuleLenient(pattern)
	if err != nil {
		return nil, err
	}
	if re.MatchString("") {
		return nil, errRuleMatchesEmpty
	}
	return re, nil
}

// compileRuleLenient 只做全库测试需要的基础校验：空串、超长、可编译。
// 刻意不拒绝「匹配空文本」——人工试跑一条 `a*` 看它命中什么是合理需求。
func compileRuleLenient(pattern string) (*regexp.Regexp, error) {
	if strings.TrimSpace(pattern) == "" {
		return nil, errRuleEmpty
	}
	if len([]rune(pattern)) > rulePatternMax {
		return nil, errRuleTooLong
	}
	return regexp.Compile(pattern)
}

// ruleKindAcc 在扫描循环里累计覆盖率数据；testRulePatternCtx 与
// ruleAgentRun.findMatches 共用，保证两边口径与数字完全一致。
type ruleKindAcc struct {
	adsTotal int64
	totals   map[string]int64
	matched  map[string]int64
}

func newRuleKindAcc() *ruleKindAcc {
	return &ruleKindAcc{totals: map[string]int64{}, matched: map[string]int64{}}
}

// ad 记一条已确认广告（分母 + 类型分母）。
func (a *ruleKindAcc) ad(kind string) {
	a.adsTotal++
	a.totals[kind]++
}

// hit 记一条命中且属于已确认广告的行（TP）。
func (a *ruleKindAcc) hit(kind string) { a.matched[kind]++ }

// result 返回分母与类型切片：Total 降序、同数按 Kind 升序。
func (a *ruleKindAcc) result() (int64, []RuleKindStat) {
	kinds := make([]RuleKindStat, 0, len(a.totals))
	for kind, total := range a.totals {
		kinds = append(kinds, RuleKindStat{Kind: kind, Total: total, Matched: a.matched[kind]})
	}
	sort.Slice(kinds, func(i, j int) bool {
		if kinds[i].Total != kinds[j].Total {
			return kinds[i].Total > kinds[j].Total
		}
		return kinds[i].Kind < kinds[j].Kind
	})
	return a.adsTotal, kinds
}

// RuleKindsJSON 把类型细分序列化成落库/契约 JSON；空或失败返回空串。
// 面板写回与 Agent 创建规则共用，保证两处落库格式一致。
func RuleKindsJSON(kinds []RuleKindStat) string {
	if len(kinds) == 0 {
		return ""
	}
	b, err := json.Marshal(kinds)
	if err != nil {
		return ""
	}
	return string(b)
}

// ParseRuleKindsJSON 解析落库的 last_kinds；空/坏数据返回空切片（非 nil），
// 面板列表与 Agent 的 list_rules 共用，避免两处解析口径漂移。
func ParseRuleKindsJSON(raw string) []map[string]any {
	if raw == "" {
		return []map[string]any{}
	}
	var kinds []RuleKindStat
	if err := json.Unmarshal([]byte(raw), &kinds); err != nil || kinds == nil {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, map[string]any{"kind": k.Kind, "total": k.Total, "matched": k.Matched})
	}
	return out
}

// TestRulePattern 在 antiad_log 全量历史上试跑一条正则，统计它会命中
// 多少已确认广告、多少正常消息（含被撤销的误判）。
//
// 这是防误封的唯一依据：保存规则与打开 enforce 之前都要跑它。
//
// 语料不再按 text 非空过滤：空文本（纯图/贴纸）同样会走到判定门，
// 匹配空文本的规则造成的误封恰恰只能在这里看见 —— 让它在测试结果里
// 显式暴露（FP/样本），而不是悄悄漏出测试口径之外。
//
// 盲区：语料是 antiad_log 里**落库截断后**的文本（adTextLimit=1000 rune），
// 而判定门匹配的是消息完整正文。超过 1000 rune 的长消息，其尾部特征在
// 这里看不见：一条只依赖被截掉尾部的规则可能测出 fp=0 却在线上误封。
// 所以规则应尽量锚定出现在前 1000 字内的形态组合。
func TestRulePattern(sh *core.Shared, pattern string) (RuleTestResult, error) {
	// 无取消需求的老入口：面板、保存流程都走这里。
	return testRulePatternCtx(context.Background(), sh, pattern)
}

// testRulePatternCtx 是 TestRulePattern 的可取消版本：扫描每
// ruleScanCtxCheckEvery 行检查一次 ctx，取消时返回 errRuleScanStopped。
// 规则发现 Agent 用它在 stop 后立刻收尾，不等整库扫完。
func testRulePatternCtx(ctx context.Context, sh *core.Shared,
	pattern string) (RuleTestResult, error) {

	res := RuleTestResult{Pattern: pattern}
	acc := newRuleKindAcc()
	re, err := compileRuleLenient(pattern)
	if err != nil {
		return res, err
	}

	rows, err := sh.Store.Read.Query(`SELECT id,verdict,action,ad_kind,user_id,
		chat_id,created_at,text FROM antiad_log
		ORDER BY id DESC LIMIT ?`, ruleScanLimit)
	if err != nil {
		return res, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			s               RuleSample
			text            string
			verdict, action string
		)
		if err := rows.Scan(&s.ID, &verdict, &action, &s.Kind, &s.UserID,
			&s.ChatID, &s.CreatedAt, &text); err != nil {
			return res, err
		}
		res.Scanned++
		if res.Scanned%ruleScanCtxCheckEvery == 0 && ctx.Err() != nil {
			return res, errRuleScanStopped
		}
		// 已确认广告：计入覆盖率分母与类型分母（与 TP 口径逐条一致）。
		if verdict == "ad" && action != "undone" {
			acc.ad(s.Kind)
		}
		if !re.MatchString(text) {
			continue
		}
		res.Matched++
		s.Verdict, s.Action = verdict, action
		s.Text = core.TruncateRunes(text, ruleSampleTextMax)

		switch {
		case action == "undone":
			// 被撤销的处罚 = 当初判错了，是最重要的误封证据。
			res.Undone++
			res.FP++
			addRuleSample(&res.FPSamples, s, ruleFPSamples)
			addRuleSample(&res.UndoneSamples, s, ruleUndoneSamples)
		case verdict == "ad":
			res.TP++
			acc.hit(s.Kind)
			addRuleSample(&res.TPSamples, s, ruleTPSamples)
		case verdict == "clean" || verdict == "none":
			res.FP++
			addRuleSample(&res.FPSamples, s, ruleFPSamples)
		case verdict == "skipped" || verdict == "error":
			// 没送检或判定失败，不构成「误封」证据，但要在结果里可见。
			res.Neutral++
		}
	}
	if err := rows.Err(); err != nil {
		return res, err
	}
	res.AdsTotal, res.Kinds = acc.result()
	return res, nil
}

func addRuleSample(dst *[]RuleSample, s RuleSample, max int) {
	if len(*dst) < max {
		*dst = append(*dst, s)
	}
}

// firstEnforcedRule 返回命中列表里第一条 enforce 规则（列表已按 id 升序）。
// 多条 enforce 同时命中时取最老的一条：顺序稳定，结果可解释。
func firstEnforcedRule(hits []store.AdRuleRec) (store.AdRuleRec, bool) {
	for _, r := range hits {
		if r.Enforce {
			return r, true
		}
	}
	return store.AdRuleRec{}, false
}
