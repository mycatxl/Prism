---
id: prism-console-implementation
type: submodule-design
title: Prism console implementation design
status: draft
parent: prism-console-visual
tags:
  - frontend
  - implementation
---
# Prism console — implementation detail

How the console is built. The **visual system of record** — concept, colour, type,
geometry, elevation, motion and the prohibited list — is [the root
DESIGN.md](../../DESIGN.md), and it is checked mechanically. This file does not
restate it: a second copy of the tokens is a second thing to drift.

Product intent (who this is for, what it must do, accessibility floors) is
[PRODUCT.md](../../PRODUCT.md). Server architecture is [docs/DESIGN.md](../../docs/DESIGN.md).

## Token plumbing

Colour, type, spacing and chrome heights are defined in
[`src/styles/design.css`](src/styles/design.css). The shape is deliberate and worth
knowing before editing:

- `@theme inline` **binds** Tailwind utility names to primitives — it defines no
  values of its own. `--color-paper: var(--p-canvas)` and so on.
- The primitives (`--p-*`) are declared **twice**: once in `:root` (light) and once in
  `[data-theme="dark"]`. They are the only place a colour literal appears.
- Static scales — fonts, the type scale, spacing, radii, chrome heights, easings — sit
  in a second `@theme` block, plus a media query that collapses the rail.
- **Glass is a material with one job**: separating a pane from the ground. The
  `--p-glass*` primitives are the only translucent ones, and the six *surface*
  primitives are opaque hex equal to the composited glass over the canvas — that is
  what lets the contrast gate measure what the operator sees (the rule is stated in
  the root `DESIGN.md`). The blur lives in `--p-glass-blur` and is applied through the
  glass classes; a fifth place that blurs is a bug, not a variation.

That split is what makes a theme switch cost one attribute on `<html>` instead of a
second stylesheet. Adding a colour means adding a primitive in *both* blocks and a
binding in `@theme inline`; adding it in one place only is how a theme breaks silently.

**Canvas cannot read CSS variables.** `src/features/dashboard/chartPalette.ts` holds a
literal copy of the series, grid and axis colours for ECharts. Change the two together
and re-run the contrast gate.

## Components

`src/components/ui/` is the only permitted source of components. A page composes
these; it does not invent a control.

