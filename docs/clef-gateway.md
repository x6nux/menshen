# CF API 兼容层（clef-gateway）

`cloudflare/clef-gateway/` 是仓库附带的一个单文件 Worker：把 Cloudflare
Workers AI 包成 **Cloudflare API v4 兼容**的入口。任何按 Cloudflare REST API
写的客户端、SDK 或中转服务，把 base URL 指到 Worker、token 填 Worker 的
`API_TOKEN`，就能调用本账号的 Workers AI —— **真实 Cloudflare 凭据不出 Worker**。

它最初是为 Clef 决策模型做的 System One 垫片，现已改为通用兼容层：
Clef（`@cf/cloudflare/clef` / `clef-flash`）与任何其他 Workers AI 模型一视同仁。

```text
调用方 ──Bearer API_TOKEN──▶ clef-gateway (Worker) ──AI binding──▶ Workers AI
         POST [/client/v4]/accounts/{account_id}/ai/run/{model}
```

## 接口

请求：

```http
POST [/client/v4]/accounts/{account_id}/ai/run/{model}
Authorization: Bearer <API_TOKEN>
Content-Type: application/json

{ ...模型输入 }
```

响应与 Cloudflare 一致：成功 `{result, success: true, errors: [], messages: []}`，
失败 `{result: null, success: false, errors: [{code, message}], messages: []}`。
`/client/v4` 前缀可省略，`model` 可 URL 编码，结尾斜杠容忍。

## 部署

```bash
cd cloudflare/clef-gateway
npm install
npx wrangler secret put API_TOKEN   # 记下这个串
npm run deploy
```

完整说明（环境变量、限制、安全）见
[cloudflare/clef-gateway/README.md](../cloudflare/clef-gateway/README.md)。

## 用法：跑一次 Clef 决策

```bash
TOKEN=<API_TOKEN>
BASE=https://clef-gateway.<子域>.workers.dev/client/v4

curl -sS "$BASE/accounts/<ACCOUNT_ID>/ai/run/@cf/cloudflare/clef" \
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
```

返回（Clef 输出包在 `result` 里，其余照 CF 信封）：

```json
{
  "result": {
    "model": "clef",
    "answers": {
      "urgent": { "type": "noul", "noul": 0.98 },
      "team": {
        "type": "choice",
        "choice": "tech",
        "probabilities": { "billing": 0.01, "invoice": 0.01, "tech": 0.98 },
        "confidence": 0.98
      }
    },
    "usage": { "input_tokens": 142, "output_tokens": 12 }
  },
  "success": true,
  "errors": [],
  "messages": []
}
```

## 兼容矩阵

| 能力 | 状态 |
|---|---|
| `POST .../ai/run/{model}`（`/client/v4` 可省略、model 可编码） | ✅ |
| CF 信封与 HTTP 状态码 | ✅ |
| Workers AI 官方错误码映射 | ✅（按官方 Errors 表） |
| `Authorization: Bearer` 与 `X-Auth-Key` | ✅ |
| 任意 Workers AI 模型（不限 Clef） | ✅ |
| `models/search`、`tasks/search` 等其他端点 | ❌ 未实现 |
| `"stream": true`（SSE） | ❌ 明确 400，AI binding 无法流式 |
| 二进制 / multipart 请求体 | ❌ 仅 JSON |

## 与 menshen 的关系

menshen 的判定上游只认 OpenAI chat 与 SystemOne 两种协议，因此**这个 Worker
目前不能直接接进 menshen**。要在 menshen 里用上它，需要在 `internal/upstream`
增加一类 `cfapi` 端点（请求发 `/accounts/{id}/ai/run/{model}`、按 CF 信封解析
`result`），目前尚未实现。

> 只是想让 menshen 用上 Clef 的话，更省事的是恢复 SystemOne 垫片版本
> （git 历史 `cc3b828`，仍是单文件 Worker），改改几行即可。

## 排错

| 信封 code | HTTP | 含义 / 处置 |
|---|---|---|
| 10000 | 401 | token 与 Worker 的 `API_TOKEN` 不一致，或 secret 未生效（重新 `wrangler secret put` 后 deploy） |
| 7003 | 404 | 路径不对；或配置了 `ACCOUNT_ID` 但 URL 里的账号对不上 |
| 3003 | 400 | 请求体为空 |
| 5007 | 400 | 模型名不存在（注意大小写与 `@cf/` 前缀） |
| 3042 | 404 | 模型名非法 |
| 3036 / 3040 | 429 | 免费额度用尽 / 容量暂时超限 |
| 5035 | 403 | 该模型需要 Workers Paid 计划 |
| 3006 | 413 | 请求体过大 |
| 1000 + 自定义 message | 500/400 | 其他上游错误；`wrangler tail` 看原始信息 |

## 安全与成本

- `API_TOKEN` 持有者等价于拥有本账号 Workers AI 的调用权，费用记在本账号；
  只发给可信调用方，建议叠加 WAF Rate Limiting 防刷。
- 模型按 [Workers AI 定价](https://developers.cloudflare.com/workers-ai/platform/pricing/)
  计费（例如 Clef $0.24 / M input tokens、Clef-flash $0.09 / M）。
- 配 `GATEWAY_ID` 变量可经 AI Gateway 转发，获得日志、限流与缓存。

## 测试

```bash
cd cloudflare/clef-gateway && npm test    # 13 个用例，桩 binding，不连云
```
