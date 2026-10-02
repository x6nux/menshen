# 规则发现 v2 实现计划（一轮多条 + 覆盖率测试工具）

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 AI 规则发现一轮产出多条候选规则，Agent 能读更多语料并拿到覆盖率，Mini App 提供任意正则试跑与覆盖率展示。

**Architecture:** 覆盖率在现有全库扫描循环里聚合（`ruleKindAcc`，`testRulePatternCtx` 与 `findMatches` 共用）；Agent 工具集从 5 个扩到 8 个并放开读取限额，`create_rule` 不再收尾；覆盖率落 `ad_rules` 两列供列表/详情展示；前端复用现有 `test` 接口做试跑抽屉。

**Tech Stack:** Go（Eino ReAct、modernc sqlite）、React 19 + MUI + TanStack Query + vitest/MSW。

**规格：** `docs/superpowers/specs/2026-10-02-rule-discovery-v2-design.md`（已评审通过）

---

## 文件结构

| 文件 | 职责 | 改动 |
|---|---|---|
| `internal/antiad/rules.go` | 规则编译/测试/覆盖率聚合 | `RuleKindStat`、`RuleTestResult.AdsTotal/Kinds/Coverage()`、`ruleKindAcc` |
| `internal/antiad/rules_test.go` | 覆盖率单测 | 新增用例 + 分类型插入 helper |
| `internal/antiad/ruleagent.go` | Agent 工具、运行态、提示词 | 8 工具、限额、多创建、状态契约、提示词 |
| `internal/antiad/ruleagent_test.go` | Agent 端到端（假上游） | 多创建、新工具、覆盖率、`[]` 契约 |
| `internal/store/db.go` | 表结构与迁移 | `ad_rules` 两列 |
| `internal/store/db_test.go` | 老库迁移 | 老形状 `ad_rules` + 断言 |
| `internal/panel/miniapp.go` | Mini App 契约 | test/list JSON、写回新列 |
| `internal/panel/miniapp_test.go` | 面板契约 | 覆盖率字段与落库断言 |
| `web/src/api/types.ts` | 前端类型 | Rule/RuleTest/RuleAgent 新字段 |
| `web/src/pages/RulesPage.tsx` | 规则页 | 测试抽屉、覆盖率、多条展示 |
| `web/src/pages/RulesPage.test.tsx` | 前端测试 | 新行为用例 |
| `web/src/mocks/fixtures.ts` / `handlers.ts` | mock | 新字段 |
| `docs/panel.md` | 文档 | AI 必封规则一节 |

---

## Chunk 1: 覆盖率引擎与持久化

### Task 1: rules.go 覆盖率聚合

**Files:**
- Modify: `internal/antiad/rules.go`
- Test: `internal/antiad/rules_test.go`

- [ ] **Step 1: 写失败测试**

在 `rules_test.go` 加一个按类型插入的 helper（现有 `insertLog` 硬编码 `ad_kind='promo'`，不要改它以免动到大量调用点）：

```go
// insertKindLog 与 insertLog 相同，但可指定 ad_kind。
func insertKindLog(t *testing.T, b *core.Bot, verdict, action, kind, text string) {
	t.Helper()
	if _, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,created_at,bot_id)
		VALUES (-100,555,0,?,?,0.9,'test',?,?,'',?,?)`,
		text, verdict, kind, action, time.Now().Unix(), b.BotID()); err != nil {
		t.Fatalf("插入流水失败: %v", err)
	}
}

