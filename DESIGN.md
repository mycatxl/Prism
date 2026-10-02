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
  (`blur(24px) saturate(140%)` on paper, `blur(22px) saturate(160%)` at night: one
  material, tighter and more saturated over a near-black ground, and used by `.panel`,
  `.glass-bar`, `.glass-rail`, `.glass-elevated` and the sticky `.data-grid` head) and
  the chrome-strip blur (`backdrop-blur-md`, 12px) on the two bands that float over
  scrolling content, the page header and the toast. A third value is a bug. The
  frosted-glass pass is the reason the fills are as faint as they are: a pane is a
  *frosted* card, so it shows the ground through it (0.58 white on paper, 0.085 at
  night), it carries the 1px inner top rim-light that reads as the lit lip of the
  glass, and what it frosts is the ambient colour wash painted *behind* it
  (`--p-ground-glow`, on the body's own ground).
- **Lift.** Soft shadows are real in both themes, because a pane floats over a lit
  ground: `shadow-md` on a pane, `shadow-lg` on an overlay. The step is a hierarchy
  decision, never a per-page one.
- **Gradient fill, on exactly two surfaces**: the ground glow (`--p-ground-glow`,
  radial washes at ≤8% alpha on paper and ≤11% at night, fixed attachment — the
  ambient wash behind the glass, not a halo) and the hero pane
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
`rgba(255,255,255,0.085)`. So the six surface primitives — `canvas`, `sunk`, `raised`,
`inset`, `rail`, `elevated` — are **opaque hex values equal to the composited glass over
the canvas**:

```
surface = fill_alpha × fill_color + (1 − fill_alpha) × canvas      (rounded to #rrggbb)
```

Each pane surface is the *same glass* as the pane's own fill at a fixed fraction of its
alpha — `raised` is `glass`, `rail` 7/8 of it, `inset` 3/4 of it, `elevated` is
`glass-strong` — which keeps the surfaces' ordering intact when an alpha moves instead of
letting a nested pane out-opaque its parent. `sunk` is the exception: on paper it is a
3.5% wash of `#101828` (a recess is *darker* than the ground) and at night it is 0.8 of
`glass-strong` (a recess is *lighter* there), so it reads no fill alpha and survives an
alpha change unchanged.

The gate in `scripts/check-contrast.mjs` then measures the ink against what the
operator actually sees, and the translucent `--p-glass*` tokens are used for fills
only. Changing a glass alpha means re-solving the surfaces beside it, and the gate is
what tells you.

### Surfaces, material and rules

| Token | Light | Dark | Purpose |
|---|---|---|---|
| `canvas` | `#f4f6fb` | `#070d1a` | The ground the panes float over |
| `sunk` | `#eceef4` | `#212632` | Sunk areas: wells, insets |
| `raised` | `#fafbfd` | `#1c222d` | The pane's own composited fill |
| `inset` | `#f9fafd` | `#141a26` | Chart and map ground |
| `rail` | `#fafbfd` | `#171d29` | **Second neutral layer**: navigation |
| `elevated` | `#fdfdfe` | `#272c38` | Overlays: sheet, palette, tooltip |
| `glass` | `rgba(255,255,255,.58)` | `rgba(255,255,255,.085)` | A pane's fill (translucent) |
| `glass-strong` | `rgba(255,255,255,.78)` | `rgba(255,255,255,.13)` | Chrome bands, sticky table head |
| `glass-edge` | `rgba(16,24,40,.09)` | `rgba(255,255,255,.11)` | The pane's 1px edge. **A region is bordered** |
| `glass-edge-strong` | `rgba(16,24,40,.16)` | `rgba(255,255,255,.19)` | Edge on hover, overlay edges |
| `glass-highlight` | `rgba(255,255,255,.86)` | `rgba(255,255,255,.11)` | The inner top rim-light: the pane's lit lip, drawn as `inset 0 1px 0 0` ahead of the elevation |
| `ground-glow` | indigo/violet radials ≤10% | indigo/violet radials ≤11% | The ambient colour wash *behind* the glass, painted on the ground. The light stops were raised ×1.25 when the light panes got their frosted pass: at ≤8% the wash sat under a `.58`-alpha white glass and the board read flat |
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
| `accent-wash` | `#e6e8fc` | `#1c2456` | Tinted ground for a selection. The night value moved with the frosted pass: the pane's own fill got lighter (`raised`), and a selection wash that measures 1.005:1 against the pane is not a fill — it is the pane. It now sits 1.09:1 from `raised`, in step with the four state washes (1.08–1.12:1), with the accent on it at 4.90:1 |
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
  radius (20px, and the same on an overlay so a dialog and the pane it opened from agree),
  the inner top rim-light, and the smallest step of lift. A pane is not decorated into
  importance.
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
  The egress plate is a 2D world map with its outline read from the repository's own
  GeoJSON — no added asset, no network call to anyone but the panel. A chart's ground
  is the pane's inset, never a second card.

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

An 8px grid. `radius-control` 10 · `radius-card-sm` 12 · `radius-panel` **20** ·
`radius-chip` 999. The generous radii are the mockup's, kept because they are what
separates a pane from a rule; the pane's 20px is the Apple-card step, and the panel's
inner top rim-light (`inset 0 1px 0 0 var(--color-glass-highlight)`, first in the
shadow list) is what makes the glass read as a lit lip rather than a flat fill.

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
`prefers-reduced-motion` is honoured, including the plate's flight-line pulses and its
hub ripples, which become solid marks.

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
| The board is a responsive Bento grid, not a pixel replica | `npm run check:responsive` reads `design.css` and fails if the geometry comes back: a `@media (min-width: 1536px)` block, a fixed pixel height on `.wb-board`/`.wb-shell-root`/`.wb-main-zone`, a `position: absolute` rule naming a board pane, or a missing board rule (the gate proves it read the right stylesheet rather than passing on an empty one). In `make test-web` → `make verify`, so the rule holds in CI even though CI installs no browser |
| Row height and panel geometry match this file | DOM audit over the live pages: `--row-h`, pane radius 20, rail 248/64, the ground's glow, both themes |
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
| `[radial-halo] radial-gradient halo (<stop> → transparent) on dark page` | none — the finding is gone | **Not applicable any more.** The finding was the hero band's radial stops, and the band went with the 1536px replica layer. Measured: the build scan is clean without it, and `check-slop.mjs` no longer carries the excuse, so a radial halo that comes back is an unexcused finding and fails the gate |

The gate's own parser used to drop the second kind of row entirely: the engine reports
**file-scoped** rules (`ai-color-palette`, `cream-palette`, the font tells) without a
`line N:` prefix, and the first version of `parse()` only understood rows that carried a
line number — so the scan reported "clean" while the engine had reported a finding. A
false clean is the one failure a gate must not have; `parse()` now reads both shapes,
which is how the palette finding surfaced at all.

### The Bento board

The workbench is a **Bento grid**, not a reproduction. An earlier revision rebuilt the board to
match an operator's reference dashboard at exactly 1536×1024, and did it with a
`@media (min-width: 1536px)` layer in `design.css` that positioned every pane as a percentage of
a 1288×1024 canvas: unequal KPI widths, a quick-action card overlapping the hero, a rail replica,
two panes folded away. It was faithful and it was wrong — at every other size the board read as
cards of odd sizes in odd places, and the operator rejected it on exactly that ground. The layer
is deleted, and with it the rail replica, the folded panes and the shell overrides: the dashboard
wears the same shell as every other route, and its composition is the point rather than its pixel
geometry.

What replaced it is a twelve-column grid at `xl` whose classes live in the JSX
(`xl:grid-cols-12`, `xl:col-span-8`, `xl:col-span-4`) — no pane geometry in the stylesheet at
all:

| Band | Panes, top to bottom |
|---|---|
| Full width (`xl:col-span-12`) | the hero band (`hero-gradient`) with the range picker, refresh, add-subscription and the four metric chips |
| Main (`xl:col-span-8`) | 全球流量: the egress plate at the column's full width · the **KPI strip**: 总请求数, 平均延迟, 错误率 and 活跃租约 as four cards across (`sm:grid-cols-2`, `xl:grid-cols-4`), each carrying its sparkline and the basis of its trend, directly under the plate · 最近加入节点 (the newest arrivals, at the column's full width) · the 订阅状态 band |
| Side (`xl:col-span-4`) | 运行状态 (instance badge, version, the two pool readouts and the sync line — one card) · 快捷操作 · 热门区域 (the region table, each row carrying its share as a bar in the region's own colour) · 流量概览 · 平台分布 (the donut) · 告警 · 延迟分布 |

Read top to bottom the board is the composition the operator's reference carries: greeting, then
where traffic leaves from, then the four numbers about that traffic, then the detail tables, then
the band. Only the hero takes a full-width row (`xl:col-span-12`); the KPI strip sits inside the
main column, under the plate, so the split into the data story and the always-on panes begins
immediately below the greeting. The four KPIs used to be a four-row card at the bottom of the side
column (关键指标), and then a full-width strip between the hero and the split; the reference puts
them under the map, where they read as the plate's own figures.

Two of the board's lines live outside the grid. The **"所有系统运行正常" pill** is a `Badge
tone="signal"` in the shell's own top bar (`components/AppShell.tsx`), so it shows on every route:
the 运行状态 card's plain-language status line moved there when the reference board put it there,
and the side card kept the instance badge, the version, the two pool readouts and the sync line.
The **告警 feed** is the audit log (`/api/v1/audit-logs`) rendered as sentences:
`alertPhrase()` in `WorkbenchPage.tsx` maps the route pattern the middleware recorded
(`METHOD /api/v1/...`, braces and all) to a whole-phrase translation key, with a route the table
does not know falling back to its own trimmed path — so a row reads "探测节点出口 / Node egress
probed" rather than "POST /api/v1/nodes/{hash}/actions/probe-egress". It is the audit trail, not a
synthetic alert stream; the method chip keeps its tone colour (a DELETE is the one row that can be
a loss) and the raw record stays in the row's own `title`.

Below `xl` the two columns stack; below `sm` the panes inside them do, and the KPI strip goes
two across and then one. Type, radii and borders stay
in px — this is a dashboard that gets wider, not a screenshot that gets scaled — but nothing about
a pane's *position* is a fixed length any more. The hero heading is 26px in its own rule: that is
the breakpoint layer's 34px folded back into the base, so the band keeps its presence at every
width.

**The plate's flow runs outward.** The map's origin is the panel's own egress
(`panel_egress_region` / `panel_egress_ip` on `/system/info`, resolved through the same centroid
index the hubs use) and one constant-width flight line runs from that origin to every hub with
exits. A missing or unresolvable egress region draws **no origin and no lines**, because the lines
are the dispatch relationship and not a measurement: converging them on whichever hub happened to
be busiest was a fact nobody took. The origin marker is the one mark on the plate that is not a
region, and it is not sized by node count.

**The plate fills its own box.** The body is width-driven — `aspect-[259/100]` on the wrapper in
`WorkbenchPage.tsx`, with `min-h-[240px]` as the guard for the narrow stacked layout — and the
ratio is the map's own: 360° of longitude by the ~139° of latitude Antarctica's removal leaves is
≈2.59:1. At the map's own ratio the projection fills the card edge to edge at every column width,
with no small centred world and dead sea at the flanks and no distortion, because "fill the box"
and "fit the projection" are the same operation there. That is why the plate's `geo` pins
`left`/`top`/`right`/`bottom` to `0` (`features/dashboard/EgressMap.tsx`): with all four set ECharts
stretches the projection into the box rather than fitting it inside.

What that replaced: a definite `h-[380px] xl:h-[440px] 2xl:h-[500px]` height. It existed because
the wrapper used to be a `flex-1` child with `flex-basis: 0%`, so the canvas had nothing to fill and
a `min-height` was the only thing giving it a size — but a fixed height cannot track the column
width, so the world was fitted and centred inside whatever box the height produced. Before that the
plate carried four measured percentage insets that made the panel's box *be* the land bounding box;
at the column's full width the same numbers widened the world and cut off its southern edge. Under
that canvas the sea is a two-token vertical gradient (`--color-paper-inset` → `--color-live-wash`),
which is why `MAP_DARK`/`MAP_LIGHT` carry no sea colour; the countries that carry exits are filled in
their region's own series colour at a low opacity, restating the hub and the table row rather than
adding a figure. In light that fill is 22% — a wash over the land, not a solid — and the two
graticule-adjacent tokens were solved as a pair for it: land `#d3ddec` (a touch lighter than the
light sea's own end, `#dcecf6`, so a lit country still separates from the water) and the coast
hairline `#a9b8d0` (crisper than the sea it draws against). The header's expand control opens the
same map in a centred dialog
adding a figure; and the header's expand control opens the same map in a centred dialog
(`components/ui/Dialog.tsx`) at the same ratio (`w-[min(92vw,1160px)] aspect-[259/100]`) whose portal
exists only while it is open.

**What stayed, because it was the design and not the replica**: the hero band's light field
(`.hero-gradient`, the pane's own two stops), the metric-chip row, the plate's header, live dot and
region-table rules, and every colour pair the contrast gate measures.

**The gate's new contract.** `npm run check:responsive` no longer resolves percentages against a
canvas; it fails if the geometry comes back. It reads `design.css` and fails when (1) a
`@media (min-width: 1536px)` block reappears, (2) a `.wb-board` / `.wb-shell-root` /
`.wb-main-zone` rule carries a fixed pixel height — those boxes must be fluid, and today none of
the three carries a rule at all, so this is a guard against the old geometry returning — (3) a
`position: absolute` rule names a board pane (the range picker's chevron is not a pane and is not
flagged), or (4) one of the board's own rules — `.wb-hero-banner`, `.wb-hero-heading`,
`.wb-metric-chips`, `.wb-status-card`, `.wb-plate`, `.wb-region-table` — has been renamed or
deleted, because a gate that passes on an empty stylesheet is a false clean. `.wb-status-card` is
the one entry nothing composes any more — the 运行状态 card's status line moved into the shell's top
bar — and it is kept, with `.wb-status-dot`, `.wb-status-title` and `.wb-status-desc`, as the
residue this rule names. It fails closed on a
missing or unparseable file, an implausible rule count or an empty check set, and prints a
one-line summary when it passes.

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
