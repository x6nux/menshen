# clef-gateway

把 Cloudflare Workers AI 上的 [Clef](https://developers.cloudflare.com/workers-ai/models/clef/)
决策模型（`@cf/cloudflare/clef` / `clef-flash`）包成 **System One 兼容**端点
（`POST /v1/systemone`），让 menshen（或任何 Jev / SystemOne 客户端）把它当作
一个普通的 systemone 判定上游接入。

- 单文件 Worker（`worker.js`），零依赖，只做鉴权 + 模型名归一化 + 透传
- 走 `AI` binding 调模型，**代码与配置里都不出现 Cloudflare API Token**
- 上游返回的 `answers` / `usage` 原样透出，menshen 的判定与计费解析不用改

接入 menshen 的完整步骤见 [docs/clef-gateway.md](../../docs/clef-gateway.md)。

## 文件

| 文件 | 作用 |
|---|---|
| `worker.js` | Worker 全部逻辑，唯一需要部署的文件 |
| `wrangler.jsonc` | AI binding 与部署配置 |
| `package.json` | wrangler devDependency 与脚本 |
| `test/worker.test.js` | 离线单测（桩 AI binding，不连云、不花钱） |

## 部署

前置：Cloudflare 账号已开通 Workers 与 Workers AI；本机 `npx wrangler login`
（或在环境里给一个有 Workers Scripts 编辑权限的 `CLOUDFLARE_API_TOKEN`）。

```bash
cd cloudflare/clef-gateway
npm install
npx wrangler secret put API_TOKEN   # 自定义一个长随机串，就是 menshen 要填的 api_key
npm run deploy
```

部署输出里的 `https://clef-gateway.<你的子域>.workers.dev` 就是 menshen 的
`base_url`。

环境变量（secret 或 `wrangler.jsonc` 的 `vars`）：

| 名称 | 必填 | 说明 |
|---|---|---|
| `API_TOKEN` | ✅ | 调用方 Bearer token，务必用 `wrangler secret` 存；不配则全部请求 401（fail closed） |
| `DEFAULT_MODEL` | | 请求里的模型名认不出时用哪个：`clef`（默认）或 `clef-flash` |
| `GATEWAY_ID` | | 配了就经 AI Gateway 转发，获得日志 / 限流 / 缓存 |

## 验证

```bash
# 探针：能通说明 Worker 已部署
curl -sS https://clef-gateway.<子域>.workers.dev/

# 直接问 Clef 一个决策问题
curl -sS https://clef-gateway.<子域>.workers.dev/v1/systemone \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "clef-flash",
    "state": "Checkout has been failing for every customer for the last hour.",
    "questions": {
      "urgent": { "type": "noul", "instructions": "Is this request urgent?" },
      "team": {
        "type": "choice",
        "instructions": "Which team should handle it?",
        "criteria": { "billing": "Payments", "technical": "Outages", "sales": "Plans" }
      }
    }
  }'
```

## 接口

`POST /v1/systemone`，其余路径 / 方法一律 404。

- 请求：`{ model, state, questions, images? }`，与 Clef 原生 schema 一致。
  `model` 接受 `clef` / `clef-flash`，也容错 `@cf/cloudflare/clef`、
  `cf/@cf/cloudflare/clef-flash` 这类写法。
- 响应：Clef 原始的 `{ model, answers, usage }`，不套 Workers AI REST 的
  `result` 外壳。
- 状态码：401 未鉴权；400 非法 JSON；413 超过 13 MiB；422 缺 `state`/`questions`；
  上游 4xx 原样透出；其余（含网络错误）统一 502。

## 测试

```bash
npm test        # node --test，11 个用例
```
