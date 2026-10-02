# clef-gateway

**Cloudflare API v4 兼容层**：用本 Worker 的 AI binding 执行 Workers AI 模型，
对外模拟 `api.cloudflare.com` 的调用形态。任何按 Cloudflare REST API 写的客户端、
SDK 或中转服务，把 base URL 指到本 Worker、token 填这里的 `API_TOKEN`，就能调用
本账号的 Workers AI —— **真实 Cloudflare 凭据不出 Worker**。模型不限，Clef 只是
其中之一，README 里的示例以它为主。

```text
调用方（CF API 客户端） ──Bearer API_TOKEN──▶ Worker ──AI binding──▶ Workers AI
```

## 接口

```http
POST [/client/v4]/accounts/{account_id}/ai/run/{model}
Authorization: Bearer <API_TOKEN>
Content-Type: application/json
```

成功（与 Cloudflare 一致）：

```json
{ "result": { "...": "模型输出" }, "success": true, "errors": [], "messages": [] }
```

失败：

```json
{
  "result": null,
  "success": false,
  "errors": [{ "code": 10000, "message": "Authentication error" }],
  "messages": []
}
```

路由细节：`/client/v4` 前缀可省略；`model` 可含 `/`，也可 URL 编码
（`%40cf%2Fcloudflare%2Fclef`）；结尾斜杠容忍。

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
npx wrangler secret put API_TOKEN   # 自定义一个长随机串，就是调用方要填的 token
npm run deploy
```

部署输出里的 `https://clef-gateway.<你的子域>.workers.dev` 就是新的 API 入口。

## 用法

```bash
TOKEN=<API_TOKEN>
BASE=https://clef-gateway.<子域>.workers.dev/client/v4
ACCOUNT=<你的 Cloudflare 账号 ID>   # 未配置 ACCOUNT_ID 变量时任意占位即可

# Clef 决策模型
curl -sS "$BASE/accounts/$ACCOUNT/ai/run/@cf/cloudflare/clef" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "clef",
    "state": "Checkout has been failing for every customer for the last hour.",
    "questions": {
      "urgent": { "type": "noul", "instructions": "Is this request urgent?" },
      "team": {
        "type": "choice",
        "instructions": "Which team should handle it?",
        "criteria": { "billing": "Payments", "invoice": "Invoices", "tech": "Outages" }
      }
    }
  }'

# 任意 Workers AI 模型
curl -sS "$BASE/accounts/$ACCOUNT/ai/run/@cf/meta/llama-3.1-8b-instruct" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{ "prompt": "Where did Hello World come from?" }'
```

支持自定义 base URL 的 Cloudflare SDK / 中转程序：base 填
`https://clef-gateway.<子域>.workers.dev/client/v4`，API token 填 `API_TOKEN`。

## 环境变量

| 名称 | 必填 | 说明 |
|---|---|---|
| `API_TOKEN` | ✅ | 调用方 Bearer token，务必用 `wrangler secret` 存；不配则所有请求 401（fail closed） |
| `ACCOUNT_ID` | | 配了就把 URL 里的 account 段钉死到该值，填错按路由不存在处理；不配则任意段都接受 |
| `GATEWAY_ID` | | 配了就经 AI Gateway 转发，获得日志 / 限流 / 缓存 |

## 兼容性与限制

- 只实现 `POST .../ai/run/{model}` 一个端点；`models/search`、`tasks/search`
  等其他 Cloudflare 端点未实现。
- 仅接受 JSON 请求体；二进制 / multipart 上传不支持。
- **不支持 `"stream": true`**：AI binding 只能一次性返回完整结果，无法输出 SSE；
  带 `stream: true` 的请求会得到明确的 400 而不是一段解析不了的响应。
- 错误码：绑定抛出的 `code` 按官方
  [Workers AI Errors 表](https://developers.cloudflare.com/workers-ai/platform/errors/)
  映射到 HTTP 状态码；兼容层自身沿用 10000（鉴权）/ 7003（路由）/ 3003（空 body）/
  1000（其他请求错误）。
- 鉴权同时接受 `Authorization: Bearer` 与老式 `X-Auth-Key` 头。

## 安全

- `API_TOKEN` 持有者等于拥有本账号 Workers AI 的调用权（费用记在本账号），
  只发给可信调用方；建议叠加 Cloudflare WAF Rate Limiting 防刷。
- Worker 代码与配置里没有任何 Cloudflare 凭据，绑定权限由平台注入。

## 测试

```bash
npm test        # node --test，13 个用例
```
