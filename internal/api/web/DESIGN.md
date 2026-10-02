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

- `@theme inline static` **binds** Tailwind utility names to primitives — it defines
  no values of its own. `--color-paper: var(--p-canvas)` and so on. `static` is
  load-bearing: Tailwind emits an alias only when it can see a consumer, and it reads
  markup and CSS, never JavaScript. An alias a component names at runtime —
  `style={{ backgroundColor: "var(--color-series-1)" }}`, which is how the donut's
  legend and the region table's dots name theirs — is invisible to it, gets dropped
  from the bundle, and the `var()` resolves to nothing, so the element paints
  transparent with no error anywhere. Six series aliases and `--color-chart-grid` had
  no other consumer and were being pruned exactly that way;
  `tests/design-tokens.test.mjs` now holds the line.
- The primitives (`--p-*`) are declared **twice**: once in `:root` (light) and once in
  `[data-theme="dark"]`. They are the only place a colour literal appears.
- Static scales — fonts, the type scale, spacing, radii, chrome heights, easings — sit
  in a second `@theme` block, plus a media query that collapses the rail.
- **Glass is a material with one job**: separating a pane from the ground. The
  `--p-glass*` primitives are the only translucent ones, and the six *surface*
  primitives are opaque hex equal to the composited glass over the canvas — that is
  what lets the contrast gate measure what the operator sees (the rule is stated in
  the root `DESIGN.md`). It is a *frosted* material: the fill is faint (`glass` 0.58 on
  paper, 0.085 at night), the inner top rim-light is what makes the pane's edge read as
  a lit lip, and the ambient colour it frosts is the ground's own wash
  (`--p-ground-glow`, painted on `body`). The blur lives in `--p-glass-blur`
  (`blur(24px) saturate(140%)` on paper, `blur(22px) saturate(160%)` at night) and is
  applied through the glass classes; a fifth place that blurs is a bug, not a variation.

That split is what makes a theme switch cost one attribute on `<html>` instead of a
second stylesheet. Adding a colour means adding a primitive in *both* blocks and a
binding in `@theme inline`; adding it in one place only is how a theme breaks silently.

**The board is a replica of a dark reference, and the light theme is ours to solve.**
The workbench's own rules (`.wb-*`) are written so the *base* rule reads a token — so
paper gets ink and surfaces that belong on paper — and a single block, "The night board,
restated", puts the art's measured literals back behind `html[data-theme="dark"]`. The
alternative (a literal in the base rule, which is what the panel originally had) makes
the light theme render the art's night values on a white sheet: the hero heading, status
title and metric values measured **1.02:1** — white on white — with every gate green,
because the literal was in a component rule and no gate read those.

Two consequences worth stating:

- **Do not write a bare colour literal in a `.wb-*` rule.** If it comes from the art,
  it belongs in the night block; if it is the panel's own, it belongs in a token. The
  block exists so that "the dark theme did not move" is checkable by reading one place.
- **A map's palette is a literal by necessity** (`chartPalette.ts`, because a canvas
  cannot read CSS variables) and so is exempt from the above — its two plates are
  `MAP_DARK`/`MAP_LIGHT`, chosen from `data-theme` at runtime, and
  `tests/chart-palette.test.mjs` holds them to the tokens they mirror.

**Canvas cannot read CSS variables.** `src/features/dashboard/chartPalette.ts` holds a
literal copy of the series, grid and axis colours for ECharts. Change the two together
and re-run the contrast gate.

## Components

`src/components/ui/` is the only permitted source of components. A page composes
these; it does not invent a control.

