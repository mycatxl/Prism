import assert from "node:assert/strict";
import test from "node:test";
import { build } from "esbuild";
import { createRequire } from "node:module";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

test("reachable local API works when the browser reports no internet", async () => {
  const directory = await mkdtemp(join(tmpdir(), "prism-reachability-"));
  const originalNavigator = Object.getOwnPropertyDescriptor(globalThis, "navigator");
  const originalFetch = globalThis.fetch;
  try {
    const outfile = join(directory, "client.cjs");
    await build({ stdin: { resolveDir: dirname(dirname(fileURLToPath(import.meta.url))), loader: "ts", contents: `
      export {apiRequest} from './src/lib/api-client';
      export {queryClient} from './src/lib/query-client';
      export {onlineManager} from '@tanstack/react-query';
    ` }, bundle: true, platform: "node", format: "cjs", define: { "import.meta.env": "{}" }, outfile, logLevel: "silent" });
    const { apiRequest, queryClient, onlineManager } = createRequire(import.meta.url)(outfile);
    Object.defineProperty(globalThis, "navigator", { configurable: true, value: { onLine: false } });
    let calls = 0;
    globalThis.fetch = async () => { calls++; return new Response(JSON.stringify({ ok: true }), { headers: { "Content-Type": "application/json" } }); };
    try {
      assert.deepEqual(await apiRequest("/api/v1/local", { method: "POST" }), { ok: true });
      onlineManager.setOnline(false);
      const timeout = AbortSignal.timeout(1000);
      const result = await Promise.race([
        queryClient.fetchQuery({ queryKey: ["reachability"], queryFn: () => apiRequest("/api/v1/local") }),
        new Promise((_, reject) => timeout.addEventListener("abort", () => reject(new Error("reachable query was paused")))),
      ]);
      assert.deepEqual(result, { ok: true });
      assert.equal(calls, 2);
    } finally {
      queryClient.clear();
      onlineManager.setOnline(true);
    }
  } finally {
    globalThis.fetch = originalFetch;
    if (originalNavigator) Object.defineProperty(globalThis, "navigator", originalNavigator);
    else delete globalThis.navigator;
    await rm(directory, { recursive: true, force: true });
  }
});
