import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { test } from "node:test";
import { build } from "esbuild";

// toPlatformCriteria / toPlatformCreateInput build the node-selection criteria
// payload of the platform form, and the live "匹配 N 个节点" preview sends the spec
// they produce. The backend half of that contract has Go tests
// (internal/platform/node_criteria_test.go); this covers the frontend half, which
// decides what actually gets sent: every criterion the operator set is a separate
// list (ANDed by the backend) and the values inside one list are alternatives.
//
// The module is TypeScript, so it is transpiled with the esbuild that already
// ships in node_modules rather than adding a test runner dependency.

const MODULE = resolve(import.meta.dirname, "../src/features/platforms/formModel.ts");

/** Load the TS module once per test file through esbuild. */
async function loadFormModel() {
  const dir = await mkdtemp(join(tmpdir(), "prism-platform-form-"));
  const outfile = join(dir, "formModel.mjs");
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

const {
  defaultPlatformFormValues,
  listValues,
  requiredChecksToText,
  textToRequiredChecks,
  toggleListValue,
  toPlatformCriteria,
  toPlatformCreateInput,
  toPlatformUpdateInput,
} = await loadFormModel();

/** The form values of a platform whose every criterion is set. */
const filledValues = {
  ...defaultPlatformFormValues,
  name: "  filled  ",
  sticky_ttl: "168h",
  regex_filters_text: "hk\n*fast\n!slow",
  region_filters_text: "HK\nJP\n!us",
  ip_types: ["residential", "mobile"],
  purity_bands: ["clean"],
  subscription_filters: ["sub-1", "sub-2"],
  protocols: ["vless"],
};

test("every criterion is sent as its own list", () => {
  const payload = toPlatformCreateInput(filledValues);
  assert.deepEqual(payload.region_filters, ["hk", "jp", "!us"]);
  assert.deepEqual(payload.ip_types, ["residential", "mobile"]);
  assert.deepEqual(payload.purity_bands, ["clean"]);
  assert.deepEqual(payload.subscription_filters, ["sub-1", "sub-2"]);
  assert.deepEqual(payload.protocols, ["vless"]);
  // The legacy escape hatch keeps its own line-oriented rules.
  assert.deepEqual(payload.regex_filters, ["hk", "*fast", "!slow"]);
  assert.equal(payload.name, "filled");
});

test("the create payload and the preview spec carry the same criteria", () => {
  const payload = toPlatformCreateInput(filledValues);
  const spec = toPlatformCriteria(filledValues);
  for (const key of [
    "regex_filters",
    "region_filters",
    "ip_types",
    "purity_bands",
    "subscription_filters",
    "protocols",
  ]) {
    assert.deepEqual(spec[key], payload[key], `preview and create payload disagree about ${key}`);
  }
});

test("an untouched form restricts nothing", () => {
  const spec = toPlatformCriteria(defaultPlatformFormValues);
  assert.deepEqual(spec, {
    regex_filters: [],
    region_filters: [],
    ip_types: [],
    purity_bands: [],
    subscription_filters: [],
    protocols: [],
  });
});

test("region filters are lowercased but keep their negations", () => {
  const spec = toPlatformCriteria({ ...defaultPlatformFormValues, region_filters_text: "US\n!HK\n" });
  assert.deepEqual(spec.region_filters, ["us", "!hk"]);
});

test("toggling a region adds and removes exactly one line", () => {
  const added = toggleListValue("hk", "us", (value) => value.toLowerCase());
  assert.deepEqual(listValues(added), ["hk", "us"]);

  const removed = toggleListValue(added, "hk");
  assert.deepEqual(listValues(removed), ["us"]);
});

test("toggling a region never disturbs a legacy line the picker cannot show", () => {
  // The picker only renders positive regions, so a persisted "!hk" exclusion
  // must survive an operator toggling an unrelated region.
  const before = "jp\n!hk";
  const after = toggleListValue(before, "us");
  assert.deepEqual(listValues(after).sort(), ["!hk", "jp", "us"].sort());
  assert.deepEqual(listValues(toggleListValue(after, "us")).sort(), ["!hk", "jp"].sort());
});

test("an empty list is a valid update that clears the criterion", () => {
  const payload = toPlatformCreateInput({ ...defaultPlatformFormValues, name: "cleared" });
  assert.deepEqual(payload.ip_types, []);
  assert.deepEqual(payload.subscription_filters, []);
});

// ---------------------------------------------------------------------------
// Unlock requirements (quality_policy.required_checks)
//
// An unlock result is a label on a node, not an admission gate, so the form must
// send nothing at all when the operator set nothing - and PATCH replaces the
// whole quality_policy, so the fields this form does not edit have to be sent
// back untouched or they are silently cleared.
// ---------------------------------------------------------------------------

test("an untouched form sends no quality policy at all", () => {
  const payload = toPlatformCreateInput({ ...defaultPlatformFormValues, name: "no-gate" });
  assert.equal(payload.quality_policy, undefined);
});

test("required checks round-trip through the line-based form value", () => {
  const text = requiredChecksToText({ netflix: "available", chatgpt: "blocked" });
  // Sorted, so the same policy always renders the same text and the dirty check
  // stays stable.
  assert.equal(text, "chatgpt:blocked\nnetflix:available");
  assert.deepEqual(textToRequiredChecks(text), { chatgpt: "blocked", netflix: "available" });
});

test("a required check without an outcome means any outcome", () => {
  assert.deepEqual(textToRequiredChecks("netflix"), { netflix: "" });
  assert.deepEqual(textToRequiredChecks("netflix\n"), { netflix: "" });
  assert.deepEqual(textToRequiredChecks(""), {});
});

test("the unlock editor keeps the policy fields it does not edit", () => {
  const current = {
    min_purity: 70,
    allowed_verdicts: ["favorable"],
    required_checks: { netflix: "available" },
  };
  const payload = toPlatformUpdateInput(
    { ...defaultPlatformFormValues, name: "gated", required_checks_text: "chatgpt:blocked" },
    current,
  );
  assert.deepEqual(payload.quality_policy, {
    min_purity: 70,
    allowed_verdicts: ["favorable"],
    required_checks: { chatgpt: "blocked" },
  });
});

test("clearing every unlock requirement leaves the rest of the policy alone", () => {
  const current = { min_purity: 70, required_checks: { netflix: "available" } };
  const payload = toPlatformUpdateInput(
    { ...defaultPlatformFormValues, name: "ungated", required_checks_text: "" },
    current,
  );
  assert.deepEqual(payload.quality_policy, { min_purity: 70 });
  assert.equal("required_checks" in payload.quality_policy, false);
});

test("clearing the last unlock requirement on an otherwise empty policy sends nothing", () => {
  const current = { required_checks: { netflix: "available" } };
  const payload = toPlatformUpdateInput(
    { ...defaultPlatformFormValues, name: "empty-again", required_checks_text: "" },
    current,
  );
  assert.equal(payload.quality_policy, undefined);
});
