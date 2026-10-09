#!/usr/bin/env node
/*
 * The anti-pattern gate for the console.
 *
 * Impeccable's deterministic detector (https://github.com/pbakaus/impeccable, 61
 * checks, no model, no API key) reports the defaults an agent reaches for before a
 * design exists: the thick coloured side border, gradient text, a zero-offset glow,
 * a pulsing status dot, two-axis grid textures. Those are the exact tells the design
 * system forbids, so the forbidden list is checkable rather than a matter of taste.
 *
 * Four things this wrapper exists to get right, each learned by getting it wrong:
 *
 * 1. The detector does NOT read `.tsx`. Handed a directory of React source it
 *    returns nothing at all, which looks exactly like a clean pass and is not one.
 *    So the source-level scan covers the files it can read — the stylesheet and the
 *    HTML shell — and the strongest scan is over a built tree.
 *
 * 2. A build scan needs resolvable asset paths. The production `index.html` links
 *    `/ui/assets/*` because the server mounts the bundle at `/ui/`. Pointed straight
 *    at `dist/`, the detector warns "could not read linked stylesheet … color and
 *    custom-property rules will be incomplete", exits 2, and silently skips its
 *    colour and custom-property checks — a green result that checked almost nothing.
 *    This script assembles a scan root where those paths resolve.
 *
 * 3. A 0 is only meaningful if the detector can fail. `--strength` injects a
 *    violation into a copy of `design.css` and requires the detector to report it,
 *    which proves the stylesheet is being read at all.
 *
 * 4. Suppression needs attribution, not a filename pattern. A finding outside our
 *    source is only excused if the file that contains it actually carries the
 *    third-party marker; the excuse and its evidence are printed.
 *
 * Fails closed: no detector, or a detector that cannot see our stylesheet, is an
 * error rather than a pass.
 *
 * Usage:
 *   node scripts/check-slop.mjs              # scan the build if present, else source
 *   node scripts/check-slop.mjs --source     # force the source-level scan
 *   node scripts/check-slop.mjs --strength   # prove the detector works, then scan
 */

import { execFileSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { basename, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const web = join(here, "..");
const dist = join(web, "dist");
const stylesheet = join(web, "src", "styles", "design.css");
const shell = join(web, "index.html");

const args = new Set(process.argv.slice(2));
const forceSource = args.has("--source");
const useStrength = args.has("--strength");

/* ---- locating the detector ---------------------------------------------- */

function findEngine() {
  const candidates = [];
  if (process.env.IMPECCABLE_BIN) candidates.push(process.env.IMPECCABLE_BIN);
  for (const dir of [process.env.IMPECCABLE_SKILL_DIR, join(homedir(), "impeccable", "skill")]) {
    if (dir) candidates.push(join(dir, "scripts", "impeccable"));
  }
  const pinned = join(homedir(), ".impeccable", "bin");
  if (existsSync(pinned)) {
    for (const version of readdirSync(pinned)) candidates.push(join(pinned, version, "impeccable"));
  }
  candidates.push("impeccable");
  for (const candidate of candidates) {
    try {
      const reply = execFileSync(candidate, ["engine-probe"], { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] });
      if (reply.startsWith("impeccable-engine")) return candidate;
    } catch {
      // Not this one. Probing is how the launcher itself decides, and a launcher
      // reached without IMPECCABLE_SKILL_DIR cannot find reference/*.md.
    }
  }
  return null;
}

const engine = findEngine();
if (!engine) {
  console.error("check-slop: the impeccable detector is not installed, and this check fails closed rather than skipping.");
  console.error("  npx impeccable install          # or:  npx skills add pbakaus/impeccable");
  console.error("  then re-run, or point IMPECCABLE_BIN at the engine binary.");
  process.exit(1);
}

const env = {
  ...process.env,
  IMPECCABLE_SKILL_DIR:
    process.env.IMPECCABLE_SKILL_DIR ??
    (basename(dirname(engine)) === "bin" ? join(engine, "..", "..", "..", "impeccable", "skill") : join(dirname(engine), "..")),
};

/* ---- running it --------------------------------------------------------- */

/** Runs the detector and returns { code, output }. A finding exits 2, not 1. */
function detect(targets) {
  try {
    return { code: 0, output: execFileSync(engine, ["detect", ...targets], { encoding: "utf8", env, stdio: ["pipe", "pipe", "pipe"] }) };
  } catch (error) {
    return { code: error.status ?? 1, output: `${error.stdout ?? ""}${error.stderr ?? ""}` };
  }
}

/**
 * Parses the report into findings. The format is a bare path line followed by
 * indented `line N: [rule] detail` rows — and, for the rules the engine decides on
 * the whole file rather than on one declaration, an indented `[rule] detail` row with
 * no line number.
 *
 * Both shapes are read. The first version of this file only understood the first, so
 * a file-scoped rule (`ai-color-palette`, `cream-palette`, the font tells) parsed as
 * zero findings and the gate printed "clean" while the engine had reported one — a
 * false clean, which is the one failure mode a gate must not have.
 */
function parse(output) {
  const findings = [];
  let file = null;
  for (const raw of output.split("\n")) {
    const line = raw.replace(/\r$/, "");
    if (!line.trim()) continue;
    const lineScoped = /^\s+line \d+: \[([^\]]+)\]/.exec(line);
    if (lineScoped) {
      findings.push({ file, rule: lineScoped[1], text: line.trim(), scope: "line" });
      continue;
    }
    const fileScoped = /^\s+\[([^\]]+)\]\s*(.*)$/.exec(line);
    if (fileScoped) {
      findings.push({ file, rule: fileScoped[1], text: line.trim(), scope: "file" });
      continue;
    }
    if (/^\s/.test(line)) continue; // rule text, arrows, advisories
    if (line.startsWith("/") || /^[A-Za-z]:[\\/]/.test(line)) file = line;
  }
  return findings;
}

const missingCss = (output) => /could not read linked stylesheet/.test(output);

/* ---- 1. prove the detector can see our stylesheet ------------------------ */

if (useStrength) {
  const probe = mkdtempSync(join(tmpdir(), "slop-strength-"));
  try {
    const css = readFileSync(stylesheet, "utf8");
    writeFileSync(
      join(probe, "design.css"),
      `${css}\n.strength-probe { border-left: 6px solid #8b5cf6; border-radius: 14px; }\n`,
    );
    const { output } = detect([probe]);
    const hit = parse(output).some((finding) => finding.rule === "side-tab");
    if (!hit) {
      console.error("check-slop: STRENGTH TEST FAILED. A deliberate `side-tab` injected into design.css was not reported,");
      console.error("so a clean scan of the real stylesheet would prove nothing. Detector output follows:");
      console.error(output.trim() || "(no output)");
      process.exit(1);
    }
    console.log("check-slop: strength test ok — a deliberate violation in design.css is reported.");
  } finally {
    rmSync(probe, { recursive: true, force: true });
  }
}

/* ---- 2. scan ------------------------------------------------------------ */

let targets;
let mode;
let cleanup = null;

if (!forceSource && existsSync(join(dist, "index.html"))) {
  // The built tree is the strongest scan: it also covers bundled chunks. The
  // scan root must hold `ui/assets/`, because that is what the HTML links.
  const root = mkdtempSync(join(tmpdir(), "slop-scan-"));
  cpSync(dist, join(root, "ui"), { recursive: true });
  targets = [root];
  mode = "build";
  cleanup = () => rmSync(root, { recursive: true, force: true });
} else {
  // Separate argv entries. Joined into one string, the detector can access neither
  // path, prints a `cannot access` warning and reports a clean scan of nothing.
  targets = [stylesheet, shell].filter(existsSync);
  mode = "source";
}

