/**
 * clef-gateway —— Cloudflare API v4 兼容层。
 *
 * 用本 Worker 的 AI binding 执行 Workers AI 模型，对外模拟
 * api.cloudflare.com 的调用形态：
 *
 *   POST [/client/v4]/accounts/{account_id}/ai/run/{model}
 *   Authorization: Bearer <API_TOKEN>
 *
 *   成功：{ "result": ..., "success": true,  "errors": [], "messages": [] }
 *   失败：{ "result": null, "success": false, "errors": [{ code, message }], "messages": [] }
 *
 * 任何按 Cloudflare REST API 写的客户端 / SDK / 中转服务，把 base URL 指到
 * 本 Worker、token 填 API_TOKEN，即可调用本账号的 Workers AI —— 真实
 * Cloudflare 凭据不出 Worker，调用方也拿不到。模型不限，clef 只是其中之一。
 *
 * 单文件、零依赖。部署与用法见同目录 README.md 与 docs/clef-gateway.md。
 */

// Workers AI 内部错误码 → HTTP 状态码（官方 Errors 表）。
// 绑定抛出的 AiError 带 code 时用它，比 err.status 更忠实。
const AI_CODE_STATUS = {
  3003: 400, // 请求缺头或缺 body
  3006: 413, // 请求过大
  3007: 408, // 超时
  3008: 408, // 被中止
  3023: 403, // 账号被限制
  3036: 429, // 免费额度用尽
  3039: 400, // 微调缺文件
  3040: 429, // 容量暂时超限
  3041: 403, // 私有模型未授权
  3042: 404, // 模型名非法
  5004: 400, // 输入类型错误
  5005: 405, // 不支持 LoRa
  5007: 400, // 没有这个模型
  5016: 403, // 未同意模型条款
  5018: 403, // 私有模型未授权
  5019: 405, // SDK 版本过旧
  5035: 403, // 模型需要付费计划
};

// 兼容层自身的错误沿用 Cloudflare 公共错误码。
const ERR_AUTH = { status: 401, code: 10000, message: "Authentication error" };
const ERR_ROUTE = 7003;
const ERR_GENERIC = 1000;

// Cloudflare API 的请求体上限。
const MAX_BODY_BYTES = 100 * 1024 * 1024;

// 路由：/client/v4 前缀可选；account_id 段必填；model 可含 "/" 且可能被编码。
const RUN_RE = /^(?:\/client\/v4)?\/accounts\/([^/]+)\/ai\/run\/(.+)$/;

function json(data, status = 200) {
  return new Response(JSON.stringify(data), {
    status,
    headers: { "content-type": "application/json; charset=utf-8" },
  });
}

function ok(result) {
  return json({ result, success: true, errors: [], messages: [] });
}

function fail(status, code, message) {
  return json(
    { result: null, success: false, errors: [{ code, message }], messages: [] },
    status,
  );
}

function routeNotFound(pathname) {
  return fail(
    404,
    ERR_ROUTE,
    `Could not route to ${pathname}, perhaps your object identifier is invalid?`,
  );
}

/** Bearer token；兼容老式 X-Auth-Key 头。 */
function tokenOf(request) {
  const auth = request.headers.get("authorization") ?? "";
  if (auth.startsWith("Bearer ")) return auth.slice(7).trim();
  return (request.headers.get("x-auth-key") ?? "").trim();
}

/** 定长逐字节比较：避免 token 被响应时间逐字符试探。长度不同直接失败。 */
function timingSafeEqual(a, b) {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

export default {
  async fetch(request, env) {
    const { pathname } = new URL(request.url);

    // 探针：CF 风格信封，不泄露凭据与账号信息。
    if (request.method === "GET" && (pathname === "/" || pathname === "/healthz")) {
      return ok({
        service: "clef-gateway",
        api: "Cloudflare API v4 compatible",
        endpoint: "POST /client/v4/accounts/{account_id}/ai/run/{model}",
      });
    }

    const match = RUN_RE.exec(pathname.replace(/\/+$/, ""));
    if (!match || request.method !== "POST") {
      return routeNotFound(pathname);
    }

    // 没配 API_TOKEN 就整体拒绝（fail closed），避免垫片裸奔在公网上。
    if (!env.API_TOKEN || !timingSafeEqual(tokenOf(request), env.API_TOKEN)) {
      return fail(ERR_AUTH.status, ERR_AUTH.code, ERR_AUTH.message);
    }

    // 可选：把 URL 里的 account_id 钉死到真实账号，填错即按路由不存在处理。
    if (env.ACCOUNT_ID && match[1] !== env.ACCOUNT_ID) {
      return routeNotFound(pathname);
    }

    let model;
    try {
      model = decodeURIComponent(match[2]);
    } catch {
      return routeNotFound(pathname);
    }
    if (!model) return routeNotFound(pathname);

    const declared = Number(request.headers.get("content-length") ?? 0);
    if (declared > MAX_BODY_BYTES) {
      return fail(413, 3006, "Request is too large");
    }

    const raw = await request.text();
    if (raw.trim() === "") {
      return fail(400, 3003, "Request is missing headers or body: body");
    }

    let input;
    try {
      input = JSON.parse(raw);
    } catch {
      return fail(400, ERR_GENERIC, "Invalid JSON body");
    }

    // AI binding 只能一次性返回完整结果，给不了 SSE。
    // 明确报错好过让流式客户端拿到一段解析不了的 JSON。
    if (input && typeof input === "object" && input.stream === true) {
      return fail(
        400,
        ERR_GENERIC,
        'Streaming is not supported by this gateway; remove "stream": true',
      );
    }

    const options = env.GATEWAY_ID ? { gateway: { id: env.GATEWAY_ID } } : undefined;

    try {
      const result = await env.AI.run(model, input, options);
      return ok(result);
    } catch (err) {
      const rawCode = err?.code;
      const code =
        rawCode != null && Number.isFinite(Number(rawCode)) ? Number(rawCode) : ERR_GENERIC;
      const status =
        AI_CODE_STATUS[code] ??
        (typeof err?.status === "number" && err.status >= 400 && err.status < 600
          ? err.status
          : 500);
      return fail(status, code, String(err?.message ?? err));
    }
  },
};
