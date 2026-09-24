import assert from "node:assert/strict";
import { once } from "node:events";
import http from "node:http";
import { test } from "node:test";
import { createPanelServer } from "../server/http.mjs";

test("chunked oversized requests retain their 413 response after upstream cancellation", async () => {
  const backend = http.createServer((request, response) => {
    request.resume();
    request.on("end", () => response.end("ok"));
  });
  backend.listen(0, "127.0.0.1");
  await once(backend, "listening");
  const panel = createPanelServer({
    apiTarget: `http://127.0.0.1:${backend.address().port}`,
    distDirectory: "/unused",
  });
  panel.listen(0, "127.0.0.1");
  await once(panel, "listening");
  try {
    const result = await new Promise((resolve, reject) => {
      const request = http.request({
        hostname: "127.0.0.1",
        port: panel.address().port,
        path: "/api/v1/import",
        method: "POST",
        headers: { "Transfer-Encoding": "chunked" },
      }, (response) => {
        let body = "";
        response.on("data", (chunk) => { body += chunk; });
        response.on("end", () => resolve({ status: response.statusCode, body }));
        response.on("error", reject);
      });
      request.on("error", reject);
      request.write(Buffer.alloc(900_000, "x"));
      request.end(Buffer.alloc(900_000, "x"));
    });
    assert.equal(result.status, 413);
    assert.equal(JSON.parse(result.body).error.code, "BODY_TOO_LARGE");
    assert.equal((await fetch(`http://127.0.0.1:${panel.address().port}/healthz`)).status, 200);
  } finally {
    for (const server of [panel, backend]) {
      const closed = once(server, "close");
      server.close();
      server.closeAllConnections();
      await closed;
    }
  }
});
