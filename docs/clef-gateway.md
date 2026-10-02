# Clef 决策上游（Cloudflare Workers AI）

[Clef](https://developers.cloudflare.com/workers-ai/models/clef/) 是 Cloudflare
2026-10 在 Workers AI 上线的决策模型（System One API，Jev 的平替）：
不生成文本，输入 `state` + 一组带类型的 `questions`，直接返回每个选项的
概率与置信度。它对 menshen 的「带置信度的结构化答案」判定链路是天然契合的。

`@cf/cloudflare/clef`（27B，$0.24 / M input tokens）追求精度，
`@cf/cloudflare/clef-flash`（9B，$0.09 / M input tokens）追求延迟，
两者都是 64K 上下文、支持图像。

| 问题类型 | 返回 |
|---|---|
| `noul` | 是为真的概率 |
| `choice` | 命中项 + 每项概率 + `confidence` |
| `score` | 概率加权的 `score` + 每档概率 + `confidence` |

## 为什么需要一层 Worker

menshen 对 systemone 上游的契约是 `POST {base_url}/v1/systemone`，请求体里的
`model` 必须是上游认识的模型名；而 Workers AI 的 REST 端点路径写死为
`/accounts/{id}/ai/run/@cf/cloudflare/clef`，响应还套了一层 `result` 外壳。
两者对不上，所以用 [cloudflare/clef-gateway](../cloudflare/clef-gateway/README.md)
里那个单文件 Worker 做协议垫片：

```text
menshen ──Bearer──▶ clef-gateway (Worker) ──AI binding──▶ @cf/cloudflare/clef
 POST /v1/systemone                              （可选 AI Gateway）
```

垫片只做三件事：鉴权、把模型名归一化成 `clef` / `clef-flash`、其余字段原样
进原样出。**menshen 不需要改任何代码** —— 面板早就预留了 systemone 端点开关。

## 一、部署垫片

按 [cloudflare/clef-gateway/README.md](../cloudflare/clef-gateway/README.md)
操作，三步：

```bash
cd cloudflare/clef-gateway
npm install
npx wrangler secret put API_TOKEN   # 记下这个串，下面面板要填
npm run deploy
```

记下部署输出的 `https://clef-gateway.<子域>.workers.dev`。

## 二、面板接入

| 面板项 | 填写 |
|---|---|
| 🤖 上游渠道 → 新增 | 名称 `cfclef`；base_url `https://clef-gateway.<子域>.workers.dev`（**只到域名**，不要带 `/v1/systemone`）；api_key 填 Worker 的 `API_TOKEN`；能力**只勾 systemone，不勾 chat** |
| 🤖 模型定价 → 新增 | 模型 ID 填 `clef-flash` 或 `clef`（面板里会存成 `cfclef/clef-flash`）；输入价 `0.09` / `0.24`；**补全价填 0**（官方输出侧不计费，面板模型页也有同样提示）；缓存价填 0 |
| ⚙️ 全局设置 → 默认判定模型（systemone） | 选 `cfclef/clef-flash`（热路径快）或 `cfclef/clef`（精度高） |
| ⚙️ 全局设置 → `antiad_so_timeout_ms` | 默认 2000ms。Clef p95 约 240ms，加 Worker 与公网 RTT 一般够用；内地服务器抖动大时可放宽到 3000–5000 |

大模型复判（`antiad_llm_models`）不需要动，继续用原来的 chat 上游即可。

## 三、两端协议对应关系

menshen 发什么、Clef 认什么：

| menshen 发出的 | Clef | 备注 |
|---|---|---|
| `model` | `clef` / `clef-flash` | menshen 发的是剥掉上游前缀后的模型 ID，所以模型 ID 必须填这两个名字之一；垫片也容错 `@cf/...` 写法 |
| `state` | ✅ 原生支持结构化对象 | 消息上下文 JSON 直接进 |
| `questions.*.type` | `choice` / `score` 均支持 | menshen 目前不用 `noul` |
| `questions.severity.criteria` | 有序数组，2–10 档 | menshen 已是数组写法；写成 map 会被上游 422 |
| `answers.is_ad.choice` / `.confidence` | ✅ choice 答案自带 | `judgeSystemOne` 直接可读 |
| `answers.ad_kind` / `ad_scope` / `severity.score` | ✅ | 同上 |
| `usage.input_tokens` / `output_tokens` | ✅ 扁平结构 | 与 menshen 的 `responsesUsage` 解析一致，计费正常 |

## 成本

- Clef 官方只对输入计价：`clef` $0.24 / M，`clef-flash` $0.09 / M；输出 token
  会返回但不计费，所以面板里补全价填 0。
- 每条群消息都会送检（设计如此），配合内容哈希与护栏控制总量，开销在面板
  「近 24 小时」那行可见。

## 排错

| 现象 | 原因 / 处置 |
|---|---|
| 面板自检报「模型绑定的上游……不支持该端点」或日志 404 | 上游没勾 systemone，或 base_url 尾部多带了 `/v1/systemone` |
| 401 | Worker 的 `API_TOKEN` 与面板 api_key 不一致；或 secret 没生效，需重新 `wrangler secret put` 后 `npm run deploy` |
| 日志「上游返回 422」 | questions 不符合 Clef schema（如 score 档次不在 2–10 之间、choice 选项不足 2 个、问题数超 64） |
| 日志「上游超过 2000ms 没有响应」 | 调大 `antiad_so_timeout_ms`，或改用 `clef-flash`；menshen 会立即切下一个判定模型 |
| 502 | Worker 缺 AI binding / 账号未开通 Workers AI / 额度用尽；`wrangler tail` 看 detail |

## 进阶

- **图片**：Clef 每请求最多 4 张图（PNG/JPEG/WebP），只收 base64 data URL 或
  `{content_type, base64}`，**不接受远程 URL**，整个请求体上限 13 MiB。
  menshen 当前的 systemone 请求不发图；以后要让识图直接参与主判，可以在
  `buildSystemOneReq` 里加 `images` 字段，垫片已原样透传。
- **AI Gateway**：给 Worker 配 `GATEWAY_ID` 变量即可获得调用日志、限流与缓存
  （绑定方式的 gateway 选项已内置）。
- **多调用方**：一个 Worker 可以服务所有 bot；需要按调用方隔离时，复制 Worker
  或给垫片加一层 key 表即可。
- **自托管退路**：权重已按 Apache 2.0 开源（Hugging Face 上的 `Cloudflare/clef`
  与 `clef-flash`），vLLM/SGLang 可部署；但要自行实现 System One 决策协议，
  除非有数据不出境的硬性要求，否则没必要。
