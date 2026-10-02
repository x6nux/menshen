# 规则发现 v2：一轮多条 + 覆盖率测试工具设计

> 2026-10-02。上游设计：`docs/superpowers/specs/2026-09-24-appeal-and-web-design.md`
> 与 `docs/panel.md` 的「AI 必封规则」一节。本设计只改规则发现与规则测试，
> 不动判定链路（enforce / 证据注入逻辑保持原样）。

## 目标

1. **一轮多条**：一次「开始发现」不再只产出一条规则，而是跑满步数/时间预算，
   对尽可能多的广告形态各产出一条高精度候选规则。
2. **多读现有内容**：Agent 能读到更多历史流水、看到广告类型分布、看到现有规则，
   先摸清语料与空白再写规则。
3. **测试工具 + 覆盖率**：Agent 的匹配/测试工具必须返回覆盖率；Mini App 提供
   任意正则试跑入口，同样展示覆盖率与按类型细分。

## 非目标

- 不设「一轮最多几条」硬上限（跑满预算；步数 1000 / 30 分钟仍是总兜底）。
- 覆盖率不做创建门槛：`create_rule` 仍只卡 `fp=0 && undone=0`。
- 不做规则集并集覆盖率、不做规则自动合并/去重（除「完全相同的 pattern 拒绝」）。
- 不改判定链路、不改 enforce 的防误封门、不改样本分类口径（TP/FP/Undone/Neutral）。

## 术语

| 词 | 含义 |
|---|---|
| 已确认广告 | `antiad_log.verdict='ad'` 且 `action<>'undone'` 的行（即现有 TP 口径） |
| 覆盖率 | 某正则命中的已确认广告数 ÷ 扫描窗口内已确认广告总数（召回率） |
| 类型覆盖率 | 按 `ad_kind` 分组的覆盖率：`matched/total` |
| 扫描窗口 | `antiad_log ORDER BY id DESC LIMIT 100000`（`ruleScanLimit`，与现状一致） |

## 0. 总体结构

```
antiad/rules.go      RuleTestResult + AdsTotal + Kinds；覆盖率在现有扫描循环里聚合
antiad/ruleagent.go  工具集扩展（list_kinds / list_rules / read_records）；
                     create_rule 不再收尾；状态契约加 created_rule_ids；提示词重写
store/db.go          ad_rules 增 last_ads_total / last_kinds（schema + migrate）
panel/miniapp.go     测试 JSON / 列表 JSON 带覆盖率；写回新列；无新端点
web/                 RulesPage 加「测试正则」抽屉；覆盖率展示；Agent 多条展示
```

## 1. 覆盖率口径与计算

### 1.1 定义

- 分母 `AdsTotal`：扫描窗口内 `verdict='ad'` 且 `action<>'undone'` 的行数。
- 分子：该正则命中且属于上一条的行数（即 `RuleTestResult.TP`）。
- `coverage = AdsTotal==0 ? 0 : TP/AdsTotal`，返回值不做四舍五入，展示方格式化。
- `undone` 行不计入分母（它的处罚已被撤销，不属于可信广告语料），与现有
  TP 分类逐条一致，不新增第二套口径。
- 覆盖率是「相对扫描窗口」的：与 `scanned` 一起展示，不宣称是绝对全量。

### 1.2 RuleTestResult 扩展

```go
// RuleKindStat 是按 ad_kind 细分的覆盖率。
type RuleKindStat struct {
    Kind    string // ad_kind；空串原样保留
    Total   int64  // 该类型已确认广告总数
    Matched int64  // 该类型被正则命中的已确认广告数
}

func (s RuleKindStat) Coverage() float64 // Matched/Total，Total=0 时 0

type RuleTestResult struct {
    // ...现有字段不变...
    AdsTotal int64          // 已确认广告总数（覆盖率分母）
    Kinds    []RuleKindStat // 仅含 Total>0 的类型，Total 降序、同数按 Kind 升序
}

func (r RuleTestResult) Coverage() float64
```

### 1.3 聚合位置

在 `testRulePatternCtx` 的现有扫描循环里聚合，不新增扫描：

- 每行先判「是否已确认广告」（`verdict=='ad' && action!='undone'`）：是则
  `AdsTotal++`，并累加 `kindTotal[ad_kind]++`。
- 命中时按现有 switch 分类；若命中行是已确认广告（TP 分支），累加
  `kindMatched[ad_kind]++`。
- 循环结束后把 map 转成切片并排序。

`findMatches`（Agent 试跑工具）在同一个循环里做同样的聚合，保证 `find` 与
`test_rule` 的覆盖率数字一致。

