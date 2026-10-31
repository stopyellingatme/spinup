import assert from "node:assert/strict";
import test from "node:test";

import { app } from "../src/app.ts";
import { probe } from "../src/healthcheck.ts";

test("healthz returns ok", async () => {
  const response = await app.request("http://service.test/healthz");

  assert.equal(response.status, 200);
  assert.match(response.headers.get("content-type") ?? "", /^text\/plain/);
  assert.equal(await response.text(), "ok");
});

test("sum adds supplied values", async () => {
  const response = await app.request("http://service.test/v1/sum", {
    body: JSON.stringify({ values: [1, 2.5, -0.5] }),
    headers: { "content-type": "application/json" },
    method: "POST",
  });

  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), { sum: 3 });
});

test("sum accepts an empty array", async () => {
  const response = await app.request("http://service.test/v1/sum", {
    body: JSON.stringify({ values: [] }),
    headers: { "content-type": "application/json" },
    method: "POST",
  });

  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), { sum: 0 });
});

test("sum rejects invalid request bodies", async (testContext) => {
  const invalidBodies = [
    "{}",
    '{"values": 1}',
    '{"values": [1, "two"]}',
    '{"values": [1e400]}',
    "{",
  ];

  for (const body of invalidBodies) {
    await testContext.test(body, async () => {
      const response = await app.request("http://service.test/v1/sum", {
        body,
        headers: { "content-type": "application/json" },
        method: "POST",
      });

      assert.equal(response.status, 422);
    });
  }
});

test("probe accepts only a successful response", async (testContext) => {
  await testContext.test("200", async () => {
    const result = await probe(8080, async (input, init) => {
      assert.equal(input, "http://127.0.0.1:8080/healthz");
      assert.ok(init?.signal instanceof AbortSignal);
      return new Response(null, { status: 200 });
    });

    assert.equal(result, true);
  });

  await testContext.test("500", async () => {
    assert.equal(await probe(8080, async () => new Response(null, { status: 500 })), false);
  });

  await testContext.test("throw", async () => {
    assert.equal(await probe(8080, async () => Promise.reject(new Error("offline"))), false);
  });
});
