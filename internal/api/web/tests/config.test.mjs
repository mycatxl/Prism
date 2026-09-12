import assert from "node:assert/strict";
import { once } from "node:events";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import http from "node:http";
import { test } from "node:test";
import { loadServerEnv, readServerConfig } from "../server/config.mjs";
import { createPanelServer } from "../server/http.mjs";

test("management defaults to loopback 1262, backend remains separate", () => {
  assert.deepEqual(readServerConfig({}), {
    host: "127.0.0.1",
    port: 1262,
    apiTarget: "http://127.0.0.1:2260",
  });
  assert.deepEqual(
    readServerConfig({
      PRISMX_UI_HOST: "0.0.0.0",
      PRISMX_UI_PORT: "8080",
      PRISMX_API_TARGET: "https://backend.example:8443",
    }),
    { host: "0.0.0.0", port: 8080, apiTarget: "https://backend.example:8443" },
  );
  assert.deepEqual(
    readServerConfig({
      PRISM_UI_HOST: "::1",
      PRISM_UI_PORT: "1263",
      PRISM_API_TARGET: "https://backend.example",
      PRISMX_UI_PORT: "1264",
    }),
    { host: "::1", port: 1263, apiTarget: "https://backend.example" },
  );
});

test("rejects invalid ports, secret-bearing origins and self-proxy loops", () => {
  for (const port of ["0", "65536", "NaN", "1262.5", "", " 1262", "-1"])
    assert.throws(
      () => readServerConfig({ PRISM_UI_PORT: port }),
      /PRISM_UI_PORT/,
    );
  for (const target of [
    "file:///etc/passwd",
    "http://admin:secret@localhost:2260",
    "http://localhost:2260/path",
    "http://localhost:2260?secret=value",
    "http://127.0.0.1:1262",
  ])
    assert.throws(
      () => readServerConfig({ PRISM_API_TARGET: target }),
      /PRISM_API_TARGET/,
    );
  const config = readServerConfig({
    RESIN_ADMIN_TOKEN: "private",
    RESIN_PROXY_TOKEN: "private-proxy",
  });
  assert(!JSON.stringify(config).includes("private"));
});

test("dotenv parser supports quoted values and environment takes precedence", async () => {
  const dir = await mkdtemp(join(tmpdir(), "prismx-config-test-"));
  try {
    await writeFile(
      join(dir, ".env"),
      'PRISMX_UI_PORT=1262\nSECRET="with # spaces"\nPRISMX_API_TARGET=http://localhost:2260\n',
    );
    await writeFile(join(dir, ".env.local"), "PRISMX_UI_PORT=1263\n");
    const env = loadServerEnv(dir, { PRISMX_UI_PORT: "1264" });
    assert.equal(env.SECRET, "with # spaces");
    assert.equal(readServerConfig(env).port, 1264);
    assert.equal(readServerConfig(loadServerEnv(dir, {})).port, 1263);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("old and new environment names preserve file and process precedence", async () => {
  const dir = await mkdtemp(join(tmpdir(), "prism-env-migration-"));
  try {
    await writeFile(join(dir, ".env"), "PRISM_UI_PORT=1262\n");
    await writeFile(join(dir, ".env.local"), "PRISMX_UI_PORT=1263\n");
    assert.equal(readServerConfig(loadServerEnv(dir, {})).port, 1263);
    assert.equal(
      readServerConfig(loadServerEnv(dir, { PRISMX_UI_PORT: "1264" })).port,
      1264,
    );
    await writeFile(join(dir, ".env.production"), "PRISM_UI_PORT=1265\n");
    await writeFile(join(dir, ".env.production.local"), "PRISMX_UI_PORT=1266\n");
    assert.equal(readServerConfig(loadServerEnv(dir, {}, "production")).port, 1266);
    assert.equal(
      readServerConfig(loadServerEnv(dir, { PRISM_UI_PORT: "1267" }, "production")).port,
      1267,
    );
    assert.throws(() => loadServerEnv(dir, {}, "../private"), /simple name/);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("production panel serves deep links and forwards only supplied credentials", async () => {
  const dir = await mkdtemp(join(tmpdir(), "prismx-panel-test-"));
  const backend = http.createServer((request, response) => {
    response.setHeader("Content-Type", "application/json");
    response.statusCode =
      request.headers.authorization === "Bearer caller-token" ? 200 : 401;
    response.end(
      JSON.stringify({
        path: request.url,
        authorized: response.statusCode === 200,
      }),
    );
  });
  let panel;
  try {
    await mkdir(join(dir, "dist", "assets"), { recursive: true });
    await writeFile(
      join(dir, "dist", "index.html"),
      "<!doctype html><title>Prism</title><div>real-build</div>",
    );
    await writeFile(
      join(dir, "dist", "assets", "app.js"),
      "window.appLoaded=true;",
    );
    await writeFile(join(dir, ".env"), "RESIN_ADMIN_TOKEN=not-public\n");
    backend.listen(0, "127.0.0.1");
    await once(backend, "listening");
    panel = createPanelServer({
      apiTarget: `http://127.0.0.1:${backend.address().port}`,
      distDirectory: join(dir, "dist"),
    });
    panel.listen(0, "127.0.0.1");
    await once(panel, "listening");
    const origin = `http://127.0.0.1:${panel.address().port}`;
    const page = await fetch(origin + "/ui/platforms/123");
    assert.equal(page.status, 200);
    assert.match(await page.text(), /real-build/);
    assert.equal((await fetch(origin + "/ui/assets/missing.js")).status, 404);
    assert.equal((await fetch(origin + "/ui/%2eenv")).status, 404);
    assert.equal((await fetch(origin + "/.env")).status, 404);
    assert.equal((await fetch(origin + "/ui/%2e%2e%2f.env")).status, 404);
    const anonymous = await fetch(origin + "/api/v1/system/info");
    assert.equal(anonymous.status, 401);
    const authenticated = await fetch(origin + "/api/v1/nodes?limit=20", {
      headers: { Authorization: "Bearer caller-token" },
    });
    assert.equal(authenticated.status, 200);
    assert.deepEqual(await authenticated.json(), {
      path: "/api/v1/nodes?limit=20",
      authorized: true,
    });
    assert.match(authenticated.headers.get("cache-control"), /no-store/);
    assert.equal(
      (await fetch(origin + "/ui/login", { method: "POST" })).status,
      405,
    );
    const tooLarge = await fetch(origin + "/api/v1/import", {
      method: "POST",
      body: "x".repeat(1024 * 1024 + 1),
    });
    assert.equal(tooLarge.status, 413);
  } finally {
    if (panel) {
      const closed = once(panel, "close");
      panel.close();
      panel.closeAllConnections();
      await closed;
    }
    const closed = once(backend, "close");
    backend.close();
    backend.closeAllConnections();
    await closed;
    await rm(dir, { recursive: true, force: true });
  }
});
