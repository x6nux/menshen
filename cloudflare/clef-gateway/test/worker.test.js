// worker.js 的离线单测：不连云、不花钱，用桩 AI binding 验证 CF API 兼容层行为。
// 运行：npm test（node --test）
import { test } from "node:test";
import assert from "node:assert/strict";
import worker from "../worker.js";

const TOKEN = "test-token";
const BASE = "https://api.example.workers.dev";

function makeEnv(overrides = {}) {
  return {
    API_TOKEN: TOKEN,
    AI: {
      calls: [],
      async run(model, input, options) {
        this.calls.push({ model, input, options });
        return { response: `ran ${model}` };
      },
    },
    ...overrides,
  };
}

function req(path, { method = "POST", body = '{"prompt":"hi"}', token = TOKEN, headers = {} } = {}) {
  const h = { ...headers };
  if (token !== null) h.authorization = `Bearer ${token}`;
  const init = { method, headers: h };
  if (method !== "GET" && method !== "HEAD") init.body = body;
  return new Request(`${BASE}${path}`, init);
}

function runPath(model = "@cf/cloudflare/clef", account = "acc123") {
  return `/client/v4/accounts/${account}/ai/run/${model}`;
}

test("GET / 探针返回 CF 风格信封", async () => {
  const res = await worker.fetch(req("/", { method: "GET" }), makeEnv());
  assert.equal(res.status, 200);
  const out = await res.json();
  assert.equal(out.success, true);
  assert.deepEqual(out.errors, []);
  assert.equal(out.result.service, "clef-gateway");
});

test("ai/run 成功后按 CF 信封返回，模型与 body 原样进 binding", async () => {
  const env = makeEnv();
  const body = JSON.stringify({
    model: "clef",
    state: "x",
    questions: { q: { type: "noul", instructions: "?" } },
  });
  const res = await worker.fetch(req(runPath(), { body }), env);
  assert.equal(res.status, 200);
  assert.deepEqual(await res.json(), {
    result: { response: "ran @cf/cloudflare/clef" },
    success: true,
    errors: [],
    messages: [],
  });
  assert.equal(env.AI.calls.length, 1);
  assert.equal(env.AI.calls[0].model, "@cf/cloudflare/clef");
  assert.deepEqual(env.AI.calls[0].input, JSON.parse(body));
});

test("路径变体：省略 /client/v4、URL 编码模型、结尾斜杠", async () => {
  const cases = [
    ["/accounts/acc123/ai/run/@cf/cloudflare/clef", "@cf/cloudflare/clef"],
    [
      "/client/v4/accounts/acc123/ai/run/%40cf%2Fmeta%2Fllama-3.1-8b-instruct",
      "@cf/meta/llama-3.1-8b-instruct",
    ],
    ["/client/v4/accounts/acc123/ai/run/@cf/cloudflare/clef-flash/", "@cf/cloudflare/clef-flash"],
  ];
  for (const [path, model] of cases) {
    const env = makeEnv();
    const res = await worker.fetch(req(path), env);
    assert.equal(res.status, 200, path);
    assert.equal(env.AI.calls[0].model, model, path);
  }
});

test("鉴权失败返回 401/10000，且不触碰模型", async () => {
  const env = makeEnv();
  for (const token of ["wrong", null, ""]) {
    const res = await worker.fetch(req(runPath(), { token }), env);
    assert.equal(res.status, 401);
    const out = await res.json();
    assert.equal(out.success, false);
    assert.equal(out.errors[0].code, 10000);
    assert.equal(out.errors[0].message, "Authentication error");
  }
  assert.equal(env.AI.calls.length, 0);
});

test("兼容 X-Auth-Key 头", async () => {
  const env = makeEnv();
  const res = await worker.fetch(
    req(runPath(), { token: null, headers: { "x-auth-key": TOKEN } }),
    env,
  );
  assert.equal(res.status, 200);
  assert.equal(env.AI.calls.length, 1);
});

test("未配置 API_TOKEN 时 fail closed", async () => {
  const env = makeEnv();
  delete env.API_TOKEN;
  const res = await worker.fetch(req(runPath()), env);
  assert.equal(res.status, 401);
});

test("未知路径/未知方法按 7003 路由不存在返回", async () => {
  const env = makeEnv();
  const cases = [
    req("/v1/systemone"),
    req(runPath(), { method: "GET" }),
    req("/client/v4/accounts/acc123/ai/other/@cf/cloudflare/clef"),
  ];
  for (const r of cases) {
    const res = await worker.fetch(r, env);
    assert.equal(res.status, 404, r.url);
    const out = await res.json();
    assert.equal(out.success, false);
    assert.equal(out.errors[0].code, 7003);
    assert.match(out.errors[0].message, /Could not route to /);
  }
  assert.equal(env.AI.calls.length, 0);
});

test("配置 ACCOUNT_ID 后账号不匹配按路由不存在", async () => {
  const env = makeEnv({ ACCOUNT_ID: "acc123" });
  assert.equal((await worker.fetch(req(runPath("@cf/cloudflare/clef", "acc123")), env)).status, 200);
  assert.equal((await worker.fetch(req(runPath("@cf/cloudflare/clef", "other")), env)).status, 404);
});

test("空 body / 非法 JSON 返回 400 信封", async () => {
  const env = makeEnv();
  const empty = await worker.fetch(req(runPath(), { body: "" }), env);
  assert.equal(empty.status, 400);
  assert.equal((await empty.json()).errors[0].code, 3003);

  const bad = await worker.fetch(req(runPath(), { body: "{oops" }), env);
  assert.equal(bad.status, 400);
  assert.equal((await bad.json()).success, false);
  assert.equal(env.AI.calls.length, 0);
});

test("stream: true 明确拒绝并提示不支持", async () => {
  const env = makeEnv();
  const res = await worker.fetch(req(runPath(), { body: '{"stream":true,"prompt":"x"}' }), env);
  assert.equal(res.status, 400);
  assert.match((await res.json()).errors[0].message, /Streaming is not supported/);
  assert.equal(env.AI.calls.length, 0);
});

test("绑定错误按官方 Workers AI 错误表映射状态码", async () => {
  const cases = [
    [{ code: 3036, message: "used up free allocation" }, 429, 3036],
    [{ code: 5007, message: "No such model" }, 400, 5007],
    [{ code: 3042, message: "invalid model id" }, 404, 3042],
    [{ code: 5035, message: "requires paid plan" }, 403, 5035],
    [{ status: 422, message: "no code" }, 422, 1000],
    [new Error("boom"), 500, 1000],
  ];
  for (const [thrown, status, code] of cases) {
    const env = makeEnv({
      AI: {
        run: async () => {
          throw thrown;
        },
      },
    });
    const res = await worker.fetch(req(runPath()), env);
    assert.equal(res.status, status, thrown.message);
    const out = await res.json();
    assert.equal(out.success, false);
    assert.equal(out.errors[0].code, code);
  }
});

test("GATEWAY_ID 透传 AI Gateway 选项", async () => {
  const env = makeEnv({ GATEWAY_ID: "gw1" });
  await worker.fetch(req(runPath()), env);
  assert.deepEqual(env.AI.calls[0].options, { gateway: { id: "gw1" } });

  const env2 = makeEnv();
  await worker.fetch(req(runPath()), env2);
  assert.equal(env2.AI.calls[0].options, undefined);
});

test("result 原样透出（含字符串结果）", async () => {
  const env = makeEnv({ AI: { run: async () => "plain text" } });
  const out = await (await worker.fetch(req(runPath()), env)).json();
  assert.equal(out.result, "plain text");
  assert.equal(out.success, true);
});