### 1.4 JSON 契约

Agent 工具（`find` / `test_rule`）新增：

```json
{
  "ads_total": 366,
  "coverage": 0.1093,
  "kinds": [
    {"kind": "scam", "total": 300, "matched": 40, "coverage": 0.1333},
    {"kind": "promo", "total": 66, "matched": 5, "coverage": 0.0758}
  ]
}
```

- `coverage` 四舍五入到 4 位小数（省 token，避免长浮点）；`kinds[].coverage`
  同样四舍五入到 4 位小数。
- `kinds` 全量返回（类型只有个位数），按 Total 降序。
- `test_rule` 的 `message` 文案追加覆盖率：通过时
  「通过：命中 X 条已确认广告（覆盖率 Y%）、0 条正常消息，可以创建。」
- 覆盖率不影响 `can_create` 判定。
- **人类可读文案里的百分比统一格式**：一位小数，如 `10.9%`
  （`fmt.Sprintf("%.1f%%", v*100)`），用于工具 `message`、`create_rule`
  返回与 `finish` 结果；JSON 数值保持 §1.4 的精度约定。

Mini App 测试 JSON（`miniRuleTestJSON`）新增：

```json
{
  "ads_total": 366,
  "coverage": 0.1092896174863388,
  "kinds": [{"kind": "scam", "total": 300, "matched": 40, "coverage": 0.13333333333333333}]
}
```

（浮点原样给前端，前端格式化为 1 位小数的百分比。）

### 1.5 落库

`ad_rules` 新增两列，保存/测试写回时一并更新，列表与详情页不用重新测试就能
展示覆盖率：

| 列 | 类型 | 含义 |
|---|---|---|
| `last_ads_total` | INTEGER NOT NULL DEFAULT 0 | 最近一轮测试的覆盖率分母 |
| `last_kinds` | TEXT NOT NULL DEFAULT '' | JSON：`[{"kind","total","matched"}]`（不含 coverage，避免舍入漂移） |

- `miniRuleWriteTest` 的 UPDATE 带上这两列；`last_kinds` 由
  `json.Marshal` 生成，失败时写空串（不阻断测试写回）。
- **AI 创建路径同样落这两列**：`createRule` 的 INSERT 直接写
  `last_ads_total`/`last_kinds`（它创建前刚跑完测试），不能只改
  `miniRuleWriteTest`，否则 AI 刚创建的规则在列表里覆盖率显示「—」。
- `miniRuleList` SELECT 带上两列，返回键固定为：
  - `last_ads_total`：int；
  - `last_kinds`：数组 `[{"kind","total","matched"}]`（解析失败按空数组），
    与 Agent 工具 `kinds` 的 `{kind,total,matched}` 形状一致（工具侧另带
    `coverage`，Mini App 侧由前端按 `matched/total` 现算，避免舍入漂移）。
- **Mini App 不新增服务端 `coverage` 键**：列表/详情的总体覆盖率由前端用
  `last_tp / last_ads_total` 现算（与 kinds 同一条防漂移理由）。
- 新库：`schemaSQL` 的 `ad_rules` 直接带上两列；老库：`migrate()` 的列清单
  追加两条，启动自动 ALTER。
- 旧数据 `last_ads_total=0` 不影响任何门；覆盖率展示按 §4.2 的分面约定
  （列表行省略该段、详情与测试面板显示「—」）。

## 2. Agent：一轮多条

### 2.1 创建不再收尾

- `createRule` 成功后**删除** `react.SetReturnDirectly(ctx)`，只把 id 记进本轮。
- **同时删除同轮拦截**：`createRule` 开头「本轮已创建规则 #N，不再重复创建；请直接
  总结收尾」那段守卫必须移除，否则「一轮多条」名存实亡。对应测试
  `TestRuleAgentCreateRuleSinglePerRun` 替换为多创建测试（见 §7）。
- `createFails` 改为真正的「连续」语义：**创建成功时清零**；失败累加，连续 3 次
  失败才 `SetReturnDirectly` 提前收尾（防死循环）。
- 返回文案改为：「创建成功：候选规则 #N《…》（enabled=0、enforce=0）。本轮测试：
  命中广告 X 条、正常消息 0 条、覆盖率 Y%。请继续用 list_kinds / list_rules
  找下一个未覆盖形态；没有新形态时用中文总结收尾。」（Y% 见 §1.4 的百分比格式）
- 保留现有「已忽略 N 个无效、重复或非广告的证据 id。」后缀（`filterEvidence`
  的计数不能因为文案重写而丢掉，`TestRuleAgentCreateRuleNotesOnlyValidEvidence`
  继续通过）。
