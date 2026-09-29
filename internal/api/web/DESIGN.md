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
| `Page` / `PageHeader` / `PageMeta` | Page skeleton. Header is a fixed 52px, title 18/600, description 12px at ≤68ch, actions on the right |
| `Panel` / `PanelHeader` / `PanelToolbar` / `PanelBody` / `PanelFooter` | A region. Header 44px, padding 16, title 14/600. **Actions are visible, never hover-revealed** — an action behind `opacity: 0` does not exist on a touch screen |
| `Table` / `THead` / `TH` / `TBody` / `TR` / `TD` / `TDNum` / `TDClip` | The data grid. Fixed row heights 32/28/36, sticky head, **row rules only, no cell borders**; long text must use `TDClip` |
| `Readout` / `ReadoutStrip` / `ReadoutCell` / `Numeral` | Instrument readings and the count-up |
| `Button` / `Input` / `Textarea` / `Select` / `Switch` | Controls, 28px tall (sm 24 / lg 32) |
| `Badge` | Status. The only fully-round shape in the system, so the shape itself says "this is a state" |
| `Tabs` / `Tooltip` / `Sheet` / `Toast` | Overlays and feedback |
| `LoadingState` / `ErrorState` / `EmptyState` | The three states, identical everywhere |

**Radix owns behaviour; this repository owns appearance.**

Charts are ECharts. The console's 3D globe is `echarts-gl` with a texture drawn at
runtime from the repository's own `public/world-110m.geo.json` — no added asset, no
network call at render time. Two `echarts-gl@2.1.0` traps are worked around in
`EgressGlobe.tsx` and commented there: handing it a canvas as `baseTexture` turns the
sphere white on the second render, and a colour-typed `environment` smears into an
opaque black block. WebGL unavailable falls back to the flat map.

## Layout

```
┌──────┬────────────────────────────────────────────┐
│ rail │ top bar 48: location · instance state · lang│
│ 232  ├────────────────────────────────────────────┤
│      │ page header 52: title · meta · actions (sticky)│
│ sect │────────────────────────────────────────────┤
│ micro│ panel grid (8px gap, unconstrained above 2560)│
└──────┴────────────────────────────────────────────┘
```

- The rail is `bg-rail`; the **current destination is a filled pill** (accent wash,
  600 weight, raised sheet) — **no coloured side bar**. A coloured edge on a list row
  is the loudest generic UI tell, and it says nothing the fill did not already say.
- The rail collapses to 56px below 1440 rather than holding 232 on a small laptop.
- Content width **flows and is not capped**. shadcn's `max-w-7xl` wastes half a 2560
  display.
- Dense tables set a `min-width` and scroll horizontally instead of compressing
  columns; every cell is `nowrap`. Measured at 1280: container 1172 = content 1172,
  no compression, no overflow.
- Sections are separated by a rule or a panel, never by stacked whitespace.

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
| Contrast and series separation, both themes | `npm run check:contrast` — **86 pairs**, light and dark. In `make test-web` → `make verify`, so a palette regression fails CI like a Go test |
| Row height and panel geometry | DOM audit of the live pages: `--row-h`, panel radius 8, rail width, rhythm on 6/8/12/16 |
| Types are really checked | `npm run check:types` runs `tsc -p tsconfig.app.json --noEmit`. **`npx tsc --noEmit` at the repo root is a no-op** — `tsconfig.json` is a solution file with `files: []` — so it proves nothing |
| Focus is visible | `:focus-visible` draws 2px accent with 1px offset |
| Reduced motion | The media query at the end of `design.css` |
| Anti-patterns | `make test-slop` — see the detector section below |

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
