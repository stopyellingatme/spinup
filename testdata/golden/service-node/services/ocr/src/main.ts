import { serve } from "@hono/node-server";

import { app } from "./app.ts";

const port = Number.parseInt(process.env.PORT ?? "8080", 10);
const server = serve({ fetch: app.fetch, hostname: "0.0.0.0", port });

console.log(JSON.stringify({ app: "todo", level: "info", port, service: "ocr", message: "service started" }));

let closing = false;

function close(signal: NodeJS.Signals): void {
  if (closing) {
    return;
  }
  closing = true;
  server.close((error) => {
    if (error) {
      console.error(JSON.stringify({ error: error.message, level: "error", signal, message: "service stop failed" }));
      process.exit(1);
    }
    process.exit(0);
  });
}

process.on("SIGINT", () => close("SIGINT"));
process.on("SIGTERM", () => close("SIGTERM"));
