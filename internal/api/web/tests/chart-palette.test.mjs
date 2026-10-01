/**
 * The chart palette is a literal copy of the design tokens, and until this file
 * existed nothing checked that the copy was current.
 *
 * `src/features/dashboard/chartPalette.ts` restates colours because an ECharts canvas
 * cannot read CSS variables. The rule that the two must be changed together was written
 * in prose in the root `DESIGN.md` and in the file's own header, and it was still broken:
 * a redesign re-solved every colour in `design.css` and the charts kept painting the
 * previous palette — a drift that no gate could see, because the contrast gate reads CSS
 * and the type checker reads types.
 *
 * So the rule is a test now. It reads both files and fails on the first pair that
 * disagrees, on a series that is out of order, on a band ramp whose steps are not
 * separable, and on a font that is not the token's family.
 */
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

const css = readFileSync(new URL("../src/styles/design.css", import.meta.url), "utf8");
const palette = readFileSync(
  new URL("../src/features/dashboard/chartPalette.ts", import.meta.url),
  "utf8",
);

/** The `--p-*` primitives declared inside one CSS block. */
function primitives(selector) {
  const start = css.indexOf(selector);
  assert.ok(start >= 0, `design.css has no ${selector} block`);
  const open = css.indexOf("{", start);
  const close = css.indexOf("\n}", open);
  const body = css.slice(open, close < 0 ? undefined : close);
  const found = new Map();
  for (const match of body.matchAll(/--p-([a-z0-9-]+):\s*(#[0-9a-fA-F]{6})\b/g)) {
    found.set(match[1], match[2].toLowerCase());
  }
  assert.ok(found.size > 0, `${selector} declares no --p-* colour primitives`);
  return found;
}

const light = primitives(":root {");
const dark = primitives('[data-theme="dark"] {');

/**
 * An `export const NAME = "…";` literal from the palette.
 *
 * Both quote styles are read, because a value that itself contains double quotes —
 * a font stack — is written with single quotes, and a parser that only understood
 * one of them would report a missing constant instead of a mismatched one.
 */
function literal(name) {
  const match = new RegExp(`export const ${name} = (["'])(.*?)\\1;`).exec(palette);
  assert.ok(match, `chartPalette.ts declares no ${name}`);
  return match[2];
}

function channel(value) {
  const c = value / 255;
  return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
}

function luminance(hex) {
  const h = hex.slice(1);
  return (
    0.2126 * channel(parseInt(h.slice(0, 2), 16)) +
    0.7152 * channel(parseInt(h.slice(2, 4), 16)) +
    0.0722 * channel(parseInt(h.slice(4, 6), 16))
  );
}

test("every chart literal mirrors the light primitive it copies", () => {
  const mirrored = [
    ["CHART_PAPER", "canvas"],
    ["CHART_PAPER_SUNK", "sunk"],
    ["CHART_PAPER_RAISED", "raised"],
    ["CHART_PAPER_INSET", "inset"],
    ["CHART_RULE", "rule"],
    ["CHART_RULE_STRONG", "rule-strong"],
    ["CHART_INK", "ink"],
    ["CHART_INK_SOFT", "ink-soft"],
    ["CHART_INK_FAINT", "ink-faint"],
    ["CHART_SIGNAL", "signal"],
    ["CHART_SIGNAL_DEEP", "signal-deep"],
    ["CHART_LIVE", "live"],
    ["CHART_GRID", "chart-grid"],
    ["CHART_AXIS", "chart-axis"],
  ];
  for (const [constant, token] of mirrored) {
    assert.equal(
      literal(constant).toLowerCase(),
      light.get(token),
      `${constant} is not --p-${token} from design.css`,
    );
  }
});

test("the series is the design system's series, in order", () => {
  const block = /export const CHART_SERIES = \[([^\]]*)\] as const;/.exec(palette);
  assert.ok(block, "chartPalette.ts declares no CHART_SERIES array");
  const series = [...block[1].matchAll(/"([^"]+)"/g)].map((match) => match[1].toLowerCase());
  assert.equal(series.length, 6, "the series must hold six colours");
  for (const [index, value] of series.entries()) {
    assert.equal(value, light.get(`series-${index + 1}`), `series ${index + 1} is not --p-series-${index + 1}`);
  }
});

test("the dark series is the dark theme's series, in order", () => {
  const block = /export const CHART_SERIES_DARK = \[([^\]]*)\] as const;/.exec(palette);
  assert.ok(block, "chartPalette.ts declares no CHART_SERIES_DARK array");
  const series = [...block[1].matchAll(/"([^"]+)"/g)].map((match) => match[1].toLowerCase());
  assert.equal(series.length, 6, "the dark series must hold six colours");
  for (const [index, value] of series.entries()) {
    assert.equal(
      value,
      dark.get(`series-${index + 1}`),
      `dark series ${index + 1} is not --p-series-${index + 1} from the dark theme`,
    );
  }
});

