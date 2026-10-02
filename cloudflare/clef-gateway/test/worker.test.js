// worker.js 的离线单测：不连云、不花钱，用桩 AI binding 验证协议垫片行为。
// 运行：npm test（node --test）
import { test } from "node:test";
import assert from "node:assert/strict";
import worker from "../worker.js";

const TOKEN = "test-token";
const URL = "https://clef.example.workers.dev/v1/systemone";

function makeEnv(overrides = {}) {
  return {
    API_TOKEN: TOKEN,
    AI: {
      calls: [],
      async run(model, input, options) {
        this.calls.push({ model, input, options });
        return {
          model: input.model,
          answers: { is_ad: { type: "choice", choice: "ad", probabilities: { ad: 1 }, confidence: 0.99 } },
          usage: { input_tokens: 12, output_tokens: 3 },
        };
      },
    },
    ...overrides,
  };
}

function post(body, headers = {}) {
  return new Request(URL, {
    method: "POST",
    headers: {
      authorization: `Bearer ${TOKEN}`,
      "content-type": "application/json",
      ...headers,
    },
    body: JSON.stringify(body),
  });
}

const BODY = {
  model: "clef",
  state: { message: "buy my crypto" },
  questions: { is_ad: { type: "choice", instructions: "广告?", criteria: { ad: "是", clean: "否" } } },
};

test("GET / 探针返回服务名", async () => {
  const res = await worker.fetch(new Request("https://clef.example.workers.dev/"), makeEnv());
  assert.equal(res.status, 200);
  assert.equal((await res.json()).service, "clef-gateway");
});

test("未带/带错 token 返回 401，且不触碰模型", async () => {
  const env = makeEnv();
  for (const authorization of ["", "Bearer wrong", "Basic abc"]) {
    const res = await worker.fetch(post(BODY, { authorization }), env);
    assert.equal(res.status, 401);
  }
  assert.equal(env.AI.calls.length, 0);
});

test("未配置 API_TOKEN 时 fail closed", async () => {
  const env = makeEnv();
  delete env.API_TOKEN;
  const res = await worker.fetch(post(BODY), env);
  assert.equal(res.status, 401);
});

test("其余路径/方法返回 404", async () => {
  const env = makeEnv();
  const wrongPath = new Request("https://clef.example.workers.dev/v1/other", {
    method: "POST",
    headers: { authorization: `Bearer ${TOKEN}`, "content-type": "application/json" },
    body: "{}",
  });
  assert.equal((await worker.fetch(wrongPath, env)).status, 404);

  const wrongMethod = new Request("https://clef.example.workers.dev/v1/systemone", {
    method: "GET",
    headers: { authorization: `Bearer ${TOKEN}` },
  });
  assert.equal((await worker.fetch(wrongMethod, env)).status, 404);
});

test("模型名归一化：@cf / 前缀写法都映射回 clef|clef-flash", async () => {
  for (const [raw, model, target] of [
    ["clef", "clef", "@cf/cloudflare/clef"],
    ["clef-flash", "clef-flash", "@cf/cloudflare/clef-flash"],
    ["@cf/cloudflare/clef", "clef", "@cf/cloudflare/clef"],
    ["cf/@cf/cloudflare/clef-flash", "clef-flash", "@cf/cloudflare/clef-flash"],
  ]) {
    const env = makeEnv();
    const res = await worker.fetch(post({ ...BODY, model: raw }), env);
    assert.equal(res.status, 200);
    assert.equal(env.AI.calls[0].model, target);
    assert.equal(env.AI.calls[0].input.model, model);
  }
});

test("state / questions 原样透传，images 也不丢", async () => {
  const env = makeEnv();
  const images = ["data:image/webp;base64,AAAA", { content_type: "image/png", base64: "BBBB" }];
  const res = await worker.fetch(post({ ...BODY, images }), env);
  assert.equal(res.status, 200);
  const input = env.AI.calls[0].input;
  assert.deepEqual(input.state, BODY.state);
  assert.deepEqual(input.questions, BODY.questions);
  assert.deepEqual(input.images, images);
});

test("缺 state/questions 返回 422，且不调用模型", async () => {
  const env = makeEnv();
  for (const body of [{ model: "clef", questions: {} }, { model: "clef", state: "x" }]) {
    const res = await worker.fetch(post(body), env);
    assert.equal(res.status, 422);
  }
  assert.equal(env.AI.calls.length, 0);
});

test("非法 JSON 返回 400", async () => {
  const res = await worker.fetch(
    new Request(URL, {
      method: "POST",
      headers: { authorization: `Bearer ${TOKEN}`, "content-type": "application/json" },
      body: "{not json",
    }),
    makeEnv(),
  );
  assert.equal(res.status, 400);
});

test("上游 4xx 原样透出，其余错误归一为 502", async () => {
  const env4 = makeEnv({ AI: { run: async () => { throw Object.assign(new Error("bad criteria"), { status: 422 }); } } });
  assert.equal((await worker.fetch(post(BODY), env4)).status, 422);

  const env5 = makeEnv({ AI: { run: async () => { throw new Error("boom"); } } });
  assert.equal((await worker.fetch(post(BODY), env5)).status, 502);
});

test("配置 GATEWAY_ID 时透传 AI Gateway 选项", async () => {
  const env = makeEnv({ GATEWAY_ID: "my-gateway" });
  await worker.fetch(post(BODY), env);
  assert.deepEqual(env.AI.calls[0].options, { gateway: { id: "my-gateway" } });

  const env2 = makeEnv();
  await worker.fetch(post(BODY), env2);
  assert.equal(env2.AI.calls[0].options, undefined);
});

test("响应保持 Clef 原形（answers / usage 不被改写）", async () => {
  const res = await worker.fetch(post(BODY), makeEnv());
  const out = await res.json();
  assert.equal(out.answers.is_ad.choice, "ad");
  assert.equal(out.answers.is_ad.confidence, 0.99);
  assert.deepEqual(out.usage, { input_tokens: 12, output_tokens: 3 });
});
