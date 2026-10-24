import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { test } from "node:test";
import { build } from "esbuild";

// toPlatformCriteria / toPlatformCreateInput build the node-selection criteria
// payload of the platform form, and the live preview sends the same shape.
const MODULE = resolve(import.meta.dirname, "../src/features/platforms/formModel.ts");

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

const filledValues = {
  ...defaultPlatformFormValues,
  name: "  filled  ",
  sticky_ttl: "168h",
  region_filters_text: "HK\nJP\n!us",
  ip_types: ["residential", "mobile"],
  purity_bands: ["clean"],
  subscription_filters: ["sub-1", "sub-2"],
  protocols: ["vless"],
};

test("every current criterion is sent as its own list", () => {
  const payload = toPlatformCreateInput(filledValues);
  assert.deepEqual(payload.region_filters, ["hk", "jp", "!us"]);
  assert.deepEqual(payload.ip_types, ["residential", "mobile"]);
  assert.deepEqual(payload.purity_bands, ["clean"]);
  assert.deepEqual(payload.subscription_filters, ["sub-1", "sub-2"]);
  assert.deepEqual(payload.protocols, ["vless"]);
  assert.equal("regex_filters" in payload, false);
  assert.equal(payload.name, "filled");
});

test("the create payload and the preview spec carry the same current criteria", () => {
  const payload = toPlatformCreateInput(filledValues);
  const spec = toPlatformCriteria(filledValues);
  for (const key of ["region_filters", "ip_types", "purity_bands", "subscription_filters", "protocols"]) {
    assert.deepEqual(spec[key], payload[key], `preview and create payload disagree about ${key}`);
  }
  assert.equal("regex_filters" in spec, false);
});

test("an untouched form restricts nothing", () => {
  const spec = toPlatformCriteria(defaultPlatformFormValues);
  assert.deepEqual(spec, {
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

// Unlock requirements are the only quality policy field edited here. The rest
// of the loaded policy must survive because PATCH replaces the whole object.
test("an untouched form sends no quality policy at all", () => {
  const payload = toPlatformCreateInput({ ...defaultPlatformFormValues, name: "no-gate" });
  assert.equal(payload.quality_policy, undefined);
});

test("required checks round-trip through the line-based form value", () => {
  const text = requiredChecksToText({ netflix: "available", chatgpt: "blocked" });
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
