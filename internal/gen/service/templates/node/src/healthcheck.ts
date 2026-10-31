export async function probe(port: number, fetchImpl: typeof fetch = fetch): Promise<boolean> {
  try {
    const response = await fetchImpl(`http://127.0.0.1:${port}/healthz`, {
      signal: AbortSignal.timeout(3_000),
    });
    return response.status === 200;
  } catch {
    return false;
  }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  process.exit(await probe(Number.parseInt(process.env.PORT ?? "8080", 10)) ? 0 : 1);
}
import { fileURLToPath } from "node:url";