- 完全相同 pattern 的查重不变；已创建过的规则在库里，重复创建会被查重拒绝。

### 2.2 运行态与状态契约

- `ruleAgentRun` 的 `createdRuleID int64` 改为 `createdRuleIDs []int64`（mu 保护）。
- `ruleAgentRuntime` 同样改存切片，创建成功时实时 append。
- `RuleAgentStatus` 返回：

```json
{"running":false, "started_at":0, "finished_at":0, "result":"…", "error":"",
 "created_rule_id": 1, "created_rule_ids": [1, 2], "steps":[…]}
```

  `created_rule_id` 保留 = 第一条（旧前端兼容）；`created_rule_ids` 是本轮全部，
  **无创建时返回 `[]` 而不是 `null`**（nil 切片要显式转成空切片，避免前端
  `number[]` 收到 null）。
- `finish` 成功分支（`len(createdRuleIDs)>0`）：
  「发现完成：本轮写入 N 条候选规则（#1、#2…），均未启用、未强制；
  请在必封规则列表里逐条复核测试后决定是否启用。」

### 2.3 预算与结束

- 总步数 1000 / 总时限 30 分钟不变，作为「跑满预算」的唯二兜底。
- 模型给出无工具调用的最终回答即正常结束；超时/步数耗尽/手动停止的文案不变。

## 3. Agent：多读与测试工具

### 3.1 工具清单与限额

工具从 5 个变为 8 个（`ruleAgentToolNames` 同步）：

| 工具 | 变化 |
|---|---|
| `list_kinds` | **新增**，只读 |
| `list_banned` | 默认 limit 20→50，上限 50→200；正文摘要仍 200 字 |
| `read_record` | 不变（单条，正文按库内全文，上限 2000 字） |
| `read_records` | **新增**，批量读，`ids` 最多 10 个 |
| `find` | 默认 limit 20→50，上限 50→200；输出加覆盖率与类型细分 |
| `test_rule` | 输出加覆盖率与类型细分；文案带覆盖率 |
| `list_rules` | **新增**，只读 |
| `create_rule` | 不再收尾；返回带覆盖率 |

所有工具仍走 `wrapRuleTool` 错误兜底，错误转字符串回给模型。

### 3.2 list_kinds

无参数。扫描窗口内按 `ad_kind` 聚合已确认广告：

```json
{
  "scanned": 1200, "ads_total": 366,
  "kinds": [
    {"kind": "scam", "total": 300, "recent_ids": [9812, 9801, 9788, 9760, 9700]},
    {"kind": "promo", "total": 66, "recent_ids": [...]}
  ],
  "hint": "先按 total 从大到小覆盖；recent_ids 可用 read_records 批量读全文。"
}
```

- 每个类型最多带 5 个最近流水 id；类型按 total 降序、同数按 kind 升序。
- 单次全表扫描（LIMIT 100000），与测试口径同一个窗口。

### 3.3 list_rules

无参数。返回现有规则与最近测试的覆盖率：**按 id 降序取最近 100 条**（本轮
新建的 id 最大、一定在返回里），返回数组再按 id 升序排列，保证确定性：

```json
{
  "rules": [
    {"id": 1, "name": "兼职押金话术", "pattern": "…", "category": "scam",
     "enabled": true, "enforce": false, "last_tested_at": 1700000000,
     "last_tp": 40, "last_fp": 0, "last_ads_total": 366, "coverage": 0.1093,
     "last_kinds": [{"kind": "scam", "total": 300, "matched": 40}]}
  ],
  "hint": "pattern 已存在的形态不要重复创建；优先补覆盖率为 0 或偏低的类型。"
}
```

- 直接读库（不读快照），本轮刚创建的规则立即可见。
- 每条规则带 `last_kinds`（与列表接口同一形状），模型才能按类型找空白；
  从未测试过的规则 `last_tested_at=0`、`coverage` **固定写 0**（用
  `last_tested_at=0` 区分「没测过」与「真的 0%」）；`enabled=false` 的候选
  同样返回。
- 规则数超过 100 时只返回最近 100 条（老规则如果不在返回里，模型按返回的
  pattern 集合去重即可）。

### 3.4 read_records

参数 `{"ids": [9812, 9801, ...]}`：

- `ids` 缺失 / 为 null / 解析后为空数组：返回错误提示「ids 必须是非空数组」。
  注：`ids` 是 `[]int64`，JSON 类型不对（如字符串）时会在 Eino 参数解析层被
  `wrapRuleTool` 拦成通用的「工具 read_records 调用失败：参数或执行出错…」，
  这条路径不作为契约要求，测试只覆盖空/缺失数组。
