/**
 * clef-gateway —— 把 Cloudflare Workers AI 上的 Clef 决策模型
 * （@cf/cloudflare/clef / clef-flash）包装成 System One 兼容端点，
 * 供 menshen 作为 systemone 判定上游直接接入。
 *
 * 调用方（menshen）的契约：
 *   POST {base_url}/v1/systemone
 *   Authorization: Bearer <api_key>
 *   { "model": "clef", "state": ..., "questions": {...}, "images": [...]? }
 *   响应：{ "model": ..., "answers": {...}, "usage": {...} }
 *
 * Workers AI 的 REST 形态是 /accounts/{id}/ai/run/@cf/cloudflare/clef，
 * 且请求体里的 model 只能是 "clef" / "clef-flash"。这个 Worker 用 AI
 * binding 抹掉这些差异：鉴权、模型名归一化、其余字段原样进、原样出。
 *
 * 单文件、零依赖。部署与接入说明见同目录 README.md 与 docs/clef-gateway.md。
 */

const TARGETS = {
  clef: "@cf/cloudflare/clef",
  "clef-flash": "@cf/cloudflare/clef-flash",
};

// Clef 单请求上限 13 MiB（图片 base64 决定了请求体可能远大于普通 JSON）。
const MAX_BODY_BYTES = 13 * 1024 * 1024;

/**
 * 从任意模型写法里认回 clef / clef-flash：
 * "clef"、"clef-flash"、"@cf/cloudflare/clef"、"cf/@cf/cloudflare/clef-flash"
 * 都能认，认不出就用 DEFAULT_MODEL，默认 clef。
 */
function pickModel(raw, fallback) {
  const s = String(raw ?? "").toLowerCase();
  if (s.includes("clef-flash")) return "clef-flash";
  if (s.includes("clef")) return "clef";
  return fallback === "clef-flash" ? "clef-flash" : "clef";
}

function bearerToken(request) {
  const h = request.headers.get("authorization") ?? "";
  return h.startsWith("Bearer ") ? h.slice(7).trim() : "";
}

/** 定长逐字节比较：避免 token 被响应时间逐字符试探。长度不同直接失败。 */
function timingSafeEqual(a, b) {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

function json(data, status = 200) {
  return new Response(JSON.stringify(data), {
    status,
    headers: { "content-type": "application/json; charset=utf-8" },
  });
}

export default {
  async fetch(request, env) {
    const { pathname } = new URL(request.url);

    // 无副作用探针：部署完 GET 一下即可确认路由与 Worker 名称。
    if (request.method === "GET" && (pathname === "/" || pathname === "/healthz")) {
      return json({ service: "clef-gateway", endpoint: "POST /v1/systemone" });
    }

    if (request.method !== "POST" || pathname !== "/v1/systemone") {
      return json({ error: "not_found" }, 404);
    }

    // 没配 API_TOKEN 就整体拒绝（fail closed），避免垫片裸奔在公网上。
    if (!env.API_TOKEN || !timingSafeEqual(bearerToken(request), env.API_TOKEN)) {
      return json({ error: "unauthorized" }, 401);
    }

    const declared = Number(request.headers.get("content-length") ?? 0);
    if (declared > MAX_BODY_BYTES) {
      return json({ error: "payload_too_large", limit_bytes: MAX_BODY_BYTES }, 413);
    }

    const raw = await request.text();
    if (raw.length > MAX_BODY_BYTES) {
      return json({ error: "payload_too_large", limit_bytes: MAX_BODY_BYTES }, 413);
    }

    let body;
    try {
      body = JSON.parse(raw);
    } catch {
      return json({ error: "invalid_json" }, 400);
    }
    if (body === null || typeof body !== "object" || Array.isArray(body)) {
      return json({ error: "invalid_json" }, 400);
    }
    // state / questions 交给 Clef 校验细节；这里只挡下必然失败的请求，
    // 免得白花一次模型调用。
    if (!body.state || !body.questions || typeof body.questions !== "object") {
      return json({ error: "state_and_questions_required" }, 422);
    }

    const model = pickModel(body.model, env.DEFAULT_MODEL);
    const input = { ...body, model };
    const options = env.GATEWAY_ID ? { gateway: { id: env.GATEWAY_ID } } : undefined;

    try {
      const out = await env.AI.run(TARGETS[model], input, options);
      // binding 直接返回 { model, answers, usage }，不套 REST 的 result 外壳，
      // menshen 的 judgeSystemOne / ExtractUsage 正是按这个形状解析。
      return json(out);
    } catch (err) {
      // 模型校验类 4xx 原样透出（调用方不该重试），其余统一 502（可重试）。
      const status = Number(err?.status);
      const out = status >= 400 && status < 600 ? status : 502;
      return json({ error: "upstream_error", detail: String(err?.message ?? err) }, out);
    }
  },
};