if (targets.length === 0) {
  console.error("check-slop: nothing to scan — neither the stylesheet nor the HTML shell was found.");
  process.exit(1);
}
const { code, output } = detect(targets);

if (missingCss(output)) {
  cleanup?.();
  console.error("check-slop: the detector could not read a linked stylesheet, so its colour and");
  console.error("custom-property checks did not run. The asset paths in the scan root are wrong:");
  console.error(output.trim());
  process.exit(1);
}

const findings = parse(output);

/* ---- 3. attribute anything that is not ours ----------------------------- */

/**
 * A finding is excused only for a reason that is written down, and only after the file
 * holding it has been read. Matching the filename alone would excuse a regression that
 * happens to land in the same file; reading it does not.
 *
 * Two kinds of reason exist, and both are printed on every run so the choice is visible
 * rather than silent:
 *
 *   - **provenance**: the code is not ours (a vendored chunk);
 *   - **decision**: the code is ours and the pattern is deliberate, recorded in the
 *     root `DESIGN.md` with its bound.
 */
function attribution(finding) {
  let body = null;
  const read = () => {
    if (body !== null) return body;
    try {
      body = readFileSync(finding.file, "utf8");
    } catch {
      body = "";
    }
    return body;
  };

  /*
   * The detector reads the shell and its linked stylesheet as one palette, and reports
   * a violet/indigo accent as an "AI tell". This console's accent *is* an indigo→violet
   * pair, because the operator pinned that look in a mockup, and the design system
   * states that decision and its bounds. The engine cannot know that; a reader of this
   * output can, which is why the finding is still printed.
   *
   * The exemption is narrow on purpose: one rule, our own shell only (read, then
   * checked), and any second palette finding still fails the gate.
   */
  if (finding.rule === "ai-color-palette" && /(^|\/)ui\/index\.html$/.test(finding.file ?? "")) {
    return /prism/i.test(read())
      ? "our own shell; the indigo→violet accent is pinned by the operator's brief and bounded in DESIGN.md"
      : null;
  }

  /*
   * There used to be a `radial-halo` excuse here: the reference board's hero band
   * was drawn with a handful of soft radial stops, the detector read that wash as
   * a decorative AI halo, and the excuse held only while the shell still carried
   * that band (it looked for the stop inside `.wb-slot-a` in the source
   * stylesheet).
   *
   * The band went with the 1536px replica layer, and the detector no longer
   * reports the finding at all - measured, not assumed: the build scan is clean
   * without it. So the excuse is gone rather than left pointing at a rule that no
   * longer exists. If a radial halo does come back, it is an unexcused finding and
   * fails the gate, which is the correct reading.
   */

  return null;
}

const ours = [];
const excused = [];
for (const finding of findings) {
  const reason = attribution(finding);
  if (reason) excused.push({ ...finding, reason });
  else ours.push(finding);
}
// Classification reads the files a finding points at, so the temporary scan root has
// to outlive it. Cleaning up before this point un-attributes every vendored finding
// and reports third-party code as ours.
cleanup?.();

for (const finding of excused) {
  console.log(`check-slop: known, attributed — ${finding.rule} in ${basename(finding.file ?? "")}`);
  console.log(`  ${finding.reason}`);
}

if (ours.length > 0) {
  console.error(`check-slop: ${ours.length} anti-pattern(s) in a file we own (${mode} scan):`);
  for (const finding of ours) {
    console.error(`  ${finding.file}: ${finding.text}`);
  }
  process.exit(1);
}

console.log(`check-slop: clean — 0 anti-patterns in the ${mode} scan (exit ${code}).`);
if (mode === "source") {
  console.log("check-slop: note — the detector does not read .tsx, so this scan covers the stylesheet and the HTML shell.");
  console.log("check-slop: run it after `npm run build` for the build scan, which also covers the bundled chunks.");
}
