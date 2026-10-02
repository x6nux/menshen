package antiad

import (
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
	errRuleEmpty   = errors.New("规则不能为空")
	errRuleTooLong = fmt.Errorf("规则过长（最多 %d 个字符）", rulePatternMax)
)

// ---- AI 必封规则：全局候选正则 ----
//
// 规则由 AI 从历史封禁记录里总结（见 T-B 的 Agent），主管理员在 Mini App
// 里测试、启用与强制。三层语义：
//   - enabled=0：完全不参与判定；
//   - enabled=1 且 enforce=0：命中只作为证据注入判定 prompt（见 RuleHintText）；
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

// RuleHintText 把命中的**非强制**规则拼成一行提示，注入判定 prompt
// （buildState 的 known_ad_patterns）。没有命中时返回空串。
//
// enforce 规则不进提示：调用方在命中 enforce 规则时已经终止判定，
// 轮不到 prompt；万一两者同时命中，处置优先，这条不重复描述。
func RuleHintText(snap *store.Snapshot, text string) string {
	var sb strings.Builder
	for _, r := range MatchRules(snap, text) {
		if r.Enforce {
			continue
		}
		sb.WriteString("命中必封规则《")
		sb.WriteString(r.Name)
		sb.WriteString("》")
		if note := strings.TrimSpace(r.Note); note != "" {
			sb.WriteString("（")
			sb.WriteString(note)
			sb.WriteString("）")
		}
		sb.WriteString("；")
	}
	return sb.String()
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

	TPSamples     []RuleSample
	FPSamples     []RuleSample
	UndoneSamples []RuleSample
}

// TestRulePattern 在 antiad_log 全量历史上试跑一条正则，统计它会命中
// 多少已确认广告、多少正常消息（含被撤销的误判）。
//
// 这是防误封的唯一依据：保存规则与打开 enforce 之前都要跑它。
func TestRulePattern(sh *core.Shared, pattern string) (RuleTestResult, error) {
	res := RuleTestResult{Pattern: pattern}
	if strings.TrimSpace(pattern) == "" {
		return res, errRuleEmpty
	}
	if len([]rune(pattern)) > rulePatternMax {
		return res, errRuleTooLong
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return res, err
	}

	rows, err := sh.Store.Read.Query(`SELECT id,verdict,action,ad_kind,user_id,
		chat_id,created_at,text FROM antiad_log
		WHERE text != '' ORDER BY id DESC LIMIT ?`, ruleScanLimit)
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
