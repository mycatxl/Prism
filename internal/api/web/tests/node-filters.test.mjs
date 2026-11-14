import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { test } from "node:test";
import { build } from "esbuild";

const directory = await mkdtemp(join(tmpdir(), "prism-node-filters-"));
let filters;
try {
  const outfile = join(directory, "filters.mjs");
  await build({ entryPoints: [resolve(import.meta.dirname, "../src/features/nodes/filters.ts")], outfile, bundle: true, format: "esm", platform: "neutral", logLevel: "silent" });
  filters = await import(pathToFileURL(outfile).href);
} finally {
  await rm(directory, { recursive: true, force: true });
}

test("latency shortcuts replace both range bounds and reset cursor paging", () => {
  const original = new URLSearchParams("latency_min=100&latency_max=500&page=4&cursor=old&protocol=vless&size=20&selected=node");
  const next = filters.updateNodeLatencyPreset(original, "excellent");
  assert.equal(next.get("latency_max"), "150");
  assert.equal(next.has("latency_min"), false);
  assert.equal(next.has("page"), false);
  assert.equal(next.has("cursor"), false);
  assert.equal(next.get("protocol"), "vless");
  assert.equal(next.get("size"), "20");
  assert.equal(next.get("selected"), "node");
  assert.equal(filters.nodeLatencyPreset(next), "excellent");
  assert.equal(filters.nodeLatencyPreset(filters.updateNodeLatencyPreset(next, "usable")), "usable");
  assert.equal(filters.nodeLatencyPreset(filters.updateNodeLatencyPreset(next, "slow")), "slow");
  assert.equal(filters.nodeLatencyPreset(filters.updateNodeLatencyPreset(next, "all")), "all");
  assert.equal(filters.nodeLatencyPreset(original), "custom");
  assert.equal(original.get("cursor"), "old", "URL updates must not mutate the current parameters");
});

test("egress status and exact address cannot leave contradictory filters", () => {
  const without = filters.updateNodeFilterParams(new URLSearchParams("egress_ip=203.0.113.1&page=2&cursor=x"), "has_egress_ip", "false");
  assert.equal(without.has("egress_ip"), false);
  assert.equal(without.has("cursor"), false);
  const exact = filters.updateNodeFilterParams(without, "egress_ip", "203.0.113.2");
  assert.equal(exact.has("has_egress_ip"), false);
  assert.equal(exact.get("egress_ip"), "203.0.113.2");
});

test("health presets clear legacy aliases and custom boolean filters are preserved", () => {
  const original = new URLSearchParams("enabled=false&has_outbound=true&circuit_open=false");
  assert.deepEqual(filters.nodeHealthFilters(original), { enabled: false, has_outbound: true, circuit_open: false });
  assert.equal(filters.nodeHealthMode(original), "custom");
  const next = filters.updateNodeFilterParams(original, "status", "healthy");
  assert.equal(next.has("enabled"), false);
  assert.equal(next.has("has_outbound"), false);
  assert.deepEqual(filters.nodeHealthFilters(next), { enabled: true, has_outbound: true, circuit_open: false });
  assert.deepEqual(filters.nodeHealthFilters(filters.updateNodeFilterParams(next, "status", "")), { enabled: undefined, has_outbound: undefined, circuit_open: undefined });
});

test("removing one condition preserves the remaining source and region constraints", () => {
  const next = filters.updateNodeFilterParams(new URLSearchParams("subscription_id=source&protocol=trojan&region=jp&country=JP&page=3&cursor=x"), "protocol", "");
  assert.equal(next.get("subscription_id"), "source");
  assert.equal(next.get("region"), "jp");
  assert.equal(next.get("country"), "JP");
  assert.equal(next.has("protocol"), false);
  assert.equal(next.has("page"), false);
});

test("invalid latency ranges and region codes block the query instead of widening it", () => {
  for (const draft of [{ status: "invalid" }, { latency_max: "0x10" }, { latency_max: "NaN" }, { latency_min: "-1" }, { latency_max: "3600001" }, { latency_min: "300", latency_max: "150" }, { latency_min: "150", latency_max: "150" }, { region: "Japan" }, { has_egress_ip: "maybe" }]) {
    assert.ok(filters.nodeFilterError(draft), JSON.stringify(draft));
  }
  assert.equal(filters.nodeFilterError({ latency_min: "100.5", latency_max: "300", region: "JP" }), "");
});
