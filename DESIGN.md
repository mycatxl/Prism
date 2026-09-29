# Prism design system

The visual system of record for the console. A page's look is decided here; the
implementation detail (component inventory, layout, copy rules, acceptance table)
lives in [internal/api/web/DESIGN.md](internal/api/web/DESIGN.md). The architecture
of the server is a different subject and lives in [docs/DESIGN.md](docs/DESIGN.md).

Every rule below is mechanically checkable. When this file and the code disagree,
the code is wrong and a check is missing — see [Verification](#verification).

## Concept

**A calibration bench.** The subject is network egress: which address traffic leaves
from, and how clean that address is. So the surface is built like an instrument, not
like a document — a dark ground by default, structure carried by panel frames and
hairline rules, data carrying the light, and one accent hue that means only
"interactive".

Three deliberate refusals:

- **No decorative telling.** The 大屏 vocabulary that was studied was copied for its
  structure and geometry, never its chrome: no laser-sweep SVG borders, radar
  sweeps, tiled grid backgrounds, neon outer glows, gradient text, 3D cone charts,
  or stretched nine-slice frames.
- **Colour never carries meaning alone.** Every state also carries a word, a shape,
  or a position.
- **`signal` and `accent` stay separate colours.** Merged into one hue, "healthy"
  and "clickable" look identical in a dense table.

## Colors and themes

Dark is the default; light is a click away in the rail. Neither is an inversion of
the other — each value was solved for its own contrast target against its own
surfaces, because on a dark ground the ink must clear 4.5:1 against a *near-black*
panel, the washes must sit *between* the panel and the state colour rather than
above it, and a filled button needs dark text rather than white (white on a bright
accent is 2.9:1 and fails).

One token set, two definitions. Every colour is a primitive (`--p-*`) defined twice —
`:root` for light, `[data-theme="dark"]` for dark — and the Tailwind utilities are
bound to those primitives through `@theme inline` in
[`src/styles/design.css`](internal/api/web/src/styles/design.css). That indirection
is the only reason a runtime theme switch costs one attribute instead of a second
stylesheet. The theme is set before first paint by an inline script in `index.html`,
so there is no white flash.

### Surfaces and rules

| Token | Light | Dark | Purpose |
|---|---|---|---|
| `paper` | `#f1f3f7` | `#0b1220` | The bench. Cool neutral |
| `paper-sunk` | `#e5e9f0` | `#0f1828` | Sunk areas: table headers, wells |
| `paper-raised` | `#ffffff` | `#131d2f` | The sheet a panel sits on |
| `paper-inset` | `#f8fafc` | `#0d1626` | Chart and map ground |
| `rail` | `#e6eaf1` | `#0a101c` | **Second neutral layer**: navigation. Cooler than the content so frame and readings never blur together |
| `rule` | `#d1d7e0` | `#374455` | Hairline |
| `rule-strong` | `#a4adbb` | `#4d5f78` | Emphasis rule |
| `rule-faint` | `#e3e7ee` | `#2b3544` | Barely-there rule |
| `panel-edge` | `rgba(10,15,22,.12)` | `rgba(255,255,255,.1)` | The panel's 1px border. **A region is bordered** |
| `row-rule` | `#eaeef3` | `#28313e` | Table row separator |

### Ink

| Token | Light | Dark | Purpose |
|---|---|---|---|
| `ink` | `#0a0f16` | `#f1f3f7` | Body and headings |
| `ink-soft` | `#39434f` | `#a5b2c3` | Secondary |
| `ink-faint` | `#566170` | `#788aa1` | Meta. Still ≥4.5:1 on the panel |

### State and interaction

| Token | Light | Dark | Purpose |
|---|---|---|---|
| `signal` | `#0a6b52` | `#2daa7d` | Healthy. `signal-deep` `#06483a` / `#27926a` |
| `live` | `#0b5f8a` | `#36a1d6` | In flight, queued |
| `warn` | `#7d4a00` | `#c88b23` | Degraded, stale |
| `alert` | `#a32313` | `#de7c71` | Risk, failure |
| `accent` | `#0b4fa8` | `#6399e1` | **Interaction only**: primary action, current selection, focus ring. `accent-deep` `#073a7d` / `#3e82da` |
| `*-wash` | `#dcefe8` `#ddebf5` `#f7ecd5` `#f8e4e0` `#e2ebf9` | `#0d3023` `#0d2f3f` `#39270a` `#4f1912` `#0f2b4f` | Tinted ground for a state |
| `on-*` | `#ffffff` | `#08101c` | Text **on** a filled state colour. A token, not a constant: a bright accent takes dark ink, a dark accent takes white |

### Data series

Six categorical colours, one meaning each, never reused for decoration.

| | 1 | 2 | 3 | 4 | 5 | 6 |
|---|---|---|---|---|---|---|
| Light | `#0554bb` | `#03725c` | `#ba6e05` | `#dd2206` | `#6505d1` | `#059bd1` |
| Dark | `#236ec4` | `#238b72` | `#b9801e` | `#e67f6f` | `#c49ee7` | `#6ed2ea` |

Two constraints hold at once, both enforced by the contrast gate:

1. A chart line is a **graphical object**, so WCAG 1.4.11 asks **3:1**, not 4.5:1.
2. The series are separated on the **lightness axis** (adjacent steps ≥1.15 in
   relative luminance), not only in hue. Greyscale printing, colour-vision
   deficiency, and viewing from three metres all leave lightness as the only
   surviving channel. **Reordering the series means running the gate.**

Chart grid and axis: `chart-grid` `#e8ecf2` / `#313c4d`, `chart-axis` `#5d6876` /
`#7a8ba2` (the axis label is text, so ≥4.5:1). Canvas cannot read CSS variables, so
these literals are duplicated in
[`src/features/dashboard/chartPalette.ts`](internal/api/web/src/features/dashboard/chartPalette.ts) —
**the two must be changed together**, and the gate must be re-run.

## Components

`src/components/ui/` is the only source of components. A page composes them; it does
not invent a control. Radix owns behaviour, this repository owns appearance, and the
inventory with per-component detail is in
[internal/api/web/DESIGN.md](internal/api/web/DESIGN.md).

What the set establishes, and what a new component must not break:

- **Structure comes from a frame and a type size**, not from shadow. A panel is a 1px
  edge plus `shadow-xs`; a region is bordered.
- **`Badge` is the only fully round shape in the system**, so the shape itself carries
  "this is a state". A second pill-shaped thing spends that signal.
- **`Table` is a data grid, not a table**: fixed row heights 32/28/36, a sticky head,
  row rules and no cell borders, and `TDClip` for any text that can run long.
- **Controls are 28px** (24 small, 32 large), and every one ships default / hover /
  focus / active / disabled / loading / error. Half that list is not done.
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
  WebGL is unavailable.

## Typography

| Token | Family |
|---|---|
| `font-sans` | **IBM Plex Sans** 400/500/600 |
| `font-mono` | **IBM Plex Mono** 400/500/600 — **only for values actually read as data** |

Scale: `2xs` 11 · `xs` 12 · `sm` **14** · `base` **16** · `lg` 18 · `xl` 20 · `2xl` 24 ·
`3xl` 30 · `4xl` 36. **Every step is at least 1.125× its neighbour** — a step a
reader cannot see is not a step, and three sizes inside a two-pixel band doing three
jobs is what makes a dense panel read as flat.

Roles: `.micro` column heads and section marks (11/600, uppercase, tracking .07em) ·
`.label` meta (12/500) · body 14 · page title 18/600 · panel title 14/600 ·
instrument reading 20/600 (`.numeral`, monospaced with tabular figures).

KPI figures are set at **2.5:1 number to unit** (Grafana's BigValue anatomy) and
count up over 800ms `easeOutCubic` when they change (go-view).

## Geometry

An 8px grid. `radius-control` 4 · `radius-panel` 8 · `radius-chip` 999.

Chrome heights are **one decision for the whole console, never a per-page one**:
`--shell-bar-h` 48 · `--page-header-h` 52 · `--panel-header-h` 44 · `--toolbar-h` 40 ·
`--control-h` 28 (sm 24 / lg 32) · `--row-h` 32 (compact 28 / comfortable 36).

`--shell-rail-w` is deliberately **not** a constant: it is 232px and collapses to
`--shell-rail-w-collapsed` 56px under a media query, because a 232px rail on a
1200px laptop is 19% of the screen. Measured: 1280 → 56, 1920 → 232.

These numbers are the convergence of Tabler, shadcn-admin, Ant Design and Vben, not
inventions.

### Elevation

`shadow-xs` on panels; `sm/md/lg` are **for overlays only** (drawer, menu, dialog).
A panel is 1px edge plus xs. **Sibling containers are separated by border and type
size, never by shadow.** On the dark theme `xs` and `sm` are literally `none`: an
edge separates a near-black panel, and a drop shadow beneath it is invisible at best
and muddy at worst. Only the overlays keep a shadow there
(`0 8px 24px -8px rgb(0 0 0/.6)`, `0 16px 48px -12px rgb(0 0 0/.7)`).

### Motion

`--ease-instrument` = `cubic-bezier(0.16, 1, 0.3, 1)`; 110 / 170 / 240ms. **There is
no page-load choreography.** `prefers-reduced-motion` is honoured, including the
globe's rotation.

## Prohibited

Each entry is a machine-detectable tell, not a taste preference.

**Page skeleton** — an "icon + title + body" card grid used as page structure; cards
inside cards; the **hero-number template** (big figure, small label, supporting stat,
accent colour) as a KPI wall; an all-caps kicker above a heading; decorative section
numerals (01 / 02 / 03).

**Surfaces** — gradient text; blur used as decoration; a coloured `border-left` /
`border-right` wider than 1px on a rounded card; hard-offset shadows
(`box-shadow: 4px 4px 0`); zero-offset coloured glow; an inline sparkline, progress
ring or soft-shadow rounded rectangle **standing in for content**; monospace as a
"technical" costume; Unicode glyphs or emoji as an icon system; tiled decorative
stripes or two-axis grid textures (unless the thing underneath genuinely is a canvas,
map, drawing or measuring device).

**Motion** — decorative pulsing status dots; blinking cursors; marquees; the same
fade-and-rise on every section; images that scale or rotate on hover.

## Verification

| Floor | How it is checked |
|---|---|
| Text contrast: body ≥4.5:1, large text and graphical objects ≥3:1 | `npm run check:contrast` reads `design.css` and computes **86 pairs across both themes**; non-zero exit on failure. Wired into `make test-web` → `make verify` |
| Series separable in greyscale | The same gate: adjacent relative luminance ≥1.15 |
| Row height and panel geometry match this file | DOM audit over the live pages: `--row-h`, panel radius 8, rail width, rhythm on 6/8/12/16 |
| Keyboard focus visible | `:focus-visible` draws accent at 2px with 1px offset |
| `prefers-reduced-motion` honoured | Media query at the end of `design.css` |
| Browser surfaces belong to the system | Selection, caret, scrollbar, underline offset and `tabular-nums` are set in the base layer |
| Every interactive component has default/hover/focus/active/disabled/loading/error | The UI kit. Half of them is not done |

### The third-party slop detector

<https://github.com/pbakaus/impeccable> ships a deterministic detector: 61 checks for
the defaults an agent reaches for before the design exists. It runs in code, with no
model and no API key.

```bash
make test-slop          # scans the source tree, exit 0 = clean, exit 2 = findings
```

**It does not scan `.tsx`.** It reads HTML, CSS and JS. Passing it a directory of
React source returns nothing at all — which reads as a clean result and is not one.
The source-level equivalent is a scan of `src/styles/design.css` plus `index.html`;
the strongest form is a scan of a built `dist` tree, which also covers the bundled
chunks.

**A scan of a built tree only works if the asset paths resolve.** The production
`index.html` links `/ui/assets/*` (the server mounts the bundle at `/ui/`), so a
scan root must contain `ui/assets/`. Pointed at `dist/` directly, the detector warns
`could not read linked stylesheet … color and custom-property rules will be
incomplete` and exits 2 — and its colour and custom-property checks silently do not
run. `make test-slop` builds the correct root for you.

### Installing the detector

```bash
npx impeccable install --providers=pi --scope=global   # lands in ~/.pi/agent/skills
cp -a ~/.pi/agent/skills/impeccable ~/.agents/skills/  # where this console actually looks
rm -rf ~/.pi/agent/skills                              # keep one place only; the copy above is the install
```

The install that matters is a **directory-style skill under `~/.agents/skills/`**:
`SKILL.md`, `reference/`, and `scripts/` holding the engine for the platform. That is the
path this console scans — its settings page calls `listUserSkills({ level: "global" })`
and the bundle resolves the global root as `~/.agents/skills`, with
`<project-root>/.agents/skills` for the project level. It rescans on demand, so a
directory appearing there needs no registry entry. Both shapes are read:
`frontend-design.md` sits there as a flat file, and a directory with `SKILL.md` beside it.

Four traps, all hit while installing it here:

- **Run it from a neutral directory.** The installer detects harnesses from the
  current directory. Run inside this repository — which has a `.github/` — and it
  installs a GitHub Copilot skill *and* `hooks/impeccable.json` into the tree. Both
  were removed again; the tree is meant to stay clean.
- **Never put it on `PATH` as a symlink.** The launcher resolves its own directory
  with `dirname "$0"` to find `reference/*.md` and its engine, so through a symlink it
  searches beside the symlink — and redirecting output into that path **overwrites the
  launcher itself**. Use a wrapper in `~/.local/bin` that exports
  `IMPECCABLE_SKILL_DIR` and execs the real path.
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

**Never trust a 0.** Confirm the detector can fail first: feed it a page with a
thick coloured `border-left`, gradient text, a zero-offset glow, a pulsing dot, an
icon tile above a heading, and a cream background. It must report `side-tab`,
`gradient-text`, `dark-glow`, `pulsing-dot`, `icon-tile-stack`, `cream-palette`.

**Known finding, attributed.** The only anti-pattern in the shipped bundle is
`[layout-transition] transition: padding` inside ECharts' own code, in a Vite shared
chunk (`useReducedMotion-*.js` is named for the module the chunk was split on, not
for its contents). It is third-party, it is not something Prism can fix without
forking ECharts, and it is recorded here rather than whitelisted away.