| Component | Purpose |
|---|---|
| `Page` / `PageHeader` / `PageMeta` | Page skeleton. The header band is **52px at minimum, never a fixed height**: the description line and the tab strip both live inside it, so it is applied as `min-h` and grows. Title 18/600, description 12px at ≤68ch, actions on the right |
| `Panel` / `PanelHeader` / `PanelToolbar` / `PanelBody` / `PanelFooter` | A region: one glass pane (`.panel`), radius 16, blur 14px, `shadow-md`. Header 44px, padding 16, title 14/600. **Actions are visible, never hover-revealed** — an action behind `opacity: 0` does not exist on a touch screen |
| `Table` / `THead` / `TH` / `TBody` / `TR` / `TD` / `TDNum` / `TDClip` | The data grid. Fixed row heights 32/28/36, sticky head, **row rules only, no cell borders**; long text must use `TDClip` |
| `Readout` / `ReadoutStrip` / `ReadoutCell` / `Numeral` | Instrument readings and the count-up. `Readout` takes `size` (sm/md/lg/xl) and an optional `delta` slot, for a trend chip whose basis is named in its own `title` |
| `Button` / `Input` / `Textarea` / `Select` / `Checkbox` / `Switch` | Controls, 28px tall (sm 24 / lg 32; `Button` also has `xl` 36, which is what the shell's search field uses). Every one ships default / hover / focus / active / disabled / loading / error — see the state table below |
| `Badge` | Status. The only fully-round shape in the system, so the shape itself says "this is a state" |
| `Sparkline` | A series as a hairline beside the number it belongs to. Never the only place a value is stated: it is `aria-hidden` and the reading sits next to it |
| `Donut` | A share-of-total ring with its legend as text (label, count, percentage). Also never the only statement: the ring summarises, the list is the data |
| `Tabs` / `Tooltip` / `Sheet` / `Toast` | Overlays and feedback |
| `LoadingState` / `ErrorState` / `EmptyState` | The three states, identical everywhere |

**Radix owns behaviour; this repository owns appearance.**

### States: where each of the seven lives

The root `DESIGN.md` (**Components**) asks every control for default / hover / focus / active /
disabled / loading / error. This is the audit of that list — it is a table because
the answer differs per state, and two of the seven belong somewhere other than the
component:

| State | Where it lives | Why there |
|---|---|---|
| default | The variant's own classes | — |
| hover | Per component, a step on the component's own colour | The step differs by surface (a button fills, a field steps its border) |
| focus | **One rule in `design.css`** — `:focus-visible`, 2px accent, 1px offset | Focus is one decision for the whole console; a per-component outline would be a second one |
| active | Per component, one step past hover | — |
| disabled | `disabled:opacity-45` (buttons) / `disabled:opacity-50` (fields) + `cursor-not-allowed` | — |
| loading | `Button`'s `loading` prop: spinner **and** `disabled` **and** `aria-busy` | A spinner that leaves the button clickable invites a double submit; one a screen reader cannot announce is not a state. **Not available with `asChild`** — Radix's `Slot` takes exactly one child |
| error | `Input` / `Select` / `Textarea`'s `invalid` prop: the alert border **and** `aria-invalid` | A button does not fail; the field does. An error that is only visible is not reported to a screen reader |

Two consequences worth stating, because they are the ones that drifted before:

- **A page never swaps a button's label to say it is working.** `<Button loading>`
  keeps the label and adds the spinner; `{pending ? t("保存中...") : t("保存")}`
  moves the button's own text — and its width — out from under the pointer that
  just clicked it.
- **`Checkbox` is the one control whose box is not its hit target.** It is always
  inside a `<label>` that carries the click area, so the 14px box is the visual
  and the label is the target.

Charts are ECharts. The console's 3D globe is `echarts-gl` with a texture drawn at
runtime from the repository's own `public/world-110m.geo.json` — no added asset, no
network call at render time. Two `echarts-gl@2.1.0` traps are worked around in
`EgressGlobe.tsx` and commented there: handing it a canvas as `baseTexture` turns the
sphere white on the second render, and a colour-typed `environment` smears into an
opaque black block. WebGL unavailable falls back to the flat map.

## Layout

```
┌──────┬────────────────────────────────────────────┐
│ rail │ top bar 56: location · instance state · lang│
│ 248  ├────────────────────────────────────────────┤
│      │ page header 52: title · meta · actions (sticky)│
│ sect │────────────────────────────────────────────┤
│ micro│ panel grid (8px gap, unconstrained above 2560)│
└──────┴────────────────────────────────────────────┘
```

- The page header band is a **floor, not a height**: `PageHeader` applies
  `--page-header-h` as a minimum. Measured on the live panel across all eleven
  rail destinations, the band is 54.6–99px depending on what it carries — 99px on
  `/nodes`, the one destination that passes a tab strip — so nothing may place
  content against a fixed 52px offset.
- The rail is a glass column (`.glass-rail`); the **current destination is a filled pill** (accent wash,
  600 weight, raised sheet) — **no coloured side bar**. A coloured edge on a list row
  is the loudest generic UI tell, and it says nothing the fill did not already say.
- The rail collapses to 64px below 1440 rather than holding 248 on a small laptop.
- Content width **flows and is not capped**. shadcn's `max-w-7xl` wastes half a 2560
  display.
- Dense tables set a `min-width` and scroll horizontally instead of compressing
  columns; every cell is `nowrap`. Measured at 1280: container 1172 = content 1172,
  no compression, no overflow.
- Sections are separated by a rule or a panel, never by stacked whitespace.

### The board

The dashboard is the console's reference composition, and the only page with a layout of
its own. It is **two columns of glass panes** on a twelve-column grid rather than a
uniform card wall, because a board of thirteen equal rectangles makes every fact look
equally important:

| Column | Panes, in order |
|---|---|
| Main (`xl:col-span-8`) | hero (`hero-gradient`) with the range picker, refresh and the four-readout strip · egress globe (or flat map) with the top-region table and the band legend · four KPI panes, each with its sparkline and its trend basis · traffic overview with the ingress/egress totals · recently added nodes · subscription state |
| Side (`xl:col-span-4`) | instance state (the shell's own `system/info` query, reused) · quick actions to four real destinations · node latency distribution · platform distribution ring · recent changes from the audit log |

Below `xl` the two columns stack; below `sm` the KPI panes do too. What the board may
**not** do, and what the design system checks: no pane states a figure it did not fetch
(a pane with nothing behind it renders its empty state), every trend names its basis in
text as well as in its `title`, a sparkline never stands in for its number, and the
range picker, refresh and import actions stay reachable without scrolling.

## Writing

- **Active voice.** If the button says "Save changes", the result says "Saved".
- **Name things as the user understands them**, not as the system implements them.
- **An empty state is an invitation**, not a mood. A failure state **says what
  happened and how to fix it**.
- **Sentence case**; all-caps is reserved for `.micro` (column heads, section marks),
  never for a decorative label above a heading.
- Each element does one job.

## Acceptance

| Floor | How it is checked |
|---|---|
| Contrast and series separation, both themes | `npm run check:contrast` — **100 pairs**, light and dark, including the hero pane's two stops. In `make test-web` → `make verify`, so a palette regression fails CI like a Go test |
| The kit is the only source of controls | `npm run check:kit` reads the `.tsx` sources: native `<select>`/`<input>`/`<button>`/`<textarea>` outside `src/components/ui/`, hard-coded control heights **anywhere** (a page that sizes a control has made the same per-page decision the kit may not make), and `rounded-full` anywhere but `Badge`. In `make test-web` → `make verify` |
| The kit's own invariants | `tests/kit.test.mjs` — the `Button` single-child rule, token heights, the pill reservation, that the height rule carries no scope guard, and that no control renders with an empty `onChange`. Each with the reason it exists |
| Row height and panel geometry | DOM audit of the live pages: `--row-h`, pane radius 16, rail width 248/64, the top bar at 56, and the ground's glow |
| Types are really checked | `npm run check:types` runs `tsc -p tsconfig.app.json --noEmit`. **`npx tsc --noEmit` at the repo root is a no-op** — `tsconfig.json` is a solution file with `files: []` — so it proves nothing |
| Focus is visible | `:focus-visible` draws 2px accent with 1px offset |
| Reduced motion | The media query at the end of `design.css` |
| The chart palette is current, not just present | `tests/chart-palette.test.mjs` — the mirror of `design.css` in `chartPalette.ts`, checked colour by colour, in the series order, on the band ramp's luminance spread and on the font families. In `make test-web` |
| Anti-patterns | `make test-slop` — see the detector section below. **It does not read `.tsx`**, which is why `check:kit` exists. Findings it prints are attributed (provenance or a recorded decision), never dropped, and the file-scoped rows it used to lose now fail the gate |

## Anti-patterns and how to check for them

Every prohibition in the root DESIGN.md corresponds to a machine-detectable tell: the
rules exist because a detector can fail the build on them, not because they are a
matter of taste.

<https://github.com/pbakaus/impeccable> ships the deterministic detector (61 checks,
no model, no API key):

```bash
make test-slop        # exit 0 = clean, exit 2 = findings
```

Rules learned the hard way, recorded so they are not relearned:

- **It does not scan `.tsx`.** It reads HTML, CSS and JS. Handed a directory of React
  source it returns nothing, which looks exactly like a clean pass and is not one.
- **A build scan needs resolvable asset paths.** Production `index.html` links
  `/ui/assets/*` because the server mounts the bundle at `/ui/`, so the scan root must
  contain `ui/assets/`. Pointed straight at `dist/`, the detector warns
  `could not read linked stylesheet … color and custom-property rules will be
  incomplete`, exits 2, and **silently skips its colour and custom-property checks**.
  `make test-slop` assembles the correct root.
- **Confirm it can fail before believing a 0.** Feed it a page with a thick coloured
  `border-left`, gradient text, a zero-offset glow, a pulsing dot, an icon tile above
  a heading, and a cream background: it must report `side-tab`, `gradient-text`,
  `dark-glow`, `pulsing-dot`, `icon-tile-stack`, `cream-palette`. Injecting a
  violation into `design.css` itself is the stronger test, because it proves the
  stylesheet is being read at all.
- **Attribution before suppression.** The one anti-pattern in the shipped bundle is
  `[layout-transition] transition: padding` inside ECharts' bundled code, in a Vite
  shared chunk whose filename (`useReducedMotion-*.js`) names the module the chunk was
  split on rather than its contents. It is third-party. It is printed, attributed and
  documented — not whitened out.