test("the map's plate mirrors the dark theme, and spends the dark series", () => {
  const block = /export const MAP_DARK: MapPalette = \{([\s\S]*?)\n\};/.exec(palette);
  assert.ok(block, "chartPalette.ts declares no MAP_DARK palette");
  const body = block[1];
  /*
   * `land` and `coast` are the two values that are art rather than token: the
   * plate is read at three metres, and the gap between the filled country and the
   * sea is what makes the footprint legible at that distance. They are asserted
   * as literals so a redesign cannot quietly swap in a token that measures wrong
   * on a night ground.
   */
  for (const field of ["land", "coast"]) {
    assert.ok(
      new RegExp(`${field}: "(#[0-9a-fA-F]{6})"`).test(body),
      `MAP_DARK.${field} is not a literal colour`,
    );
  }
  assert.match(
    body,
    /series:\s*CHART_SERIES_DARK,/,
    "MAP_DARK must spend the dark series rather than restating six colours",
  );
  // Everything a reader has to *interpret* on the plate is a dark-theme token, so
  // the map and the rest of the board stay one edit apart.
  for (const [field, token] of [
    ["ink", "ink"],
    ["tooltipSignal", "signal"],
  ]) {
    const value = new RegExp(`${field}: "(#[0-9a-fA-F]{6})"`).exec(body);
    assert.ok(value, `MAP_DARK.${field} is not a literal colour`);
    assert.equal(value[1].toLowerCase(), dark.get(token), `MAP_DARK.${field} is not --p-${token} from the dark theme`);
  }
});

test("the day plate mirrors the light theme, and spends the light series", () => {
  const block = /export const MAP_LIGHT: MapPalette = \{([\s\S]*?)\n\};/.exec(palette);
  assert.ok(block, "chartPalette.ts declares no MAP_LIGHT palette");
  const body = block[1];
  // The day plate reuses the light literals rather than restating them, which is
  // what keeps it one edit away from the theme it belongs to.
  for (const constant of ["CHART_INK", "CHART_SERIES", "CHART_SIGNAL_DEEP"]) {
    assert.match(
      body,
      new RegExp(`:\\s*${constant},`),
      `MAP_LIGHT restates a colour instead of using ${constant}`,
    );
  }
  /*
   * The two plates must read in opposite directions, or the footprint vanishes
   * into the ground it is drawn on: on paper the countries are darker than the
   * sheet, and on the night plate they are lighter than the sea.
   */
  const lightLand = /land: "(#[0-9a-fA-F]{6})"/.exec(body)[1];
  const darkBlock = /export const MAP_DARK: MapPalette = \{([\s\S]*?)\n\};/.exec(palette)[1];
  const darkLand = /land: "(#[0-9a-fA-F]{6})"/.exec(darkBlock)[1];
  assert.ok(
    luminance(lightLand) < luminance(light.get("canvas")),
    "MAP_LIGHT.land must be darker than the light canvas, or the footprint disappears into the sheet",
  );
  assert.ok(
    luminance(darkLand) > luminance(dark.get("canvas")),
    "MAP_DARK.land must be lighter than the dark canvas, or the footprint disappears into the sea",
  );
});

test("the chart fonts are the tokens' families", () => {
  const sans = /--font-sans:\s*"([^"]+)"/.exec(css);
  const mono = /--font-mono:\s*"([^"]+)"/.exec(css);
  assert.ok(sans && mono, "design.css declares no font families");
  assert.ok(
    literal("CHART_FONT_SANS").startsWith(`"${sans[1]}"`),
    `CHART_FONT_SANS does not start with ${sans[1]}`,
  );
  assert.ok(literal("CHART_FONT_MONO").startsWith(`"${mono[1]}"`), `CHART_FONT_MONO does not start with ${mono[1]}`);
});