| Component | Purpose |
|---|---|
| `Page` / `PageHeader` / `PageMeta` | Page skeleton. The header band is **52px at minimum, never a fixed height**: the description line and the tab strip both live inside it, so it is applied as `min-h` and grows. Title 18/600, description 12px at ≤68ch, actions on the right |
| `Panel` / `PanelHeader` / `PanelToolbar` / `PanelBody` / `PanelFooter` | A region: one glass pane (`.panel`), radius 20 (`--radius-panel`, the same value the dialog content takes), `--p-glass-blur`, the inner top rim-light (`inset 0 1px 0 0 var(--color-glass-highlight)` first in the shadow list), `shadow-md`. Header 44px, padding 16, title 14/600. **Actions are visible, never hover-revealed** — an action behind `opacity: 0` does not exist on a touch screen |
| `Table` / `THead` / `TH` / `TBody` / `TR` / `TD` / `TDNum` / `TDClip` | The data grid. Fixed row heights 32/28/36, sticky head, **row rules only, no cell borders**; long text must use `TDClip` |
| `Readout` / `ReadoutStrip` / `ReadoutCell` / `Numeral` | Instrument readings and the count-up. `Readout` takes `size` (sm/md/lg/xl) and an optional `delta` slot, for a trend chip whose basis is named in its own `title` |
| `Button` / `Input` / `Textarea` / `Select` / `Checkbox` / `Switch` | Controls, 28px tall (sm 24 / lg 32; `Button` also has `xl` 36, which is what the shell's search field uses). Every one ships default / hover / focus / active / disabled / loading / error — see the state table below |
| `Badge` | Status. The only fully-round shape in the system, so the shape itself says "this is a state" |
| `Sparkline` | A series as a hairline beside the number it belongs to. Never the only place a value is stated: it is `aria-hidden` and the reading sits next to it |
| `Donut` | A share-of-total ring with its legend as text (label, count, percentage). Also never the only statement: the ring summarises, the list is the data |
| `Dialog` / `Sheet` / `Tabs` / `Tooltip` / `Toast` | Overlays and feedback. `Dialog` is the composed Radix surface — root, trigger, portal, overlay, content, title, close — and its portal mounts only while it is open, so a dialog may hold a chart. The overlay is the `bg-ink/45` scrim; the content is glass on `bg-paper-elevated` at `--radius-panel` (20px, so an overlay matches the pane it opened from) and takes the width of its own body |
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

Charts use the renderer that fits the evidence. ECharts remains the canvas renderer for the traffic timeline and other series charts. The egress plate is a separate offline SVG world map: it reads `public/world-110m.geo.json`, uses `d3-geo`'s `geoNaturalEarth1`/`geoPath`, and makes no map-service request. There is no WebGL path or extension pack.

The map is a content region inside the main dashboard panel. Its country paths, hub marks, origin mark and relationship lines are separate SVG layers. A hub sits on the region's **busiest member country** rather than on a hand-picked label position, so every marker remains a place the pool really exits from. The existing region table beside it reads the same aggregate rows.

**The projection follows the stage.** `ResizeObserver` measures the actual `.egress-map` box; `fitExtent` computes a new projection for that size; SVG `viewBox` plus `preserveAspectRatio="xMidYMid meet"` preserves the world outline without stretching or cropping. The normal map wrapper uses a fluid minimum height and ordinary flex flow rather than `aspect-[259/100]` or fixed dashboard coordinates. This is deliberate: the map may leave a little sea around the outline, but it cannot tear when the column changes width.

**The flow runs outward.** The plate's origin is the panel's own egress — `panel_egress_region` / `panel_egress_ip` on `/system/info`, resolved through the same centroid index the hubs use — and one constant-width flight line runs from that origin to every hub with exits. It is a relationship, never a volume. When the panel reports no resolvable origin, the plate draws no origin and no lines. The origin marker is not a region and is not sized by node count. Country fills, hubs and table dots spend the same region series colour.

The expanded plate is a viewport-level Radix dialog. Its content is a flex column sized from the current viewport; the header is shrink-wrapped and the map body is `min-height: 0; flex: 1`, so the second `EgressMap` measures the available dialog area instead of inheriting a small fixed ratio. The portal exists only while it is open, and Radix retains Escape, scrim dismissal, focus capture and focus return.

The latency profile is also ordinary flow: API buckets and overflow are grouped into six bands, each rendered as a label/bar/count grid row. The panel lives in the main evidence column, and its summary stacks on narrow screens so it cannot be squeezed into the side rail.

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

The dashboard is the console's reference composition, and the only page with a layout of its own.
It is **a top band over two columns of glass panes** on a twelve-column grid rather than a uniform
card wall, because a board of thirteen equal rectangles makes every fact look equally important.
Every pane is a plain responsive panel — the board carries no pixel geometry of its own, and no
breakpoint layer positions anything:

| Band | Panes, in order |
|---|---|
| Full width (`xl:col-span-12`) | the hero band (`hero-gradient`) with the range picker, refresh, add-subscription and the four metric chips |
| Main (`xl:col-span-8`) | the Global Traffic plate at the column's full width, with its expand control · the KPI strip: total requests, average latency, error rate and active leases as four cards across (`sm:grid-cols-2`, `xl:grid-cols-4`) · the streaming latency profile · Recently added nodes · the subscription band |
| Side (`xl:col-span-4`) | instance state, in one card (the shell's own `system/info` query, reused: badge, version, the two pool readouts, sync line) · quick actions to four real destinations · the Top Regions table, each row carrying its share as a bar in the region's own colour · the Traffic overview pane with the ingress/egress totals · the platform-distribution ring · the Alerts feed from the audit log |

The order is the reference board's: greeting, then where traffic leaves from, then the four numbers
about that traffic, then the detail tables, then the band. Only the hero is full width; the KPI
strip sits inside the main column, under the plate. The four KPIs used to be the last card of the
side column (关键指标), then a full-width strip between the hero and the split — the strip moved
into the main column when the reference put it under the map.

Two of the board's lines are not panes. The "All Systems Operational" pill is a signal `Badge` in
the shell's own top bar, so every route carries it: the instance card's plain-language status line
moved there, and the card kept the badge, the version, the two pool readouts and the sync line. The
Alerts feed is the audit log rendered as sentences — `alertPhrase()` in
`src/features/dashboard/WorkbenchPage.tsx` maps the recorded route pattern (`METHOD /api/v1/...`,
braces and all) to a whole-phrase translation key, falling back to the trimmed path for a route the
table does not know, so a row reads "Node egress probed" and not "POST
/api/v1/nodes/{hash}/actions/probe-egress". It is the audit trail, not a synthetic alert stream;
the method chip keeps its tone colour and the raw record stays in the row's `title`.
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
| The *rendered page* is legible in both themes | `npm run test:e2e` → "every theme is legible, not just the one the art was drawn in". `check:contrast` reads the `--p-*` primitives, so a colour written into a component's own rule is invisible to it — and the workbench was full of them. This check screenshots each theme with the words made transparent, samples the **painted** ground under every text element, and grades the computed ink against it at WCAG 1.4.3. **One** shortfall is exempted as the reference art's own value (4.43:1, the blue badge wash in the hero's fourth chip), pinned to the ratio it renders so the exemption cannot quietly widen — the number is `.wb-metric-badge-down` in `ART_EXEMPTIONS`, and the chip's opaque night gradient is why the frosted-glass pass did not move it; the two the board used to carry (4.44:1 on the hero description, 4.05:1 on the status line) went with the pixel replica, because the band and the status line are token surfaces now and clear 4.5:1 on them |
| The kit is the only source of controls | `npm run check:kit` reads the `.tsx` sources: native `<select>`/`<input>`/`<button>`/`<textarea>` outside `src/components/ui/`, hard-coded control heights **anywhere** (a page that sizes a control has made the same per-page decision the kit may not make), and `rounded-full` anywhere but `Badge`. In `make test-web` → `make verify` |
| The board stays a responsive Bento grid | `npm run check:responsive` reads `design.css` and fails if the geometry comes back: a `@media (min-width: 1536px)` block, a fixed pixel height on `.wb-board`/`.wb-shell-root`/`.wb-main-zone`, a `position: absolute` rule naming a board pane, or a board rule that has been renamed or deleted (so the gate cannot pass on an empty stylesheet). In `make test-web` → `make verify`; the browser-side legibility and geometry measurements are `npm run test:e2e` and the root `DESIGN.md` |
| The kit's own invariants | `tests/kit.test.mjs` — the `Button` single-child rule, token heights, the pill reservation, that the height rule carries no scope guard, and that no control renders with an empty `onChange`. Each with the reason it exists |
| Row height and panel geometry | DOM audit of the live pages: `--row-h`, pane radius 20, rail width 248/64, the top bar at 56, and the ground's glow |
| Types are really checked | `npm run check:types` runs `tsc -p tsconfig.app.json --noEmit`. **`npx tsc --noEmit` at the repo root is a no-op** — `tsconfig.json` is a solution file with `files: []` — so it proves nothing |
| Focus is visible | `:focus-visible` draws 2px accent with 1px offset |
| Reduced motion | The media query at the end of `design.css` |
| The chart palette is current, not just present | `tests/chart-palette.test.mjs` — the mirror of `design.css` in `chartPalette.ts`, checked colour by colour, in the series order, on the band ramp's luminance spread and on the font families. In `make test-web` |
| Every visible string is translated | `tests/i18n-coverage.test.mjs` — every `t("…")` literal against the dictionaries. Chinese **is** the key (`buildZhTranslations()` maps each key to itself), so a missing entry is invisible in the default locale and silently Chinese in English: 38 had accumulated that way, 30 of them in the platform criteria form, a page the rebuild never touched. The scan survives the three shapes that fooled its first version: bare CJK keys (`视图: "View"` is legal — CJK characters are identifier characters), URLs inside strings (`//user:pass@host`, which a `//`-strips-comments pass eats), and function names ending in `t` (`default("http:…")`). In `make test-web` → `make verify` |
| Anti-patterns | `make test-slop` — see the detector section below. **It does not read `.tsx`**, which is why `check:kit` exists. Findings it prints are attributed (provenance or a recorded decision), never dropped, and the file-scoped rows it used to lose now fail the gate |

### How the console learns whether it needs a token

`/ui/session.json` answers `{"auth_required": bool}`, served by the SPA handler from
the deployment's own admin token (`internal/api/webui.go`). The login page asks before
it holds a token; the protected routes ask too, to tell an anonymous deployment from a
secured one.

The previous shape — an unauthenticated probe of `GET /api/v1/system/info`, reading
401 as "auth is on" — gave the right answer through a request the browser itself logs
as a console error, so the first screen of every secured deployment carried noise the
operator could not clear. `/ui/` is already gated by the access point's
`allow_management`, so the answer reaches exactly the callers that can load the console
anyway, and the deployment states the fact instead of a client inferring it.

Both answers are pinned by `TestWebUISessionModeAnswersBothWays`: a handler that only
ever said `true` would send an anonymous deployment to a login form that cannot accept
anything.

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
