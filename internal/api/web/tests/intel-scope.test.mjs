import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { test } from "node:test";
import { build } from "esbuild";

// buildBulkIntelScope / hasUnsupportedFilters turn the node-pool URL into the
// scope of a bulk intel job. The backend half of that contract has tests
// (cmd/prism/intel_scope_branches_test.go); this covers the frontend half, which
// decides what actually gets sent.
//
// The module is TypeScript, so it is transpiled with the esbuild that already
// ships in node_modules rather than adding a test runner dependency.

const MODULE = resolve(import.meta.dirname, "../src/features/nodes/intelScope.ts");

/** Load the TS module once per test file through esbuild. */
async function loadIntelScope() {
  const dir = await mkdtemp(join(tmpdir(), "prism-intelscope-"));
  const outfile = join(dir, "intelScope.mjs");
  await build({
    entryPoints: [MODULE],
    outfile,
    bundle: true,
    format: "esm",
    platform: "neutral",
    logLevel: "silent",
  });
  const mod = await import(pathToFileURL(outfile).href);
  await rm(dir, { recursive: true, force: true });
  return mod;
}

const { buildBulkIntelScope, hasUnsupportedFilters } = await loadIntelScope();

/** Convenience: build a URLSearchParams from a plain object. */
const params = (query = {}) => new URLSearchParams(query);

test("empty URL selects everything and still carries healthy", () => {
  const scope = buildBulkIntelScope(params());
  assert.deepEqual(scope, { filter: { healthy: "true" }, all: true });
});

test("supported filters are copied and suppress the all selector", () => {
  const scope = buildBulkIntelScope(params({ protocol: "vless", region: "JP" }));
  assert.deepEqual(scope, {
    filter: { healthy: "true", protocol: "vless", region: "JP" },
  });
  assert.equal(scope.all, undefined, "all must not be sent alongside a filter");
});

test("every supported key maps to itself", () => {
  const query = {
    protocol: "trojan",
    region: "SG",
    ip_type: "residential",
    purity_band: "clean",
    verdict: "favorable",
  };
  const scope = buildBulkIntelScope(params(query));
  assert.deepEqual(scope.filter, { healthy: "true", ...query });
});

test("subscription_id and platform_id become scope selectors, not filters", () => {
  const scope = buildBulkIntelScope(
    params({ subscription_id: "sub-1", platform_id: "plat-1" }),
  );
  assert.deepEqual(scope, {
    filter: { healthy: "true" },
    subscription_ids: ["sub-1"],
    platform_ids: ["plat-1"],
  });
  assert.equal(scope.all, undefined, "a source selector already narrows the scope");
});

test("a source selector combined with a filter keeps both", () => {
  const scope = buildBulkIntelScope(
    params({ platform_id: "plat-1", protocol: "hysteria2" }),
  );
  assert.deepEqual(scope, {
    filter: { healthy: "true", protocol: "hysteria2" },
    platform_ids: ["plat-1"],
  });
});

test("blank and whitespace-only values are ignored", () => {
  const scope = buildBulkIntelScope(
    params({ protocol: "   ", region: "", subscription_id: "  " }),
  );
  assert.deepEqual(scope, { filter: { healthy: "true" }, all: true });
});

test("values are trimmed", () => {
  const scope = buildBulkIntelScope(params({ protocol: "  vless  " }));
  assert.equal(scope.filter.protocol, "vless");
});

test("healthy is always sent, even when the URL sets it to false", () => {
  // A manual intel run must never spend provider quota on unhealthy nodes, so
  // the module pins healthy=true rather than trusting the URL.
  const scope = buildBulkIntelScope(params({ healthy: "false" }));
  assert.equal(scope.filter.healthy, "true");
});

test("unsupported filters are reported so the UI can warn", () => {
  for (const key of [
    "tag_keyword",
    "tag",
    "egress_ip",
    "quality_state",
    "risk_grade",
    "purity_min",
    "purity_max",
    "confidence_min",
    "native",
    "asn",
    "country",
    "check",
    "probed_since",
  ]) {
    assert.equal(
      hasUnsupportedFilters(params({ [key]: "x" })),
      true,
      `${key} must be reported as unsupported`,
    );
  }
});

test("supported filters are not reported as unsupported", () => {
  assert.equal(
    hasUnsupportedFilters(
      params({ protocol: "vless", region: "JP", subscription_id: "s", platform_id: "p" }),
    ),
    false,
  );
  assert.equal(hasUnsupportedFilters(params()), false);
});

test("an unsupported filter still produces a usable scope", () => {
  // The scope itself stays valid; the warning is what tells the operator the job
  // covers more nodes than the visible list.
  const query = params({ tag_keyword: "tokyo", protocol: "vless" });
  assert.equal(hasUnsupportedFilters(query), true);
  assert.deepEqual(buildBulkIntelScope(query), {
    filter: { healthy: "true", protocol: "vless" },
  });
});