- 先按出现顺序去重，再截断到前 10 个；被截断与重复的 id 计入返回里的
  `ignored`（只统计这两类）。
- id<=0 与不存在的 id **不算 ignored**，在 `records` 里给
  `{"id":N,"error":"id 必须为正整数"}` / `{"id":N,"error":"没有 id=N 的判定记录"}`
  条目，不影响其他条。
- 返回 `{"records":[…], "ignored": N}`；每条正文与 `read_record` 一致
  （库内全文，上限 2000 字）。

### 3.5 提示词重写

`ruleAgentSystemPrompt` 关键变化：

- 任务从「产出一条最扎实的规则」改为「在预算内产出多条规则，尽量覆盖不同
  广告形态与类型」。
- 工作流：`list_kinds` 看类型分布 → `list_rules` 看已有覆盖与空白 →
  `list_banned`（可按 kind、可翻 offset）挑形态 → `read_records` 批量读全文 →
  `find` 试跑（看覆盖率与误伤）→ `test_rule` 正式验证 → `create_rule` 创建 →
  **回到第二步找下一个未覆盖形态**；没有新形态或预算将尽时用中文总结收尾。
- 覆盖率不是硬门槛，但「优先做覆盖某类型较大比例的形态；覆盖率过低的规则
  说明太窄，考虑合并同类话术或换更有代表性的形态」。
- 重申硬约束（RE2、不得匹配空文本、fp=0/undone=0、证据 id、不得重复现有
  pattern）。
- `ruleAgentUserPrompt` 同步改为「先摸清类型分布与现有规则，再按流程连续产出
  多条候选规则，直到没有新的高精度形态」。

## 4. Mini App

### 4.1 测试正则抽屉（RulesPage）

- 规则列表标题行新增「测试正则」按钮（在「＋ 手动新增」左侧）。
- 抽屉内容：
  - 正则输入框（等宽字体，placeholder 如 `兼职.{0,6}(日结|垫付|押金)`）；
  - 「开始测试」按钮（`action:'test'`，只带 pattern，不落库——复用现有接口）；
  - 测试结果：完整 `RuleTestPanel`（含覆盖率与按类型细分、样本）；
  - 「用此正则新建规则」按钮：关闭测试抽屉、打开「手动新增」抽屉并预填 pattern。
- 失败（编译错误等 400）在抽屉内 toast，保留输入。
- 手动新增抽屉的保存仍由服务端强制跑全库测试，行为不变。

### 4.2 覆盖率展示

- `RuleTestPanel` 数字格从 6 个变 7 个：新增「覆盖率」格（一位小数百分比，
  `ads_total=0` 显示「—」）；下方新增「按类型覆盖」小节：每行
  `kind  matched/total  xx.x%`，`matched=0` 的类型用灰字，避免像误封一样红。
- 规则列表行：`TP 40 · FP 0 · 覆盖 10.9%`（`last_tested_at=0` 仍显示「未测试」；
  `last_ads_total=0` 时**整段省略**覆盖率，不显示「—」）。
- 规则详情「最近测试」行追加 `覆盖率 10.9%`；`last_tested_at=0` 时该行仍显示
  「从未测试」，`last_tested_at>0` 但 `last_ads_total=0` 时覆盖率显示「—」。
  同一卡片下方渲染**落库的** `last_kinds`（无样本，只列
  `kind  matched/total  xx.x%`），这样离开页面再回来也能看到上一轮的按类型
  覆盖，不必重跑测试。实时测试面板仍展示带样本的完整版（那里 `ads_total=0`
  时覆盖率格显示「—」）。
- 测试结果面板同时被详情页与测试抽屉复用。

### 4.3 Agent 卡

- `created_rule_ids` 非空时显示「本轮创建规则：#1、#2」，并保留
  「可在下方列表查看与测试」。
- 运行中/结束文案随 2.2 的结果文案走，前端不再假设只有一条。

## 5. 表结构

```sql
-- schemaSQL（新库）在 ad_rules 里新增：
  last_ads_total INTEGER NOT NULL DEFAULT 0,
  last_kinds     TEXT    NOT NULL DEFAULT '',

-- migrate()（老库）追加：
  {"ad_rules", "last_ads_total", "INTEGER NOT NULL DEFAULT 0"},
  {"ad_rules", "last_kinds", "TEXT NOT NULL DEFAULT ''"},
```

无其他表/设置项变化。

## 6. 代码结构与改动清单

