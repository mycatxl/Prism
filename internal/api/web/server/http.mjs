import http from "node:http";
import https from "node:https";
import { createReadStream } from "node:fs";
import { realpath, stat } from "node:fs/promises";
import { extname, resolve, sep } from "node:path";

const types = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json",
  ".png": "image/png",
  ".svg": "image/svg+xml",
  ".woff2": "font/woff2",
  ".woff": "font/woff",
  ".ico": "image/x-icon",
};
const hops = new Set([
  "connection",
  "keep-alive",
  "proxy-authenticate",
  "proxy-authorization",
  "te",
  "trailer",
  "transfer-encoding",
  "upgrade",
  "x-forwarded-for",
  "x-forwarded-host",
  "x-forwarded-proto",
]);
function endJSON(response, status, code, message) {
  if (response.writableEnded || response.destroyed) return;
  if (response.headersSent) {
    response.destroy();
    return;
  }
  response.writeHead(status, {
    "Content-Type": "application/json; charset=utf-8",
    "Cache-Control": "no-store",
  });
  response.end(JSON.stringify({ error: { code, message } }));
}
function safeHeaders(headers) {
  const remove = new Set([
    ...hops,
    ...String(headers.connection || "")
      .split(",")
      .map((name) => name.trim().toLowerCase()),
  ]);
  return Object.fromEntries(
    Object.entries(headers).filter(([name]) => !remove.has(name.toLowerCase())),
  );
}

export function createPanelServer({ apiTarget, distDirectory }) {
  const target = new URL(apiTarget);
  const dist = resolve(distDirectory);
  const server = http.createServer(
    { maxHeaderSize: 32 * 1024 },
    async (request, response) => {
      response.setHeader("X-Content-Type-Options", "nosniff");
      response.setHeader("Referrer-Policy", "no-referrer");
      response.setHeader("X-Frame-Options", "DENY");
      let url;
      try {
        if (!request.url?.startsWith("/") || request.url.startsWith("//"))
          throw new Error();
        url = new URL(request.url, "http://panel.local");
      } catch {
        endJSON(response, 400, "INVALID_ARGUMENT", "Invalid request path");
        return;
      }

      if (url.pathname === "/healthz" || url.pathname.startsWith("/api/")) {
        if (Number(request.headers["content-length"]) > 1024 * 1024) {
          endJSON(
            response,
            413,
            "BODY_TOO_LARGE",
            "Management request exceeds 1 MiB",
          );
          return;
        }
        // Forward only caller-provided credentials. Environment tokens must never
        // authenticate anonymous visitors to this panel on their behalf.
        const upstream = (target.protocol === "https:" ? https : http).request(
          {
            protocol: target.protocol,
            hostname: target.hostname.replace(/^\[|\]$/g, ""),
            port: target.port || undefined,
            method: request.method,
            path: url.pathname + url.search,
            headers: { ...safeHeaders(request.headers), host: target.host },
          },
          (incoming) => {
            if (response.writableEnded) {
              incoming.destroy();
              return;
            }
            response.writeHead(incoming.statusCode || 502, {
              ...safeHeaders(incoming.headers),
              "cache-control": "no-store",
            });
            incoming.on("error", () => response.destroy());
            incoming.pipe(response);
          },
        );
        upstream.setTimeout(30_000, () => {
          endJSON(
            response,
            504,
            "UPSTREAM_TIMEOUT",
            "Backend request timed out",
          );
          upstream.destroy();
        });
        upstream.on("error", () =>
          endJSON(
            response,
            502,
            "BACKEND_UNAVAILABLE",
            "Unable to connect to backend",
          ),
        );
        request.on("aborted", () => upstream.destroy());
        response.on("close", () => {
          if (!response.writableEnded) upstream.destroy();
        });
        let size = 0;
        let bodyRejected = false;
        request.on("data", (chunk) => {
          if (bodyRejected) return;
          size += chunk.length;
          if (size > 1024 * 1024) {
            bodyRejected = true;
            if (!response.headersSent) response.setHeader("Connection", "close");
            endJSON(
              response,
              413,
              "BODY_TOO_LARGE",
              "Management request exceeds 1 MiB",
            );
            request.unpipe(upstream);
            upstream.destroy();
          }
        });
        request.pipe(upstream);
        return;
      }

      if (request.method !== "GET" && request.method !== "HEAD") {
        endJSON(response, 405, "METHOD_NOT_ALLOWED", "Method not allowed");
        return;
      }
      if (url.pathname === "/" || url.pathname === "/ui") {
        response.writeHead(302, {
          Location: "/ui/",
          "Cache-Control": "no-store",
        });
        response.end();
        return;
      }
      if (!url.pathname.startsWith("/ui/")) {
        endJSON(response, 404, "NOT_FOUND", "Not found");
        return;
      }
      try {
        const relative = decodeURIComponent(url.pathname.slice(4));
        if (
          relative.split(/[\\/]/).some((piece) => piece.startsWith(".")) ||
          relative.includes("\0")
        ) {
          endJSON(response, 404, "NOT_FOUND", "Not found");
          return;
        }
        const candidate = resolve(dist, relative || "index.html");
        if (!candidate.startsWith(dist + sep)) {
          endJSON(response, 404, "NOT_FOUND", "Not found");
          return;
        }
        let file = candidate;
        try {
          if (!(await stat(file)).isFile()) throw new Error();
        } catch {
          if (extname(relative) || relative.startsWith("assets/")) {
            endJSON(response, 404, "NOT_FOUND", "Not found");
            return;
          }
          file = resolve(dist, "index.html");
        }
        const realFile = await realpath(file);
        if (!realFile.startsWith((await realpath(dist)) + sep)) {
          endJSON(response, 404, "NOT_FOUND", "Not found");
          return;
        }
        const data = await stat(realFile);
        const type = types[extname(realFile)] || "application/octet-stream";
        response.writeHead(200, {
          "Content-Type": type,
          "Content-Length": data.size,
          "Cache-Control": relative.startsWith("assets/")
            ? "public, max-age=31536000, immutable"
            : "no-cache",
        });
        if (request.method === "HEAD") {
          response.end();
          return;
        }
        createReadStream(realFile)
          .on("error", () => response.destroy())
          .pipe(response);
      } catch {
        endJSON(response, 404, "NOT_FOUND", "Panel build not found");
      }
    },
  );
  server.on("connect", (_request, socket) =>
    socket.end("HTTP/1.1 405 Method Not Allowed\r\nConnection: close\r\n\r\n"),
  );
  server.on("upgrade", (_request, socket) => socket.destroy());
  server.headersTimeout = 10_000;
  server.requestTimeout = 30_000;
  server.timeout = 35_000;
  return server;
}