// TestRuleCoverageByKind 锁覆盖率口径：分母是 verdict='ad' 且 action<>'undone'；
// 按 ad_kind 分组；undone/clean 不进分母；空库 coverage=0。
func TestRuleCoverageByKind(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	insertKindLog(t, b, "ad", "deleted", "scam", "办理贷款加微信")
	insertKindLog(t, b, "ad", "deleted", "scam", "办理贷款加微信")
	insertKindLog(t, b, "ad", "deleted", "scam", "今天天气不错")
	insertKindLog(t, b, "ad", "deleted", "promo", "限时促销")
	insertKindLog(t, b, "ad", "undone", "scam", "办理贷款（撤销）")
	insertKindLog(t, b, "clean", "none", "scam", "办理贷款（正常聊天）")

	res, err := TestRulePattern(b.Shared, "办理贷款")
	if err != nil {
		t.Fatal(err)
	}
	if res.AdsTotal != 4 || res.TP != 2 {
		t.Fatalf("覆盖率分子分母不对：AdsTotal=%d TP=%d", res.AdsTotal, res.TP)
	}
	if got := res.Coverage(); got != 0.5 {
		t.Errorf("总体覆盖率应为 0.5，得到 %v", got)
	}
	want := []RuleKindStat{
		{Kind: "scam", Total: 3, Matched: 2},
		{Kind: "promo", Total: 1, Matched: 0},
	}
	if len(res.Kinds) != len(want) {
		t.Fatalf("类型数应为 %d，得到 %+v", len(want), res.Kinds)
	}
	for i := range want {
		if res.Kinds[i] != want[i] {
			t.Errorf("类型[%d] 应为 %+v，得到 %+v", i, want[i], res.Kinds[i])
		}
	}
	if got := res.Kinds[1].Coverage(); got != 0 {
		t.Errorf("promo 覆盖率应为 0，得到 %v", got)
	}

	// 空库：不除零。
	b2, _ := testutil.NewTestBot(t, 2)
	empty, err := TestRulePattern(b2.Shared, "办理贷款")
	if err != nil {
		t.Fatal(err)
	}
	if empty.AdsTotal != 0 || empty.Coverage() != 0 || len(empty.Kinds) != 0 {
		t.Errorf("空库应 AdsTotal=0 coverage=0 kinds 空，得到 %+v", empty)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/antiad -run TestRuleCoverageByKind -v`
Expected: 编译失败（`undefined: RuleKindStat` / `res.AdsTotal`）。

- [ ] **Step 3: 实现**

`rules.go` 在 `RuleTestResult` 定义前后加入：

```go
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
```

`RuleTestResult` 追加字段（放在 `Neutral` 后）：

```go
	// AdsTotal 是扫描窗口内已确认广告总数（verdict='ad' 且
	// action<>'undone'），覆盖率分母；Kinds 是同一批按 ad_kind 的细分。
	AdsTotal int64
	Kinds    []RuleKindStat
```

同文件加：

```go
// Coverage 返回总体覆盖率：命中的已确认广告 ÷ 已确认广告总数。
func (r RuleTestResult) Coverage() float64 {
	if r.AdsTotal == 0 {
		return 0
	}
	return float64(r.TP) / float64(r.AdsTotal)
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
```

（`rules.go` 需要新增 `encoding/json` import。）

改 `testRulePatternCtx`：

1. `res := RuleTestResult{Pattern: pattern}` 后加 `acc := newRuleKindAcc()`。
2. 每行扫描、ctx 检查之后、`if !re.MatchString(text)` 之前：

```go
		// 已确认广告：计入覆盖率分母与类型分母（与 TP 口径逐条一致）。
		if verdict == "ad" && action != "undone" {
			acc.ad(s.Kind)
		}
```

3. `case verdict == "ad":` 分支里（`res.TP++` 后）加 `acc.hit(s.Kind)`。
4. `rows.Err()` 检查之后、`return res, nil` 之前：`res.AdsTotal, res.Kinds = acc.result()`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/antiad -run 'TestRuleCoverageByKind|TestRuleTestPatternClassification' -v`
Expected: PASS（既有分类测试不受影响）。

- [ ] **Step 5: 提交**

```bash
git add internal/antiad/rules.go internal/antiad/rules_test.go
git commit -m "antiad: 规则测试增加总体/按类型覆盖率聚合"
```

### Task 2: ad_rules 两列（schema + migrate）

**Files:**
- Modify: `internal/store/db.go`
- Test: `internal/store/db_test.go`

- [ ] **Step 1: 写失败测试**

`db_test.go` 的 `TestMigrateOldDB` 里，在造老库的 SQL 列表中加入老形状 `ad_rules`（没有新列）：

```go
		// 老形状的 ad_rules：没有 last_ads_total / last_kinds（migrate 才补）。
		`CREATE TABLE ad_rules (id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL, pattern TEXT NOT NULL,
			category TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT 'ai', enabled INTEGER NOT NULL DEFAULT 0,
			enforce INTEGER NOT NULL DEFAULT 0, hits INTEGER NOT NULL DEFAULT 0,
			last_matched INTEGER NOT NULL DEFAULT 0, last_tp INTEGER NOT NULL DEFAULT 0,
			last_fp INTEGER NOT NULL DEFAULT 0, last_undone INTEGER NOT NULL DEFAULT 0,
			last_scanned INTEGER NOT NULL DEFAULT 0,
			last_tested_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT 0,
			created_by INTEGER NOT NULL DEFAULT 0)`,
		`INSERT INTO ad_rules (name,pattern) VALUES ('old','old')`,
```

并把断言列表扩成：

```go
	for _, c := range [][2]string{{"bot_chats", "punish"},
		{"group_members", "whitelisted"}, {"group_messages", "media_group"},
		{"bots", "is_main"}, {"upstreams", "kind"},
		{"ad_rules", "last_ads_total"}, {"ad_rules", "last_kinds"}} {
```

再补老行默认值断言：

```go
	var adsTotal int64
	var kinds string
	if err := s.Read.QueryRow(`SELECT last_ads_total,last_kinds FROM ad_rules
		WHERE name='old'`).Scan(&adsTotal, &kinds); err != nil {
		t.Fatalf("读老规则新列失败: %v", err)
	}
	if adsTotal != 0 || kinds != "" {
		t.Errorf("老规则新列默认应为 0/空串，得到 %d/%q", adsTotal, kinds)
	}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/store -run TestMigrateOldDB -v`
Expected: FAIL（`ad_rules.last_ads_total` 没有补上 / 查询 no such column）。

- [ ] **Step 3: 实现**

`schemaSQL` 的 `ad_rules` 建表里，`last_undone` 之后加：

```sql
  last_ads_total INTEGER NOT NULL DEFAULT 0,
  last_kinds     TEXT    NOT NULL DEFAULT '',
```

`migrate()` 的列清单末尾加：

```go
		// last_ads_total / last_kinds：最近一轮全库测试的覆盖率分母与按
		// 类型细分（JSON）。老库默认 0/空串，前端按「未测过覆盖率」展示。
		{"ad_rules", "last_ads_total", "INTEGER NOT NULL DEFAULT 0"},
		{"ad_rules", "last_kinds", "TEXT NOT NULL DEFAULT ''"},
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/store -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/store/db.go internal/store/db_test.go
git commit -m "store: ad_rules 增加最近测试的覆盖率分母与类型细分列"
```

### Task 3: 面板契约（test/list JSON + 写回）

**Files:**
- Modify: `internal/panel/miniapp.go`
- Test: `internal/panel/miniapp_test.go`

- [ ] **Step 1: 写失败测试**

在 `miniapp_test.go` 的 `TestMiniRulesCRUD` 里，把覆盖率断言放在**重跑测试之后**
（那一轮 `tp=1/fp=0`，此时库里只有 1 条已确认广告；第一次保存时 tp=0、分母为 0）：

重跑测试响应（现有断言 `test["fp"]==0 || test["tp"]==1` 的旁边）：

```go
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
```

列表字段循环加入 `"last_ads_total", "last_kinds"`，并断言：

```go
	if row["last_ads_total"].(float64) != 1 {
		t.Errorf("列表应带覆盖率分母：%v", row["last_ads_total"])
	}
```

落库断言（读库，同样放在重跑之后）：

```go
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
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/panel -run TestMiniRules -v`
Expected: FAIL（缺字段）。

- [ ] **Step 3: 实现**

`miniRuleWriteTest` 改为：

```go
func miniRuleWriteTest(sh *core.Shared, id int64, r antiad.RuleTestResult) error {
	_, err := sh.Store.Write.Exec(`UPDATE ad_rules
		SET last_tested_at=?,last_scanned=?,last_tp=?,last_fp=?,last_undone=?,
		    last_ads_total=?,last_kinds=?
		WHERE id=?`, time.Now().Unix(), r.Scanned, r.TP, r.FP, r.Undone,
		r.AdsTotal, antiad.RuleKindsJSON(r.Kinds), id)
	return err
}

// parseRuleKinds 解析落库 JSON；空/坏数据按空数组处理。
func parseRuleKinds(raw string) []map[string]any {
	if raw == "" {
		return []map[string]any{}
	}
	var kinds []antiad.RuleKindStat
	if err := json.Unmarshal([]byte(raw), &kinds); err != nil || kinds == nil {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, map[string]any{"kind": k.Kind, "total": k.Total, "matched": k.Matched})
	}
	return out
}
```

`miniRuleTestJSON` 返回里加：

```go
	kinds := make([]map[string]any, 0, len(r.Kinds))
	for _, k := range r.Kinds {
		kinds = append(kinds, map[string]any{
			"kind": k.Kind, "total": k.Total, "matched": k.Matched,
			"coverage": k.Coverage(),
		})
	}
	return map[string]any{
		"pattern": r.Pattern, "scanned": r.Scanned, "matched": r.Matched,
		"tp": r.TP, "fp": r.FP, "undone": r.Undone, "neutral": r.Neutral,
		"ads_total": r.AdsTotal, "coverage": r.Coverage(), "kinds": kinds,
		"tp_samples":     samples(r.TPSamples),
		"fp_samples":     samples(r.FPSamples),
		"undone_samples": samples(r.UndoneSamples),
	}
```

`miniRuleList`：SELECT 列尾加 `,last_ads_total,last_kinds`，扫描变量加 `lastAdsTotal int64, lastKinds string`，返回 map 加：

```go
			"last_ads_total": lastAdsTotal,
			"last_kinds":     parseRuleKinds(lastKinds),
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/panel -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/panel/miniapp.go internal/panel/miniapp_test.go
git commit -m "panel: 规则测试/列表契约带覆盖率，写回两列"
```

---

## Chunk 2: Agent 一轮多条 + 多读

### Task 4: find / test_rule 覆盖率输出与读取限额

**Files:**
- Modify: `internal/antiad/ruleagent.go`
- Test: `internal/antiad/ruleagent_test.go`

- [ ] **Step 1: 写失败测试**

在 `ruleagent_test.go` 的 `TestRuleAgentFindAlignsWithTestPattern`（现有 find/正式
测试口径对齐用例）里，把匿名结构体加三个字段并断言：

```go
	var got struct {
		Scanned   int64             `json:"scanned"`
		Matched   int64             `json:"matched"`
		TP        int64             `json:"tp"`
		FP        int64             `json:"fp"`
		Undone    int64             `json:"undone"`
		Neutral   int64             `json:"neutral"`
		AdsTotal  int64             `json:"ads_total"`
		Coverage  float64           `json:"coverage"`
		Kinds     []RuleKindStat    `json:"kinds"`
		ByVerdict map[string]int64  `json:"by_verdict"`
		Scope     string            `json:"scope"`
		Samples   []ruleAgentSample `json:"samples"`
	}
	// ...现有计数断言之后追加：
	if got.AdsTotal != ref.AdsTotal || got.Coverage != round4(ref.Coverage()) {
		t.Errorf("find 覆盖率与 TestRulePattern 不一致：find=%d/%v ref=%d/%v",
			got.AdsTotal, got.Coverage, ref.AdsTotal, ref.Coverage())
	}
	if len(got.Kinds) != len(ref.Kinds) {
		t.Errorf("find kinds 数量不一致：%v vs %+v", got.Kinds, ref.Kinds)
	}
```

该语料里已确认广告 2 条（两条 `ad/deleted`）、命中 1 条 → `AdsTotal=2`、
`Coverage=0.5`、`Kinds=[{scam,2,1}]`；补一条显式断言锁死：

```go
	if got.AdsTotal != 2 || got.Coverage != 0.5 {
		t.Errorf("覆盖率不对：ads=%d cov=%v", got.AdsTotal, got.Coverage)
	}
```

再加两条：`test_rule` 输出带覆盖率；覆盖率**不是**创建门槛（低覆盖但 fp=0 仍可创建）。

```go
// TestRuleAgentTestRuleOutputsCoverage：test_rule 返回 ads_total/coverage/kinds，
// 且低覆盖率不阻断 can_create（覆盖率只展示）。
func TestRuleAgentTestRuleOutputsCoverage(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	insertRuleAgentLog(t, b, "办理贷款加微信", "ad", "deleted", "scam")
	for i := 0; i < 50; i++ {
		insertRuleAgentLog(t, b, "正常聊天", "ad", "deleted", "promo")
	}
	run := &ruleAgentRun{sh: b.Shared, ctx: context.Background()}

	var out map[string]any
	if err := json.Unmarshal([]byte(run.testRule(context.Background(), "办理贷款")), &out); err != nil {
		t.Fatal(err)
	}
	if out["ads_total"].(float64) != 51 || out["can_create"] != true {
		t.Fatalf("低覆盖也应可创建：%v", out)
	}
	if got := out["coverage"].(float64); got < 0.019 || got > 0.020 {
		t.Errorf("覆盖率应为 1/51≈0.0196，得到 %v", got)
	}
	if len(out["kinds"].([]any)) != 2 {
		t.Errorf("kinds 应含 scam 与 promo：%v", out["kinds"])
	}
	// 创建入口也不看覆盖率：低覆盖 + fp=0 仍能创建。
	if got := run.createRule(context.Background(), createRuleArgs{
		Name: "低覆盖", Pattern: "办理贷款"}); !strings.Contains(got, "创建成功") {
		t.Errorf("低覆盖不应阻断创建：%q", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/antiad -run 'TestRuleAgentFindAlignsWithTestPattern|TestRuleAgentTestRuleOutputsCoverage' -v`
Expected: FAIL（缺 `ads_total` 等键）。

- [ ] **Step 3: 实现**

`ruleagent.go` 常量区加：

```go
	// ruleAgentListBannedDefault/Max：list_banned 的默认与上限条数。
	ruleAgentListBannedDefault = 50
	ruleAgentListBannedMax     = 200
	// ruleAgentFindDefault/Max：find 的默认与上限样本条数。
	ruleAgentFindDefault = 50
	ruleAgentFindMax     = 200
```

加两个 helper（放在 `findMatches` 之前）：

```go
// round4 把覆盖率四舍五入到 4 位小数：给模型的 JSON 省 token。
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

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
```

`findMatches`：

- 限额改成 `ruleAgentFindDefault` / `ruleAgentFindMax`。
- 扫描循环加 `acc := newRuleKindAcc()`；每行在 `scanned++` 与 ctx 检查之后：

```go
		if verdict == "ad" && action != "undone" {
			acc.ad(s.Kind)
		}
```

- `case verdict == "ad":` 分支 `tp++` 后加 `acc.hit(s.Kind)`（`action=="undone"` 已在前一 case 拦截）。
- 输出 map 加：

```go
	adsTotal, kinds := acc.result()
	out, _ := json.Marshal(map[string]any{
		"scanned": scanned, "matched": matched,
		"tp": tp, "fp": fp, "undone": undone, "neutral": neutral,
		"ads_total": adsTotal, "coverage": round4(ruleCoverage(tp, adsTotal)),
		"kinds": ruleKindsAgentJSON(kinds),
		"by_verdict": byVerdict, "scope": scope, "samples": samples,
	})
```

```go
// ruleCoverage 计算覆盖率；分母为 0 时 0。
func ruleCoverage(matched, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(matched) / float64(total)
}
```

- `listBanned` 限额改 `ruleAgentListBannedDefault` / `ruleAgentListBannedMax`。
- 同步更新参数描述（否则模型看到的还是旧限额）：
  `listBannedArgs.Limit` → `description=返回条数，默认 50，最大 200`；
  `findArgs.Limit` → `description=样本条数，默认 50，最大 200`。
- `testRule` 输出加 `ads_total/coverage/kinds`：

```go
	out := map[string]any{
		"scanned": res.Scanned, "matched": res.Matched,
		"tp": res.TP, "fp": res.FP, "undone": res.Undone, "neutral": res.Neutral,
		"ads_total": res.AdsTotal, "coverage": round4(res.Coverage()),
		"kinds":          ruleKindsAgentJSON(res.Kinds),
		"fp_samples":     ruleSamplesJSON(res.FPSamples, 10),
		"undone_samples": ruleSamplesJSON(res.UndoneSamples, 5),
	}
```

  并把通过分支的 message 改为：

```go
	default:
		out["can_create"] = true
		out["message"] = fmt.Sprintf(
			"通过：命中 %d 条已确认广告（覆盖率 %.1f%%）、0 条正常消息，可以创建。",
			res.TP, res.Coverage()*100)
	}
```

（`Matched==0` 分支文案保持，不改。）
- 补 import：`math`（round4）、`sort`（listKinds）、`slices`（listRules 反转）。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/antiad -run 'TestRuleAgent' -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/antiad/ruleagent.go internal/antiad/ruleagent_test.go
git commit -m "antiad: find/test_rule 输出覆盖率并放开读取限额"
```

### Task 5: 新工具 list_kinds / list_rules / read_records

**Files:**
- Modify: `internal/antiad/ruleagent.go`
- Test: `internal/antiad/ruleagent_test.go`

- [ ] **Step 1: 写失败测试**

加两个直调测试（不经过假模型，直接调 `ruleAgentRun` 方法，参考现有工具测试的写法）：

```go
// TestRuleAgentListKinds：类型计数只算已确认广告，recent_ids 最多 5 个、降序。
func TestRuleAgentListKinds(t *testing.T) {
	b := ruleAgentTestBot(t, func(w http.ResponseWriter, r *http.Request) {})
	var lastScam int64
	for i := 0; i < 6; i++ {
		lastScam = insertRuleAgentLog(t, b, "诈骗广告", "ad", "deleted", "scam")
	}
	insertRuleAgentLog(t, b, "正常", "clean", "none", "scam")
	insertRuleAgentLog(t, b, "被撤销", "ad", "undone", "scam")
	insertRuleAgentLog(t, b, "促销", "ad", "deleted", "promo")

	run := &ruleAgentRun{sh: b.Shared}
	var got map[string]any
	if err := json.Unmarshal([]byte(run.listKinds()), &got); err != nil {
		t.Fatal(err)
	}
	if got["ads_total"].(float64) != 7 {
		t.Errorf("ads_total 应为 7（6 scam + 1 promo），得到 %v", got["ads_total"])
	}
	kinds := got["kinds"].([]any)
	scam := kinds[0].(map[string]any)
	if scam["kind"] != "scam" || scam["total"].(float64) != 6 {
		t.Errorf("scam 统计不对：%v", scam)
	}
	if n := len(scam["recent_ids"].([]any)); n != 5 {
		t.Errorf("recent_ids 应截到 5 个，得到 %d", n)
	}
	// 降序：最新一条在最前。
	if got := scam["recent_ids"].([]any)[0].(float64); int64(got) != lastScam {
		t.Errorf("recent_ids 应最新在前（%d），得到 %v", lastScam, got)
	}
}

// TestRuleAgentReadRecords：批量、去重截断、无效 id 容错。
func TestRuleAgentReadRecords(t *testing.T) {
	b := ruleAgentTestBot(t, func(w http.ResponseWriter, r *http.Request) {})
	id1 := insertRuleAgentLog(t, b, "广告一", "ad", "deleted", "scam")
	id2 := insertRuleAgentLog(t, b, "广告二", "ad", "deleted", "scam")
	run := &ruleAgentRun{sh: b.Shared}

	var got map[string]any
	if err := json.Unmarshal([]byte(run.readRecords([]int64{id1, id1, id2, 9999, 0})), &got); err != nil {
		t.Fatal(err)
	}
	if got["ignored"].(float64) != 1 { // 重复的 id1
		t.Errorf("ignored 应只统计重复/截断，得到 %v", got["ignored"])
	}
	recs := got["records"].([]any)
	if len(recs) != 4 { // id1/id2 正常 + 9999/0 各一条 error
		t.Fatalf("records 应有 4 条，得到 %d", len(recs))
	}
	if run.readRecords(nil) != "错误：ids 必须是非空数组" {
		t.Errorf("空 ids 文案不对：%s", run.readRecords(nil))
	}
}

// TestRuleAgentListRules：返回现有规则、未测试的 last_tested_at=0/coverage=0。
func TestRuleAgentListRules(t *testing.T) {
	b := ruleAgentTestBot(t, func(w http.ResponseWriter, r *http.Request) {})
	id := insertRule(t, b, "旧规则", "旧正则", "promo", true, false)
	run := &ruleAgentRun{sh: b.Shared}
	var got map[string]any
	if err := json.Unmarshal([]byte(run.listRules()), &got); err != nil {
		t.Fatal(err)
	}
	rules := got["rules"].([]any)
	if len(rules) != 1 || rules[0].(map[string]any)["id"].(float64) != float64(id) {
		t.Fatalf("list_rules 不对：%v", got)
	}
	if rules[0].(map[string]any)["coverage"].(float64) != 0 {
		t.Errorf("未测试规则 coverage 应为 0：%v", rules[0])
	}
	if rules[0].(map[string]any)["last_tested_at"].(float64) != 0 {
		t.Errorf("未测试规则 last_tested_at 应为 0：%v", rules[0])
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/antiad -run 'TestRuleAgentListKinds|TestRuleAgentReadRecords|TestRuleAgentListRules' -v`
Expected: 编译失败（方法不存在）。

- [ ] **Step 3: 实现**

常量：

```go
	// ruleAgentReadRecordsMax 是 read_records 单次最多读的条数。
	ruleAgentReadRecordsMax = 10
	// ruleAgentListKindsRecent 是 list_kinds 每类附带的最近流水 id 数。
	ruleAgentListKindsRecent = 5
	// ruleAgentListRulesMax 是 list_rules 最多返回的规则数（取最近创建的）。
	ruleAgentListRulesMax = 100
```

参数类型：

```go
// readRecordsArgs 是 read_records 的参数。
type readRecordsArgs struct {
	IDs []int64 `json:"ids" jsonschema:"required,description=要读取的判定流水 id 列表（最多 10 个，去重后截断）"`
}

// listKindsArgs / listRulesArgs 无参数；InferTool 需要占位类型。
type listKindsArgs struct{}
type listRulesArgs struct{}
```

实现（放在 `readRecord` 附近）：

```go
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
```

`readRecord` 改为（**保持原有字符串错误文案不变**，spec §3.1 要求单条工具行为不变）：

```go
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
```

```go
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
			name, pattern, category, kindsRaw    string
		)
		if err := rows.Scan(&id, &name, &pattern, &category, &en, &enf,
			&tested, &tp, &fp, &adsTotal, &kindsRaw); err != nil {
			return "错误：解析必封规则失败：" + err.Error()
		}
		rules = append(rules, map[string]any{
			"id": id, "name": name, "pattern": pattern, "category": category,
			"enabled": en == 1, "enforce": enf == 1, "last_tested_at": tested,
			"last_tp": tp, "last_fp": fp, "last_ads_total": adsTotal,
			"coverage": round4(ruleCoverage(tp, adsTotal)),
			"last_kinds": parseKindsJSON(kindsRaw),
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

// parseKindsJSON 解析落库的 last_kinds；坏数据按空数组。
func parseKindsJSON(raw string) []map[string]any {
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
```

工具注册（`buildRuleAgentTools`）：

- 更新 `ruleAgentToolNames` 为
  `[]string{"list_kinds", "list_banned", "read_record", "read_records", "find", "test_rule", "list_rules", "create_rule"}`。
- 在 list_banned 前注册 `list_kinds`：

```go
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
```

- `read_records`：

```go
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
```

- `list_rules`：

```go
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
```

- 返回切片按顺序加 `wrapRuleTool(run, "list_kinds", kindsTool)`、`"read_records"`、`"list_rules"`。

注意：`InferTool` 对空结构体的 schema 生成若报错，把 `listKindsArgs`/`listRulesArgs` 改成带一个被忽略的可选字段
（如 `Dummy bool \`json:"dummy"\``），并同步把调用处传零值。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/antiad -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/antiad/ruleagent.go internal/antiad/ruleagent_test.go
git commit -m "antiad: 新增 list_kinds/list_rules/read_records 只读工具"
```

### Task 6: 一轮多条 + 状态契约 + AI 创建落覆盖率

**Files:**
- Modify: `internal/antiad/ruleagent.go`
- Test: `internal/antiad/ruleagent_test.go`
- Test: `internal/panel/ruleagent_test.go`

- [ ] **Step 1: 写失败测试**

把现有单元测试 `TestRuleAgentCreateRuleSinglePerRun`（给 `run.createdRuleID` 赋值、
断言第二条被拒）替换为「同轮允许多条」的直调测试：

```go
// TestRuleAgentCreateRuleAllowsMultiplePerRun：同一轮连续创建不同 pattern
// 都写库，createdRuleIDs 累计，成功不增加失败计数。
func TestRuleAgentCreateRuleAllowsMultiplePerRun(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	insertRuleAgentLog(t, b, "广告文案一", "ad", "deleted", "scam")
	insertRuleAgentLog(t, b, "广告文案二", "ad", "deleted", "promo")

	run := &ruleAgentRun{sh: b.Shared, ctx: context.Background()}
	if got := run.createRule(context.Background(), createRuleArgs{
		Name: "规则一", Pattern: "广告文案一"}); !strings.Contains(got, "创建成功") {
		t.Fatalf("第一条应创建成功，得到 %q", got)
	}
	if got := run.createRule(context.Background(), createRuleArgs{
		Name: "规则二", Pattern: "广告文案二"}); !strings.Contains(got, "创建成功") {
		t.Fatalf("第二条也应创建成功，得到 %q", got)
	}
	if len(run.createdRuleIDs) != 2 || run.createFails != 0 {
		t.Errorf("createdRuleIDs=%v createFails=%d", run.createdRuleIDs, run.createFails)
	}
	var n int
	if err := b.Store.Read.QueryRow(`SELECT COUNT(*) FROM ad_rules`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("库里应有 2 条规则，得到 %d", n)
	}
}
```

再加端到端的多创建测试（假上游驱动）：

```go
// TestRuleAgentCreatesMultipleRules：一轮内可以连续创建多条规则，
// 状态带 created_rule_ids，结果文案列出全部。
func TestRuleAgentCreatesMultipleRules(t *testing.T) {
	var calls atomic.Int32
	b := ruleAgentTestBot(t, func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			w.Write([]byte(oaToolCallReply("c1", "create_rule", map[string]any{
				"name": "规则一", "pattern": "办理贷款", "category": "promo"})))
		case 2:
			w.Write([]byte(oaToolCallReply("c2", "list_rules", map[string]any{})))
		case 3:
			w.Write([]byte(oaToolCallReply("c3", "create_rule", map[string]any{
				"name": "规则二", "pattern": "博彩娱乐", "category": "gambling"})))
		default:
			w.Write([]byte(oaTextReply("本轮完成，共两条。")))
		}
	})
	insertRuleAgentLog(t, b, "办理贷款加微信 vx123", "ad", "deleted", "scam")
	insertRuleAgentLog(t, b, "博彩娱乐平台开户", "ad", "deleted", "gambling")

	if err := StartRuleDiscovery(b.Shared, 7); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	st := waitRuleAgentDone(t, 15*time.Second)
	ids, _ := st["created_rule_ids"].([]int64)
	if len(ids) != 2 {
		t.Fatalf("应创建两条规则，状态 %v", st)
	}
	if st["created_rule_id"] != ids[0] {
		t.Errorf("created_rule_id 应保留为第一条：%v", st)
	}
	var n int64
	if err := b.Store.Read.QueryRow(`SELECT COUNT(*) FROM ad_rules`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("库里应有 2 条规则，得到 %d", n)
	}
	var adsTotal int64
	var kinds string
	if err := b.Store.Read.QueryRow(`SELECT last_ads_total,last_kinds FROM ad_rules
		WHERE id=?`, ids[0]).Scan(&adsTotal, &kinds); err != nil {
		t.Fatal(err)
	}
	if adsTotal != 2 || !strings.Contains(kinds, `"kind":"scam"`) {
		// 两条广告都在开始前入库：第一条规则的扫描窗口里 AdsTotal=2。
		t.Errorf("AI 创建也要落覆盖率：ads=%d kinds=%s", adsTotal, kinds)
	}
	if result, _ := st["result"].(string); !strings.Contains(result, "2 条") {
		t.Errorf("结果应说明两条：%q", result)
	}
}

// TestRuleAgentCreatedRuleIDsEmpty：未创建/失败时是 [] 而不是 null。
func TestRuleAgentCreatedRuleIDsEmpty(t *testing.T) {
	b := ruleAgentTestBot(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(oaTextReply("没有发现。")))
	})
	if err := StartRuleDiscovery(b.Shared, 7); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	st := waitRuleAgentDone(t, 15*time.Second)
	ids, ok := st["created_rule_ids"].([]int64)
	if !ok || ids == nil || len(ids) != 0 {
		t.Errorf("created_rule_ids 应为空切片，得到 %#v", st["created_rule_ids"])
	}
}
```

再加连续失败清零测试（失败→成功→失败→失败不触发提前收尾）：

```go
// TestRuleAgentCreateFailsResetOnSuccess：连续失败计数在成功创建后清零。
// 观察点：模型调用次数——若成功后不清零，第 4 次失败就累计到 3，
// SetReturnDirectly 会提前收尾，第 6 次模型调用不会发生。
func TestRuleAgentCreateFailsResetOnSuccess(t *testing.T) {
	var calls atomic.Int32
	b := ruleAgentTestBot(t, func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1, 2, 4, 5:
			// 坏 pattern `[` 走严格编译失败，每次都算一次创建失败。
			w.Write([]byte(oaToolCallReply(fmt.Sprintf("c%d", n), "create_rule",
				map[string]any{"name": "坏规则", "pattern": "["})))
		case 3:
			w.Write([]byte(oaToolCallReply("c3", "create_rule", map[string]any{
				"name": "好规则", "pattern": "办理贷款"})))
		default:
			w.Write([]byte(oaTextReply("收尾。")))
		}
	})
	insertRuleAgentLog(t, b, "办理贷款加微信", "ad", "deleted", "scam")

	if err := StartRuleDiscovery(b.Shared, 7); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	st := waitRuleAgentDone(t, 15*time.Second)
	if got := calls.Load(); got < 6 {
		t.Errorf("成功后失败计数应清零、跑满 6 次模型调用，实际 %d 次（状态 %v）", got, st)
	}
	ids, _ := st["created_rule_ids"].([]int64)
	if len(ids) != 1 {
		t.Errorf("应只成功创建 1 条，得到 %v（状态 %v）", ids, st)
	}
	if errText, _ := st["error"].(string); strings.Contains(errText, "连续") {
		t.Errorf("不该触发连续失败收尾：%q", errText)
	}
}
```

（`fmt` 不在 `ruleagent_test.go` 现有 import 里，需要补。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/antiad -run 'TestRuleAgentCreateRuleAllowsMultiplePerRun|TestRuleAgentCreatesMultipleRules|TestRuleAgentCreatedRuleIDsEmpty|TestRuleAgentCreateFailsResetOnSuccess' -v`
Expected: FAIL（第二条被拒 / 状态缺 `created_rule_ids` / 提前收尾）。

- [ ] **Step 3: 实现**

`ruleAgentRuntime`：`createdRuleID int64` → `createdRuleIDs []int64`。

同时更新 `internal/panel/ruleagent_test.go` 的冻结契约字段清单，加入
`"created_rule_ids"`（与 `created_rule_id` 并存）。

`RuleAgentStatus`：

```go
	ids := append([]int64{}, ruleAgentRT.createdRuleIDs...)
	var first int64
	if len(ids) > 0 {
		first = ids[0]
	}
	return map[string]any{
		...
		"created_rule_id":  first,
		"created_rule_ids": ids,
		...
	}
```

（`ids` 用非 nil 空切片，保证 JSON 是 `[]`。）

`ruleAgentRun`：`createdRuleID int64` → `createdRuleIDs []int64`。

`finish`：

```go
	r.mu.Lock()
	created := append([]int64{}, r.createdRuleIDs...)
	fails, lastErr := r.createFails, r.lastCreateErr
	r.mu.Unlock()
	...
	switch {
	case len(created) > 0:
		ids := make([]string, 0, len(created))
		for _, id := range created {
			ids = append(ids, fmt.Sprintf("#%d", id))
		}
		result = fmt.Sprintf("发现完成：本轮写入 %d 条候选规则（%s），均未启用、未强制；"+
			"请在必封规则列表里逐条复核测试后决定是否启用。",
			len(created), strings.Join(ids, "、"))
	...
	}
	...
	ruleAgentRT.mu.Lock()
	...
	ruleAgentRT.createdRuleIDs = append([]int64{}, created...)
```

`createRule`：

- **删除**开头的同轮拦截（`if createdID != 0 { return ... }`）。
- 成功分支：删掉 `_ = react.SetReturnDirectly(ctx)`；改为

```go
	r.mu.Lock()
	r.createdRuleIDs = append(r.createdRuleIDs, id)
	r.createFails = 0 // 连续失败计数：成功即清零
	r.mu.Unlock()
	ruleAgentRT.mu.Lock()
	ruleAgentRT.createdRuleIDs = append(ruleAgentRT.createdRuleIDs, id)
	ruleAgentRT.mu.Unlock()
```

- 成功文案：

```go
	msg := fmt.Sprintf("创建成功：候选规则 #%d《%s》已写入（enabled=0、enforce=0，等待主管理员复核）。"+
		"本轮测试：命中广告 %d 条、正常消息 0 条、覆盖率 %.1f%%（全库扫描 %d 条）。"+
		"请继续用 list_kinds / list_rules 找下一个未覆盖形态；没有新形态时用中文总结收尾。",
		id, name, res.TP, res.Coverage()*100, res.Scanned)
	if ignoredEvidence > 0 {
		msg += fmt.Sprintf("已忽略 %d 个无效、重复或非广告的证据 id。", ignoredEvidence)
	}
	return msg
```

- INSERT 增加两列：

```go
	result, err := r.sh.Store.Write.Exec(`INSERT INTO ad_rules
		(name,pattern,category,note,source,enabled,enforce,
		 last_tp,last_fp,last_undone,last_scanned,last_tested_at,
		 last_ads_total,last_kinds,
		 created_at,created_by)
		VALUES (?,?,?,?, 'ai', 0, 0, ?,?,?,?,?, ?,?, ?,?)`,
		name, in.Pattern, category, note,
		res.TP, res.FP, res.Undone, res.Scanned, now,
		res.AdsTotal, RuleKindsJSON(res.Kinds), now, r.uid)
```

（序列化用 Task 1 导出的 `RuleKindsJSON`，与面板写回同源。）

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/antiad -v`
Expected: PASS（注意同步改掉依赖「创建即收尾」的旧断言）。

- [ ] **Step 5: 提交**

```bash
git add internal/antiad/ruleagent.go internal/antiad/ruleagent_test.go
git commit -m "antiad: 规则发现一轮可创建多条，状态带 created_rule_ids"
```

### Task 7: 提示词重写

**Files:**
- Modify: `internal/antiad/ruleagent.go`

- [ ] **Step 1: 替换 `ruleAgentSystemPrompt` 与 `ruleAgentUserPrompt`**

系统提示词要点（完整文案写进代码，保持现有口吻）：

1. 角色与语料不变；任务改为「在预算内产出多条规则，尽量覆盖不同广告形态/类型」。
2. 硬约束不变（RE2、不匹配空文本、fp=0/undone=0、证据 id、不得与现有 pattern 重复）。
3. 工作流：
   - `list_kinds` 看类型分布（total 从大到小）；
   - `list_rules` 看已有 pattern 与覆盖率，列出还没覆盖的类型/形态；
   - 对每个候选形态：`list_banned`（可按 kind、可翻 offset，分批读不要一次拉满）→
     `read_records` 批量读全文 → `find` 试跑（看覆盖率与误伤）→ `test_rule` 正式验证 →
     `create_rule` 创建；
   - 创建成功后**继续回到 `list_kinds`/`list_rules` 找下一个未覆盖形态**；
   - 没有新的高精度形态、或步数/时间预算将尽时，用中文总结收尾（列出创建了哪些规则、依据哪些证据）。
4. 覆盖率说明：`find`/`test_rule` 会返回总体覆盖率与按类型细分；覆盖率不是创建门槛，
   但优先做「覆盖某类型较大比例」的形态；覆盖率过低说明规则太窄，考虑合并同类话术。
5. 读取量提醒：工具单次返回可能很大，分批读；重复 pattern 会被拒绝。

用户提示词：

```
请开始一轮规则发现：先用 list_kinds 看广告类型分布，再用 list_rules 看已有覆盖与空白；
然后对每个未覆盖的高精度形态按 list_banned → read_records → find → test_rule → create_rule
的流程产出候选规则。创建成功后不要停，继续找下一个形态；没有新形态或预算将尽时总结收尾。
```

- [ ] **Step 2: 跑测试**

Run: `go test ./internal/antiad -v`
Expected: PASS（测试不校验提示词原文，只校验行为）。

- [ ] **Step 3: 提交**

```bash
git add internal/antiad/ruleagent.go
git commit -m "antiad: 规则发现提示词改为多形态覆盖工作流"
```

---

## Chunk 3: Mini App 前端

### Task 8: 类型、fixture 与 mock

**Files:**
- Modify: `web/src/api/types.ts`
- Modify: `web/src/mocks/fixtures.ts`
- Modify: `web/src/mocks/handlers.ts`

- [ ] **Step 1: 类型**

`types.ts`：

```ts
export interface RuleKindStat {
  kind: string
  total: number
  matched: number
  /** 工具/测试响应里带；列表落库数据由前端按 matched/total 现算。 */
  coverage?: number
}

// Rule 追加：
  last_ads_total: number
  last_kinds: RuleKindStat[]

// RuleTest 追加：
  ads_total: number
  coverage: number
  kinds: RuleKindStat[]

// RuleAgent 追加：
  created_rule_ids: number[]
```

- [ ] **Step 2: fixture**

`fixtures.ts`：`mockRules[0]` 加 `last_ads_total: 120`、
`last_kinds: [{ kind: 'scam', total: 100, matched: 9 }, { kind: 'promo', total: 20, matched: 0 }]`
（与现有 `last_tp: 9` 一致 → 覆盖率 7.5%）；`mockRules[1]` 加
`last_ads_total: 0, last_kinds: []`。
`mockRuleTest` 加：

```ts
  ads_total: 96,
  coverage: 0.125,
  kinds: [
    { kind: 'scam', total: 80, matched: 12, coverage: 0.15 },
    { kind: 'promo', total: 16, matched: 0, coverage: 0 },
  ],
```

（`tp: 12` / `ads_total: 96` → 12.5%，与现有 `mockRuleTest.tp=12` 自洽。）
`mockRuleAgent` 加 `created_rule_ids: []`。
`handlers.ts` 的 `agent_start` 返回值同步加 `created_rule_ids: []`。

- [ ] **Step 3: 跑类型检查**

Run: `npm --prefix web run typecheck`
Expected: 先报错（`fixtures.ts` 缺新字段、`RulesPage.test.tsx` 里两处
`const testResp: RuleTest = {...}` 字面量缺 `ads_total/coverage/kinds`）→
把这两处字面量补上（`ads_total: 1200`、`coverage: 0`、`kinds: []` 即可，
它们只服务强制门用例）→ PASS。

- [ ] **Step 4: 提交**

```bash
git add web/src/api/types.ts web/src/mocks/fixtures.ts web/src/mocks/handlers.ts
git commit -m "web: 规则契约类型与 mock 补覆盖率字段"
```

### Task 9: 「测试正则」抽屉

**Files:**
- Modify: `web/src/pages/RulesPage.tsx`
- Test: `web/src/pages/RulesPage.test.tsx`

- [ ] **Step 1: 写失败测试**

```tsx
it('测试正则：试跑展示结果，可带着正则去新建', async () => {
  captureRules((body) => {
    if (body.action === 'list') return HttpResponse.json({ rules: [] })
    if (body.action === 'agent_status') return HttpResponse.json({ agent: mockRuleAgent })
    if (body.action === 'test') return HttpResponse.json({ test: mockRuleTest })
    return ok()
  })
  renderPage(<RulesPage />)
  await screen.findByText('还没有规则')

  fireEvent.click(screen.getByRole('button', { name: '测试正则' }))
  fireEvent.change(await screen.findByLabelText('正则（RE2）'), {
    target: { value: '兼职.{0,6}押金' },
  })
  fireEvent.click(screen.getByRole('button', { name: '开始测试' }))

  // 覆盖率格/按类型小节在 Task 10 才加，这里只验结果面板与预填。
  expect(await screen.findByTestId('rule-test-result')).toBeInTheDocument()
  expect(screen.getByTestId('rule-test-tp')).toHaveTextContent('12')

  fireEvent.click(screen.getByRole('button', { name: '用此正则新建规则' }))
  expect(await screen.findByLabelText('正则（RE2）')).toHaveValue('兼职.{0,6}押金')
  expect(screen.getByText('手动新增规则')).toBeInTheDocument()
})
```

（`mockRuleTest` 需要 import；测试文件已有 `captureRules`/`ok`/`renderPage`/`mockRuleAgent`。）

```tsx
it('测试正则：400 保留输入并 toast 服务端文案', async () => {
  captureRules((body) => {
    if (body.action === 'list') return HttpResponse.json({ rules: [] })
    if (body.action === 'agent_status') return HttpResponse.json({ agent: mockRuleAgent })
    if (body.action === 'test') {
      return HttpResponse.json(
        { error: '测试失败：规则不能匹配空文本（会命中所有消息）' },
        { status: 400 },
      )
    }
    return ok()
  })
  renderPage(<RulesPage />)
  await screen.findByText('还没有规则')

  fireEvent.click(screen.getByRole('button', { name: '测试正则' }))
  fireEvent.change(await screen.findByLabelText('正则（RE2）'), { target: { value: 'a*' } })
  fireEvent.click(screen.getByRole('button', { name: '开始测试' }))

  expect(
    await screen.findByText('测试失败：规则不能匹配空文本（会命中所有消息）'),
  ).toBeInTheDocument()
  expect(screen.getByLabelText('正则（RE2）')).toHaveValue('a*')
})
```

- [ ] **Step 2: 跑测试确认失败**

Run: `npm --prefix web test -- src/pages/RulesPage.test.tsx`
Expected: FAIL（找不到「测试正则」按钮）。

- [ ] **Step 3: 实现**

`RulesPage`：

```tsx
const testMut = useRulesMutation<{ test: RuleTest }>()
const [testOpen, setTestOpen] = useState(false)
const [testPattern, setTestPattern] = useState('')
const [testResult, setTestResult] = useState<RuleTest | null>(null)
```

标题行按钮：

```tsx
<Button size="small" onClick={() => { setTestPattern(''); setTestResult(null); setTestOpen(true) }}
  sx={{ minHeight: 30, px: 1, fontSize: 13 }}>
  测试正则
</Button>
```

抽屉（放在手动新增抽屉前）：

```tsx
<FormDrawer
  open={testOpen}
  onClose={() => setTestOpen(false)}
  title="测试正则"
  pending={testMut.isPending}
  submitText="开始测试"
  submitDisabled={testPattern.trim() === ''}
  onSubmit={() =>
    testMut.mutate(
      { action: 'test', pattern: testPattern.trim() },
      {
        onSuccess: (resp) => setTestResult(resp.test),
        onError: (err) => toast(err.message),
      },
    )
  }
>
  <TextField
    fullWidth
    multiline
    minRows={2}
    size="small"
    label="正则（RE2）"
    placeholder="如 兼职.{0,6}(日结|垫付|押金)"
    value={testPattern}
    onChange={(event) => setTestPattern(event.target.value)}
    sx={{ '& textarea': { fontFamily: MONO, fontSize: 13 } }}
  />
  <Typography sx={{ mt: 1, fontSize: 12, color: 'text.secondary', lineHeight: 1.6 }}>
    只试跑不落库；保存时服务端仍会跑一次同样的全库测试。
  </Typography>
  {testResult !== null && (
    <>
      <RuleTestPanel test={testResult} tz={tz} />
      <Button
        fullWidth
        variant="outlined"
        sx={{ mt: 1.5 }}
        onClick={() => {
          setTestOpen(false)
          setName('')
          setPattern(testPattern.trim())
          setCategory('')
          setNote('')
          setAddOpen(true)
        }}
      >
        用此正则新建规则
      </Button>
    </>
  )}
</FormDrawer>
```

- [ ] **Step 4: 跑测试确认通过**

Run: `npm --prefix web test -- src/pages/RulesPage.test.tsx`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add web/src/pages/RulesPage.tsx web/src/pages/RulesPage.test.tsx
git commit -m "web: 规则页新增测试正则抽屉"
```

### Task 10: 覆盖率展示

**Files:**
- Modify: `web/src/pages/RulesPage.tsx`
- Test: `web/src/pages/RulesPage.test.tsx`

- [ ] **Step 1: 写失败测试**

```tsx
it('列表行展示最近测试覆盖率', async () => {
  captureRules((body) => {
    if (body.action === 'list') return HttpResponse.json({ rules: mockRules })
    if (body.action === 'agent_status') return HttpResponse.json({ agent: mockRuleAgent })
    return ok()
  })
  renderPage(<RulesPage />)
  expect(await screen.findByTestId('rule-tpfp-1')).toHaveTextContent('覆盖 7.5%')
})

it('详情展示最近测试覆盖率与落库的按类型细分', async () => {
  captureRules((body) => {
    if (body.action === 'list') return HttpResponse.json({ rules: [mockRules[0]] })
    return ok()
  })
  renderPage(<RuleDetailPage id={1} />)
  expect(await screen.findByText(/覆盖率 7\.5%/)).toBeInTheDocument()
  expect(screen.getByText('按类型覆盖')).toBeInTheDocument()
  expect(screen.getByText('scam')).toBeInTheDocument()
})
```

（`mockRules[0].last_tp=9`、`last_ads_total=120` → 7.5%；现有
`toHaveTextContent('TP 9 · FP 0')` 是子串匹配，不受新增「覆盖 7.5%」段影响。）

```tsx
it('测试正则抽屉展示覆盖率与按类型', async () => {
  captureRules((body) => {
    if (body.action === 'list') return HttpResponse.json({ rules: [] })
    if (body.action === 'agent_status') return HttpResponse.json({ agent: mockRuleAgent })
    if (body.action === 'test') return HttpResponse.json({ test: mockRuleTest })
    return ok()
  })
  renderPage(<RulesPage />)
  await screen.findByText('还没有规则')

  fireEvent.click(screen.getByRole('button', { name: '测试正则' }))
  fireEvent.change(await screen.findByLabelText('正则（RE2）'), { target: { value: '兼职' } })
  fireEvent.click(screen.getByRole('button', { name: '开始测试' }))

  expect(await screen.findByTestId('rule-test-coverage')).toHaveTextContent('12.5%')
  expect(screen.getByText('按类型覆盖')).toBeInTheDocument()
  expect(screen.getByText('scam')).toBeInTheDocument()
})
```

- [ ] **Step 2: 跑测试确认失败**

Run: `npm --prefix web test -- src/pages/RulesPage.test.tsx`
Expected: FAIL。

- [ ] **Step 3: 实现**

加格式化与小组件（types import 补 `RuleKindStat`）：

```tsx
/** pct 覆盖率显示：一位小数；分母为 0 时 null（调用方决定「—」或省略）。 */
function pct(matched: number, total: number): string | null {
  if (total <= 0) return null
  return `${((matched / total) * 100).toFixed(1)}%`
}

/** KindCoverageList 是「按类型覆盖」小节；countsOnly=true 用于落库数据（无样本）。 */
function KindCoverageList({ kinds, title = '按类型覆盖' }: { kinds: RuleKindStat[]; title?: string }) {
  if (kinds.length === 0) return null
  return (
    <Box sx={{ mt: 1.5 }}>
      <Typography sx={{ fontSize: 13, fontWeight: 600, color: 'text.secondary' }}>
        {title}
      </Typography>
      {kinds.map((k) => {
        const p = pct(k.matched, k.total)
        return (
          <Box key={k.kind || '（空）'} sx={{ display: 'flex', gap: 1, mt: 0.5, fontSize: 13 }}>
            <Box component="span" sx={{ flex: 1, fontFamily: MONO }}>{k.kind || '（空）'}</Box>
            <Box component="span" sx={{ color: 'text.secondary' }}>
              {k.matched}/{k.total}
            </Box>
            <Box component="span" sx={{ color: k.matched === 0 ? 'text.disabled' : 'text.primary' }}>
              {p ?? '—'}
            </Box>
          </Box>
        )
      })}
    </Box>
  )
}
```

`RuleTestPanel`：数字格加

```tsx
<TestStat testId="rule-test-coverage" label="覆盖率"
  value={pct(test.tp, test.ads_total) ?? '—'} />
```

（`TestStat` 的 `value` 类型放宽为 `number | string`。）数字格后加
`<KindCoverageList kinds={test.kinds} />`。

`TpFpValue`：

```tsx
  const cov = pct(rule.last_tp, rule.last_ads_total)
  return (
    <Box ...>
      TP {rule.last_tp} · FP {rule.last_fp}
      {cov !== null && <> · 覆盖 {cov}</>}
    </Box>
  )
```

详情「最近测试」InfoRow：

```tsx
            ? `${fmtTS(rule.last_tested_at, tz)} · 扫描 ${rule.last_scanned} · TP ${rule.last_tp} · FP ${rule.last_fp}${
                rule.last_undone > 0 ? ` · 已撤销 ${rule.last_undone}` : ''
              }${pct(rule.last_tp, rule.last_ads_total) !== null ? ` · 覆盖率 ${pct(rule.last_tp, rule.last_ads_total)}` : rule.last_ads_total === 0 ? ' · 覆盖率 —' : ''}`
```

并在该 InfoRow 下渲染 `<KindCoverageList kinds={rule.last_kinds} />`。

- [ ] **Step 4: 跑测试确认通过**

Run: `npm --prefix web test -- src/pages/RulesPage.test.tsx`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add web/src/pages/RulesPage.tsx web/src/pages/RulesPage.test.tsx
git commit -m "web: 规则列表/详情/测试面板展示覆盖率与按类型细分"
```

### Task 11: Agent 卡多条创建

**Files:**
- Modify: `web/src/pages/RulesPage.tsx`
- Test: `web/src/pages/RulesPage.test.tsx`

- [ ] **Step 1: 写失败测试**

```tsx
it('Agent 结束展示本轮创建的多条规则', async () => {
  captureRules((body) => {
    if (body.action === 'list') return HttpResponse.json({ rules: [] })
    if (body.action === 'agent_status') {
      return HttpResponse.json({
        agent: {
          ...mockRuleAgent,
          finished_at: 1700000700,
          result: '发现完成：本轮写入 2 条候选规则',
          created_rule_id: 1,
          created_rule_ids: [1, 2],
        },
      })
    }
    return ok()
  })
  renderPage(<RulesPage />)
  expect(await screen.findByText(/本轮创建规则：#1、#2/)).toBeInTheDocument()
})
```

同时**更新现有用例**「空态 → 开始发现 → …」：结束态设置改为
`created_rule_ids: [7]`（保留 `created_rule_id: 7`），断言从
`screen.getByText(/已创建规则 #7/)` 改为
`screen.getByText(/本轮创建规则：#7/)`；否则删掉旧分支后它会失败。

- [ ] **Step 2: 跑测试确认失败**

Run: `npm --prefix web test -- src/pages/RulesPage.test.tsx`
Expected: FAIL。

- [ ] **Step 3: 实现**

`AgentCard` 结果区把单条展示改为：

```tsx
          {(agent.created_rule_ids ?? []).length > 0 && (
            <Typography sx={{ mt: 0.5, fontSize: 13, color: 'text.secondary' }}>
              本轮创建规则：{(agent.created_rule_ids ?? []).map((id) => `#${id}`).join('、')}，可在下方列表查看与测试
            </Typography>
          )}
```

（删除旧的 `agent.created_rule_id > 0` 分支；`created_rule_id` 仍留在类型里兼容。）

- [ ] **Step 4: 跑测试确认通过**

Run: `npm --prefix web test -- src/pages/RulesPage.test.tsx`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add web/src/pages/RulesPage.tsx web/src/pages/RulesPage.test.tsx
git commit -m "web: Agent 卡展示本轮创建的多条规则"
```

---

## Chunk 4: 文档与质量门

### Task 12: docs/panel.md

**Files:**
- Modify: `docs/panel.md`

- [ ] **Step 1: 更新「AI 必封规则」一节**

补：一轮可连续产出多条候选（跑满 1000 步 / 30 分钟预算，不设条数上限）；
Agent 工具与读取限额（list_kinds/list_rules/read_records，list_banned 与 find
上限 200）；覆盖率口径（已确认广告召回 + 按 ad_kind 细分，不设创建门槛）；
规则页「测试正则」抽屉（试跑不落库 + 覆盖率 + 一键新建）。

- [ ] **Step 2: 提交**

```bash
git add docs/panel.md
git commit -m "docs: 更新 AI 必封规则（多条产出、覆盖率、测试工具）"
```

### Task 13: 全量质量门与收尾

- [ ] **Step 1: 后端**

Run: `go test ./...`
Expected: 全部 PASS。

- [ ] **Step 2: 前端**

Run: `npm --prefix web run build && npm --prefix web test && npm --prefix web run lint`
Expected: tsc 通过、42+ 测试文件全 PASS、lint 无新增 error（仅既有 `shared.tsx` warning）。

- [ ] **Step 3: bd 工单**

```bash
bd create "规则发现 v2：一轮多条 + 覆盖率测试工具" -t feature -p 2
bd close <id> --reason "实现、测试、文档全部完成"
```

- [ ] **Step 4: 提交与推送**

```bash
git status   # 确认工作树干净（除既有未跟踪文件）
git pull --rebase origin main && git push origin main
```

---

## 风险与注意事项

- `create_rule` 的旧「同轮拦截」与 `SetReturnDirectly` 必须同时删；只删一个会出现
  「不终止但拒绝第二条」或「终止所以永远只有一条」。
- `createFails` 必须成功清零，否则「失败→成功→失败→失败」会误触发提前收尾。
- 覆盖率分母与 TP 口径必须同源（`verdict='ad' && action<>'undone'`），否则
  `find`/`test_rule` 会出现两个数字。
- `last_kinds` 的 JSON 键由 `RuleKindStat` 的 tag 决定，改动 tag 要同步
  panel 解析与前端类型。
- `InferTool` 空参数结构体若 schema 生成失败，用带 `dummy` 字段的占位结构体兜底。