| 文件 | 改动 |
|---|---|
| `internal/antiad/rules.go` | `RuleKindStat`、`RuleTestResult.AdsTotal/Kinds/Coverage()`；扫描循环聚合 |
| `internal/antiad/rules_test.go` | 覆盖率与类型细分、undone 不入分母 |
| `internal/antiad/ruleagent.go` | 8 工具、限额、覆盖率输出、多创建、状态契约、提示词 |
| `internal/antiad/ruleagent_test.go` | 连续创建两条、list_kinds/list_rules/read_records、覆盖率字段 |
| `internal/store/db.go` | ad_rules 两列（schema + migrate） |
| `internal/store/db_test.go` | 老库迁移补列断言 |
| `internal/panel/miniapp.go` | test/list JSON 带覆盖率；write-back 新列；list 解析 kinds |
| `internal/panel/miniapp_test.go` | 契约与写回断言 |
| `web/src/api/types.ts` | Rule/RuleTest/RuleAgent 新字段 |
| `web/src/pages/RulesPage.tsx` | 测试抽屉、覆盖率展示、多条创建展示 |
| `web/src/pages/RulesPage.test.tsx` | 上述前端行为 |
| `web/src/mocks/fixtures.ts` + `handlers.ts` | fixture 与 mock 响应补新字段 |
| `docs/panel.md` | AI 必封规则一节更新（多条、覆盖率、测试工具、限额） |

## 7. 测试计划

- **覆盖率**：造 scam/promo/undone/clean 四类流水，断言 `AdsTotal`、`Kinds`
  排序与数值、`Coverage()`；`undone` 只进 FP 不进分母；空库 coverage=0。
- **口径一致**：`find` 与 `test_rule` 在同一份语料上的 `ads_total/tp/coverage`
  完全相等（沿用现有 find/test 对齐测试的写法）；`list_kinds` 的类型计数同样
  排除 `undone`（不只是 `RuleTestResult` 的分母）。
- **多创建**：替换现有 `TestRuleAgentCreateRuleSinglePerRun`：假上游按
  `create A → list_rules → create B → 文本收尾` 驱动，断言两条规则落库、
  `created_rule_ids=[A,B]`、result 提到两条、运行态归位；另测
  `失败→成功→失败→失败` 不会触发连续失败收尾（成功清零）；再测未创建/
  失败的运行 `created_rule_ids` 是 `[]` 而不是 `null`。
- **新工具**：`list_kinds` 的类型计数与 recent_ids；`read_records` 批量、去重
  截断、`ignored` 语义、无效 id 容错、空/缺失 ids 报错；`list_rules` 返回本轮
  刚创建的规则、未测试规则的 `last_tested_at=0`/`coverage=0`、`last_kinds`
  透出，以及规则数 >100 时仍包含最新创建的规则（降序取 100 再升序返回）。
- **落库**：保存/测试/AI 创建三条路径后 `last_ads_total`、`last_kinds` 都写回；
  列表 JSON 能解析且键名固定；老库启动后两列存在且默认值正确。
- **覆盖率不设门槛**：覆盖率极低（如 TP=1/AdsTotal=10000）但 fp=0 的规则仍能
  通过 `create_rule` 与保存；覆盖率不影响 `can_create`。
- **前端**：测试抽屉试跑（mock 返回带覆盖率）→ 展示覆盖率与按类型；列表行
  覆盖率；详情「最近测试」覆盖率与落库按类型；Agent 多条创建 id 展示；
  400 保留输入。
- **质量门**：`go test ./...`、`npm --prefix web run build`（tsc）、
  `npm --prefix web test`、`npm --prefix web run lint`。

## 8. 风险与取舍

- **成本**：一轮可能创建多条并大量读样本，token 消耗显著高于现状；由用户明确
  选择「不限条数、跑满预算」，总兜底仍是 1000 步 / 30 分钟。
- **长上下文**：`list_banned` 上限 200 条 × 200 字、`find` 上限 200 条样本、
  `list_rules` 最多 100 条 × pattern 最多 500 字符，单次可达约 4 万字符以上；
  模型可能接近上下文上限，提示词要求分批读、不要一次拉满（pattern 原样返回，
  如需截断在计划阶段定，默认不截断以保查重准确）。
- **覆盖率口径盲区**：与现有测试一致——只看库内截断后的正文（1000 rune），
  长消息尾部特征不可见；文案与现有测试页保持同一说明口径。
- **旧前端兼容**：状态保留 `created_rule_id`；新字段都是追加，旧 bundle 不会
  因为缺字段崩（TS 类型同步更新，产物与后端同版本部署）。
- **实施分两阶段**：后端（规则引擎 + Agent + 表 + 面板契约）先行并可独立
  测试，前端随后；计划按这两个阶段拆分，便于分段评审与回滚。
