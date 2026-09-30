---
id: prism-console-visual
type: module-design
title: Prism console visual system
status: draft
parent: prism-product
tags:
  - frontend
  - visual-system
---
# Prism design system

The visual system of record for the console. A page's look is decided here; the
implementation detail (component inventory, layout, copy rules, acceptance table)
lives in [internal/api/web/DESIGN.md](internal/api/web/DESIGN.md). The architecture
of the server is a different subject and lives in [docs/DESIGN.md](docs/DESIGN.md).

Every rule below is mechanically checkable. When this file and the code disagree,
the code is wrong and a check is missing — see [Verification](#verification).

## Concept

**A lit board on a night ground.** The console is a set of glass panes standing over a
deep navy ground lit from two distant sources: indigo at the far left, violet at the
far right. The ground is the room, and it stays near-black; the panes are the work.
Inside a pane the light belongs to the readings, and one accent hue means only
"interactive".

Light is the same room at noon — pale indigo-lit paper, identical geometry — solved
separately rather than derived, so neither theme is an inversion of the other.

This world replaced an earlier, deliberately austere one: flat bordered panels, no
blur, no lift, no KPI row. The replacement is a decision, not drift. The refusals
below are what survived it, and they are now narrowed to the techniques that actually
cost legibility rather than to a general taste for plainness.

### Five refusals that survived

- **No gradient text.** A gradient belongs to a *surface* — the ground glow and the
  hero pane — never to glyphs.
- **No looping decorative motion.** No pulsing status dots, blinking cursors,
  marquees, radar sweeps, or the same fade-and-rise on every section. A status dot is
  static or it reports a real value.
- **Colour never carries meaning alone.** Every state also carries a word, a shape, a
  position, or a number.
- **`signal` and `accent` stay separate colours.** Merged into one hue, "healthy" and
  "clickable" become indistinguishable in a dense table.
- **No invented chrome.** No notification bell, avatar or user identity this console
  does not have, no status sentence the backend does not report, no count that was not
  fetched. The console's honesty is part of its look.

### What is deliberate now, and its bound

Every item below was refused outright by the previous world. It is allowed now with a
bound a reviewer can check, and the bound is the rule — not the technique.

- **Glass.** Blur has one job: separating a pane from the ground. **Two values are
  sanctioned, both declared in `design.css`** — the card blur `--p-glass-blur`
  (`blur(14px) saturate(…)`, used by `.panel`, `.glass-bar`, `.glass-rail`,
  `.glass-elevated` and the sticky `.data-grid` head) and the chrome-strip blur
  (`backdrop-blur-md`, 12px) on the two bands that float over scrolling content, the
  page header and the toast. A third value is a bug.
- **Lift.** Soft shadows are real in both themes, because a pane floats over a lit
  ground: `shadow-md` on a pane, `shadow-lg` on an overlay. The step is a hierarchy
  decision, never a per-page one.
- **Gradient fill, on exactly two surfaces**: the ground glow (`--p-ground-glow`,
  radial washes at ≤8% alpha, fixed attachment) and the hero pane
  (`.hero-gradient`, 135°). Anywhere else a fill is flat, and text is never filled.
- **The KPI row.** A row of large numerals is allowed *because* every figure in it is a
  fetched value with a named basis, and its card also carries the series or the
  definition behind it. A large number with nothing behind it is still prohibited.
- **Sparklines and rings are support, never content.** They may accompany a stated
  value; they may not stand in for it. Every one is `aria-hidden`, and its numbers are
  stated in text beside it.
- **Bento.** A page is a grid of unequal panes: the asymmetry is the point. A pane may
  contain a *region* — a table, a chart — drawn as an inset well; a pane whose only
  content is another decorative pane is still prohibited.

## Colors and themes

Dark is the default; light is a click away in the rail. Neither is an inversion of the
other — each value was solved for its own contrast target against its own surfaces,
because on a dark ground the ink must clear 4.5:1 against a *near-black* pane, the
washes must sit *between* the pane and the state colour rather than above it, and a
filled button needs dark text rather than white (white on a bright accent fails).

One token set, two definitions. Every colour is a primitive (`--p-*`) defined twice —
`:root` for light, `[data-theme="dark"]` for dark — and the Tailwind utilities are
bound to those primitives through `@theme inline` in
[`src/styles/design.css`](internal/api/web/src/styles/design.css). That indirection is
the only reason a runtime theme switch costs one attribute instead of a second
stylesheet. The theme is set before first paint by an inline script in `index.html`, so
there is no flash of the wrong ground.

### The contrast truth rule

A glass pane is translucent, and a contrast gate cannot measure
`rgba(255,255,255,0.045)`. So the six surface primitives — `canvas`, `sunk`, `raised`,
`inset`, `rail`, `elevated` — are **opaque hex values equal to the composited glass over
the canvas**:

```
surface = fill_alpha × fill_color + (1 − fill_alpha) × canvas      (rounded to #rrggbb)
```

The gate in `scripts/check-contrast.mjs` then measures the ink against what the
operator actually sees, and the translucent `--p-glass*` tokens are used for fills
only. Changing a glass alpha means re-solving the surface pair, and the gate is what
tells you.

### Surfaces, material and rules

| Token | Light | Dark | Purpose |
|---|---|---|---|
| `canvas` | `#f4f6fb` | `#070d1a` | The ground the panes float over |
| `sunk` | `#eceef4` | `#161c28` | Sunk areas: wells, insets |
| `raised` | `#fcfcfe` | `#121824` | The pane's own composited fill |
| `inset` | `#fafbfd` | `#0e1420` | Chart and map ground |
| `rail` | `#fbfcfd` | `#101522` | **Second neutral layer**: navigation |
| `elevated` | `#fefeff` | `#1a1f2b` | Overlays: sheet, palette, tooltip |
| `glass` | `rgba(255,255,255,.72)` | `rgba(255,255,255,.045)` | A pane's fill (translucent) |
| `glass-strong` | `rgba(255,255,255,.88)` | `rgba(255,255,255,.075)` | Chrome bands, sticky table head |
| `glass-edge` | `rgba(16,24,40,.08)` | `rgba(255,255,255,.09)` | The pane's 1px edge. **A region is bordered** |
| `glass-edge-strong` | `rgba(16,24,40,.15)` | `rgba(255,255,255,.16)` | Edge on hover, overlay edges |
| `glass-highlight` | `rgba(255,255,255,.86)` | `rgba(255,255,255,.06)` | Inner top highlight: the pane's lip |
| `ground-glow` | indigo/violet radials ≤6% | indigo/violet radials ≤8% | The ground's light |
| `rule` | `#d5dbe6` | `#2a3447` | Hairline |
| `rule-strong` | `#9fa9ba` | `#46556e` | Emphasis rule |
| `rule-faint` | `#e4e8f1` | `#1f2737` | Barely-there rule |
| `row-rule` | `#e9edf5` | `#1d2535` | Table row separator |

### Ink

| Token | Light | Dark | Purpose |
|---|---|---|---|
| `ink` | `#0b111e` | `#f1f3f7` | Body and headings |
| `ink-soft` | `#364152` | `#aeb9cb` | Secondary |
| `ink-faint` | `#526075` | `#8494ad` | Meta. Still ≥4.5:1 on every surface above |

### State and interaction

| Token | Light | Dark | Purpose |
|---|---|---|---|
| `signal` | `#086a50` | `#34b888` | Healthy. `signal-deep` `#054937` / `#299e73` |
| `live` | `#0a5d88` | `#38a8df` | In flight, queued |
| `warn` | `#7c4900` | `#d49426` | Degraded, stale |
| `alert` | `#a42214` | `#e68277` | Risk, failure |
| `accent` | `#4338ca` | `#818cf8` | **Interaction only**: primary action, current selection, focus ring |
| `accent-deep` | `#312e81` | `#6875f5` | Accent pressed / emphasised |
| `accent-wash` | `#e6e8fc` | `#191f45` | Tinted ground for a selection |
| `accent-lift` | `#6d28d9` | `#a78bfa` | The hero gradient's far stop. **Never text, never a state** |
| `*-wash` | `#dbf0e8` `#dcecf6` `#f7ecd4` `#f9e3df` | `#0d2f23` `#0d2e3f` `#382609` `#491812` | Tinted ground for a state |
| `on-*` | `#ffffff` | `#070d1a` | Text **on** a filled state colour. A token, not a constant: a bright accent takes the dark ground's ink |

### Data series

Six categorical colours, one meaning each, never reused for decoration.

| | 1 | 2 | 3 | 4 | 5 | 6 |
|---|---|---|---|---|---|---|
| Light | `#2b2470` | `#04745c` | `#b86c04` | `#dd2408` | `#6d1eb5` | `#0393c9` |
| Dark | `#4f5de2` | `#248f75` | `#bf841f` | `#e88272` | `#c6a0eb` | `#70d4ec` |

Two constraints hold at once, both enforced by the contrast gate:

1. A chart line is a **graphical object**, so WCAG 1.4.11 asks **3:1**, not 4.5:1.
2. The series are separated on the **lightness axis** (adjacent steps ≥1.15 in relative
   luminance), not only in hue. Greyscale printing, colour-vision deficiency and
   viewing from three metres all leave lightness as the only surviving channel.
   **Reordering the series means running the gate.**

Chart grid and axis: `chart-grid` `#e6eaf2` / `#253042`, `chart-axis` `#556274` /
`#8596af` (the axis label is text, so ≥4.5:1). Canvas cannot read CSS variables, so
these literals are duplicated in
[`src/features/dashboard/chartPalette.ts`](internal/api/web/src/features/dashboard/chartPalette.ts) —
**the two must be changed together**, and the gate must be re-run.

## Components

`src/components/ui/` is the only source of components. A page composes them; it does
not invent a control. Radix owns behaviour, this repository owns appearance, and the
inventory with per-component detail is in
[internal/api/web/DESIGN.md](internal/api/web/DESIGN.md).

What the set establishes, and what a new component must not break:

- **Structure comes from a pane and a type size**: one glass edge, one blur, one
  radius, the smallest step of lift. A pane is not decorated into importance.
- **`Badge` is the only fully round shape in the system**, so the shape itself carries
  "this is a state". A second pill-shaped thing spends that signal.
- **`Table` is a data grid, not a table**: fixed row heights 32/28/36, a sticky glass
  head, row rules and no cell borders, and `TDClip` for any text that can run long.
- **Controls are 28px** (24 small, 32 large, 36 extra-large for the shell's search
  field), and every one ships default / hover / focus / active / disabled / loading /
  error. Focus is one rule for the whole console and error belongs to the field rather
  than to the button; the per-state audit is in
  [internal/api/web/DESIGN.md](internal/api/web/DESIGN.md).
- **`Readout` / `Numeral` is how a figure is shown**: monospaced, tabular, counting up
  on change. A bare number in a `div` is not a reading.
- **Panel actions are visible.** Never revealed on hover: on a touch screen an action
  behind `opacity: 0` does not exist.
- **Loading, error and empty are the same three components everywhere**, so a state is
  never invented per page.
- **Charts are ECharts**, with the palette literals mirrored in
  `src/features/dashboard/chartPalette.ts` because canvas cannot read CSS variables.
  The globe is `echarts-gl` with its texture drawn at runtime from the repository's own
  GeoJSON — no added asset, no network call — and it falls back to the flat map where
  WebGL can't start. A chart's ground is the pane's inset, never a second card.

## Typography

| Token | Family |
|---|---|
| `font-sans` | **Manrope** 400/500/600/700, falling back to IBM Plex Sans |
| `font-mono` | **IBM Plex Mono** 400/500/600 — **only for values actually read as data** |

Scale: `2xs` 11 · `xs` 12 · `sm` **14** · `base` **16** · `lg` 18 · `xl` 20 · `2xl` 24 ·
`3xl` 30 · `4xl` 36. **Every step is at least 1.125× its neighbour** — a step a reader
cannot see is not a step, and three sizes inside a two-pixel band doing three jobs is
what makes a dense console read as flat.

Roles: `.micro` column heads, rail group labels and card meta (11/600, uppercase,
tracking .07em) · `.label` meta (12/500) · body 14 · page title 18/600 · pane title
14/600 · instrument reading 20/600 (`.numeral`, monospaced with tabular figures).

KPI figures are set at **2.5:1 number to unit** (Grafana's BigValue anatomy) and count
up over `--dur-count` (800ms) when they change. Micro-caps are for a column head, a
rail group or a card's meta — **never as a decorative eyebrow above a title**, which is
a kicker and is prohibited.

## Geometry

An 8px grid. `radius-control` 10 · `radius-card-sm` 12 · `radius-panel` 16 ·
`radius-chip` 999. The generous radii are the mockup's, kept because they are what
separates a pane from a rule.

Chrome heights are **one decision for the whole console, never a per-page one**:
`--shell-bar-h` 56 · `--page-header-h` 52 (a minimum; the band grows with its
description and tab strip) · `--panel-header-h` 44 · `--toolbar-h` 40 · `--control-h`
28 (sm 24 / lg 32 / xl 36) · `--row-h` 32 (compact 28 / comfortable 36).

`--shell-rail-w` is 248px expanded and 64px collapsed. Below 1440 the shell folds it to
its icon width on its own, because a 248px rail on a 1280px laptop is a fifth of the
screen; below 1024 the rail becomes a drawer and the top bar keeps the destinations.

### Elevation

`shadow-md` on a pane, `shadow-lg` on an overlay (sheet, menu, dialog, palette), and
`shadow-xs` inside a control that is itself lifted. A pane's lift is the same in both
themes; only its colour differs, because a shadow over a lit ground is a real shadow
and over pale paper is a soft one.

### Motion

`--ease-instrument` = `cubic-bezier(0.16, 1, 0.3, 1)`; 110 / 170 / 240ms, with
`--dur-count` 800ms for a counting numeral. **There is no page-load choreography.**
`prefers-reduced-motion` is honoured, including the globe's rotation.

## Prohibited

Each entry is a machine-detectable tell, not a taste preference.

**Page skeleton** — a uniform card wall used as page structure; a pane whose only
content is another pane; an all-caps kicker above a heading; decorative section
numerals (01 / 02 / 03).

**Surfaces** — gradient text; a blur value outside the two sanctioned ones; a coloured
`border-left` / `border-right` wider than 1px on a rounded pane; hard-offset shadows
(`box-shadow: 4px 4px 0`); zero-offset coloured glow; a sparkline, ring or soft-shadow
rounded rectangle **standing in for a stated value**; monospace as a "technical"
costume; Unicode glyphs or emoji as an icon system; tiled decorative stripes or
two-axis grid textures (unless the thing underneath genuinely is a canvas, map, drawing
or measuring device).

**Motion** — pulsing status dots; blinking cursors; marquees; the same fade-and-rise on
every section; images that scale or rotate on hover; any animation that a value does
not drive.

**Chrome** — invented status text, notification bells, avatars or identities; a
control that does not do what its label says.

## Verification

| Floor | How it is checked |
|---|---|
| Text contrast: body ≥4.5:1, large text and graphical objects ≥3:1 | `npm run check:contrast` reads `design.css` and computes **100 pairs across both themes**, including the hero pane's two gradient stops; non-zero exit on failure. Wired into `make test-web` → `make verify` |
| Series separable in greyscale | The same gate: adjacent relative luminance ≥1.15 |
| The console composes the kit and nothing else | `npm run check:kit`: native controls outside `src/components/ui/`, hard-coded control heights, and the pill shape outside `Badge` — over every `.tsx` in `src/`. Wired into `make test-web` |
| Row height and panel geometry match this file | DOM audit over the live pages: `--row-h`, pane radius 16, rail 248/64, the ground's glow, both themes |
| The chart palette is a copy, and it is current | `tests/chart-palette.test.mjs` (in `npm test` → `make test-web`) reads `design.css` and `chartPalette.ts` and fails on any pair that disagrees, on a series out of order, on a band ramp that is not separable in greyscale, or on a font that is not the token's family |
| Focus visible | `:focus-visible` draws accent at 2px with 1px offset |
| `prefers-reduced-motion` honoured | Media query at the end of `design.css` |
| Browser surfaces belong to the system | Selection, caret, scrollbar, underline offset and `tabular-nums` are set in the base layer |
| Every interactive component has default/hover/focus/active/disabled/loading/error | The UI kit, audited per state in [internal/api/web/DESIGN.md](internal/api/web/DESIGN.md). Focus is the one shared `:focus-visible` rule; error is the field's `invalid` prop |
| The ground that was painted is the ground that was asked for | `make test-ui` (local-only, needs a built `bin/prism`) drives a real Chromium and asserts `body` against the **canvas and ink primitives read out of `design.css`**, so re-solving the palette can't produce a false failure |

### The third-party slop detector

<https://github.com/pbakaus/impeccable> ships a deterministic detector: 61 checks for
the defaults an agent reaches for before the design exists. It runs in code, with no
model and no API key.

```bash
make test-slop          # scans the source tree, exit 0 = clean, exit 2 = findings
```

**It does not scan `.tsx`.** It reads HTML, CSS and JS. Passing it a directory of React
source returns nothing at all — which reads as a clean result and is not one. The
source-level equivalent is a scan of `src/styles/design.css` plus `index.html`; the
strongest form is a scan of a built `dist` tree, which also covers the bundled chunks.

**A scan of a built tree only works if the asset paths resolve.** The production
`index.html` links `/ui/assets/*` (the server mounts the bundle at `/ui/`), so a scan
root must contain `ui/assets/`. Pointed at `dist/` directly, the detector warns
`could not read linked stylesheet … color and custom-property rules will be
incomplete` and exits 2 — and its colour and custom-property checks silently do not
run. `make test-slop` builds the correct root for you.

**A finding survives only for a reason, and the reason is printed.** The gate prints
every finding it excuses, on every run, so a reader sees the choice rather than a clean
scan. Two kinds of reason exist: *provenance* (the code is not ours) and *decision* (the
code is ours and the pattern is deliberate, recorded here with its bound). Anything else
fails the build. Recorded for the current tree:

| Finding | Where | Why it stands |
|---|---|---|
| `[layout-transition] transition: padding` | ECharts' own bundled code, in a Vite shared chunk (`useReducedMotion-*.js` names the module the split happened on, not its contents) | **Provenance.** It is third-party; Prism cannot fix it without forking ECharts. The gate re-reads the chunk and only excuses it while the file still contains ECharts |
| `[ai-color-palette] Purple/violet accent colors detected` | the built `ui/index.html`, i.e. this console's own palette | **Decision.** The accent is an indigo→violet pair, because the operator pinned that look in a mockup. The bound: two gradient stops on one pane, one ground wash, one accent token pair, no gradient text, no glow and no second accent hue — see [What is deliberate now, and its bound](#what-is-deliberate-now-and-its-bound). A second palette finding still fails the gate |
| `[radial-halo] radial-gradient halo (<the band's stop> → transparent) on dark page` | the built `ui/index.html`, i.e. our own shell | **Decision.** The hero band reproduces the light field of the operator's reference art, which was measured rather than eyeballed: one lit pane, soft stops inside the 1255×233 band only, no shadow on any card, no second accent hue. The bound is enforced by the replica probes (`hero_*`) — see [The 1536 board](#the-1536-board-the-reference-replica). The gate excuses it only while the shell still carries that band |

The gate's own parser used to drop the second kind of row entirely: the engine reports
**file-scoped** rules (`ai-color-palette`, `cream-palette`, the font tells) without a
`line N:` prefix, and the first version of `parse()` only understood rows that carried a
line number — so the scan reported "clean" while the engine had reported a finding. A
false clean is the one failure a gate must not have; `parse()` now reads both shapes,
which is how the palette finding surfaced at all.

### The 1536 board: the reference replica

The workbench was rebuilt to reproduce the operator's reference art at exactly 1536×1024.
The art exists only as a PNG and no agent here has an image channel, so it was turned into
numbers first — colour census, hairline grid, per-card text metrics, corner insets — and
those numbers became the spec. The acceptance harness boots a real binary, seeds a
fixture, renders `/dashboard` at 1536×1024 in dark, and re-runs the same probes against
the render, printing PASS/FAIL per probe. Fidelity is therefore a number, not an opinion.

Measured skeleton (px, origin = canvas top-left): floating rail card `20,188 → 235,1023`;
hero band `257,0 → 1512,233` with the search field at `259..740, 18..50` and the title's
glyph box at `281..981, 110..145`; main column `257..1054`, right column `1070..1512`,
16 px gutters; KPI tiles at `y579..688` with the measured unequal widths 206/188/186/173;
and the quick-action card `1237,163 → 1512,361`, which breaks the grid upward across the
hero's bottom edge — the art's most recognisable move.

| Slot | Rect | Content |
|---|---|---|
| A | 257,0 → 1512,233 | hero: title, range, refresh, add-subscription, readouts |
| B | 1237,163 → 1512,361 | quick actions (four rows) |
| C | 20,188 → 235,1023 | rail: brand, nav at 44 px pitch, status, session footer |
| D | 257,250 → 1054,562 | 出口 / 区域: the sphere or the plate, plus the region breakdown |
| E | 1070,250 → 1221,361 | 订阅状态, the compact tile |
| F | 1070,378 → 1512,579 | 流量概览: the window's timeline |
| G1–G4 | y579..688 | 总请求数 / 平均延迟 / 错误率 / 活跃租约 |
| H | 1070,600 → 1512,934 | 最近变更 |
| I | 257,704 → 727,987 | 最近加入节点 |
| J | 743,704 → 1054,987 | 平台分布 |

The art has nine content slots and this console has eleven blocks, so **two blocks fold at
exactly this breakpoint**: instance health (its figures live in the rail status block) and
the latency profile (the hero carries window latency). Both render in full below 1536, so
the fold is a breakpoint decision rather than a deletion — it is the one place the replica
knowingly stops being one panel per slot.

The skeleton layer is scoped with `:has(.wb-board)`, so it applies to this board and not
to the dense routes; the rail's own geometry is shared, because the rail should read the
same everywhere at this width.

Residuals, printed rather than hidden: the art's nested tile inside `H` (a six-row region
list) is not reproduced yet, and where the art contradicts itself — unequal KPI widths and
the overlapping quick-action card — it is reproduced as measured where that reads as
intent, and normalised where it reads as noise.

### Installing the detector

```bash
npx impeccable install --providers=pi --scope=global   # lands in ~/.pi/agent/skills
cp -a ~/.pi/agent/skills/impeccable ~/.agents/skills/  # where this console actually looks
rm -rf ~/.pi/agent/skills                              # keep one place only; the copy above is the install
```

The install that matters is a **directory-style skill under `~/.agents/skills/`**:
`SKILL.md`, `reference/`, and `scripts/` holding the engine for the platform. That is
the path this console scans — its settings page calls `listUserSkills({ level: "global" })`
and the bundle resolves the global root as `~/.agents/skills`, with
`<project-root>/.agents/skills` for the project level. It rescans on demand, so a
directory appearing there needs no registry entry. Both shapes are read:
`frontend-design.md` sits there as a flat file, and a directory with `SKILL.md` beside
it.

Four traps, all hit while installing it here:

- **Run it from a neutral directory.** The installer detects harnesses from the current
  directory. Run inside this repository — which has a `.github/` — and it installs a
  GitHub Copilot skill *and* `hooks/impeccable.json` into the tree. Both were removed
  again; the tree is meant to stay clean.
- **Never put it on `PATH` as a symlink.** The launcher resolves its own directory with
  `dirname "$0"` to find `reference/*.md` and its engine, so through a symlink it
  searches beside the symlink — and redirecting output into that path **overwrites the
  launcher itself**. Use a wrapper in `~/.local/bin` that exports `IMPECCABLE_SKILL_DIR`
  and execs the real path.
- **The vendored engine can lag the checkout** (the installer ships 0.1.5, the local
  checkout here is 0.1.6). Keep one engine — replace the vendored copy or set
  `IMPECCABLE_BIN` — so every launcher reports the same version.
- **The installer's Pi provider does not put the skill where this console reads it.** It
  writes `~/.pi/agent/skills/impeccable`; this console scans `~/.agents/skills`. It looks
  like the right place because the console *does* read `~/.pi/agent/AGENTS.md` for its
  instructions — but not for skills. Verified in the app bundle, not guessed.

`~/.local/bin` reaches **interactive** shells only. A non-interactive shell — which is
how an agent runs commands — does not read `~/.bashrc`, so it needs the absolute path
or an exported `PATH`.

**Never trust a 0.** Confirm the detector can fail first: feed it a page with a thick
coloured `border-left`, gradient text, a zero-offset glow, a pulsing dot, an icon tile
above a heading, and a cream background. It must report `side-tab`, `gradient-text`,
`dark-glow`, `pulsing-dot`, `icon-tile-stack`, `cream-palette`.
