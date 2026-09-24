import { existsSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { parseEnv } from "node:util";

const aliases = {
  PRISM_UI_HOST: ["PRISMX_UI_HOST"],
  PRISM_UI_PORT: ["PRISMX_UI_PORT"],
  PRISM_API_TARGET: ["PRISMX_API_TARGET", "VITE_DEV_API_TARGET"],
};

function normalizeEnv(env) {
  const values = { ...env };
  for (const [name, legacyNames] of Object.entries(aliases)) {
    if (values[name] !== undefined) continue;
    for (const legacyName of legacyNames) {
      if (env[legacyName] !== undefined) {
        values[name] = env[legacyName];
        break;
      }
    }
  }
  return values;
}

export function loadServerEnv(directory, overrides = process.env, mode) {
  const names = [".env", ".env.local"];
  if (mode) {
    if (!/^[a-zA-Z0-9_-]+$/.test(mode)) {
      throw new Error("Environment mode must be a simple name");
    }
    names.push(`.env.${mode}`, `.env.${mode}.local`);
  }
  let values = {};
  for (const name of names) {
    const path = resolve(directory, name);
    if (existsSync(path))
      values = {
        ...values,
        ...normalizeEnv(parseEnv(readFileSync(path, "utf8"))),
      };
  }
  // Normalize each layer separately so a legacy process variable can still
  // override a new-name value from .env, and vice versa.
  return { ...values, ...normalizeEnv(overrides) };
}

export function readServerConfig(input) {
  const env = normalizeEnv(input);
  const host = env.PRISM_UI_HOST?.trim() || "127.0.0.1";
  const rawPort = env.PRISM_UI_PORT ?? "1262";
  const port = Number(rawPort);
  if (
    !/^\d+$/.test(rawPort) ||
    !Number.isInteger(port) ||
    port < 1 ||
    port > 65535
  ) {
    throw new Error("PRISM_UI_PORT must be an integer from 1 to 65535");
  }
  const rawTarget =
    env.PRISM_API_TARGET || "http://127.0.0.1:2260";
  let target;
  try {
    target = new URL(rawTarget);
  } catch {
    throw new Error("PRISM_API_TARGET must be an HTTP(S) origin");
  }
  if (
    !["http:", "https:"].includes(target.protocol) ||
    target.username ||
    target.password ||
    target.pathname !== "/" ||
    target.search ||
    target.hash
  ) {
    throw new Error(
      "PRISM_API_TARGET must be an HTTP(S) origin without credentials, path or query",
    );
  }
  const localHosts = [
    "127.0.0.1",
    "localhost",
    "[::1]",
    "::1",
    "0.0.0.0",
    "[::]",
  ];
  if (
    Number(target.port || (target.protocol === "https:" ? 443 : 80)) === port &&
    (target.hostname === host ||
      (localHosts.includes(host) && localHosts.includes(target.hostname)))
  ) {
    throw new Error(
      "PRISM_API_TARGET cannot point back to the management panel port",
    );
  }
  return { host, port, apiTarget: target.origin };
}
