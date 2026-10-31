import { Hono } from "hono";

export const app = new Hono();

app.get("/healthz", (context) => context.text("ok"));

app.post("/v1/sum", async (context) => {
  let body: unknown;
  try {
    body = await context.req.json();
  } catch {
    return context.json({ error: "invalid request body" }, 422);
  }

  if (!isSumRequest(body)) {
    return context.json({ error: "invalid request body" }, 422);
  }

  return context.json({ sum: body.values.reduce((sum, value) => sum + value, 0) });
});

function isSumRequest(body: unknown): body is { values: number[] } {
  if (typeof body !== "object" || body === null || !("values" in body)) {
    return false;
  }
  const { values } = body;
  return Array.isArray(values) && values.every((value) => typeof value === "number" && Number.isFinite(value));
}
