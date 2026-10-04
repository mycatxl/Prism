---
id: prism-console-implementation
type: submodule-design
title: Prism Precision Glass console implementation design
status: active
parent: prism-console-visual
tags: [frontend, implementation, precision-glass]
---

# Prism console implementation

The visual system is recorded in the root [DESIGN.md](../../DESIGN.md). This document
records implementation boundaries that are easy to drift: component responsibilities,
query freshness, page composition and acceptance contracts.

## Source of UI truth

`src/components/ui/` owns controls and shared surfaces. Feature pages compose those
components and own only business-specific arrangement and data mapping. A page must not
invent a native button, input, table surface or one-off state component.

The shared primitives are:

- `Page`, `PageHeader`, `PageMeta`: location, scope, tabs and page actions. A page header
  does not repeat the title of the work surface below it.
- `Panel`, `PanelHeader`, `PanelToolbar`, `PanelBody`, `PanelFooter`: one real work or
  evidence region. The default surface is opaque; a lead Dashboard region may opt into
  light glass. A panel must not be nested only to gain another border.
- `Readout`, `ReadoutStrip`, `ReadoutCell`, `Numeral`: values and compact evidence bands.
  A cell is not a standalone card.
- `StatusMark`: a square marker, status text and optional age/value. It is for runtime
  state and does not turn every label into a pill. `Badge` remains for short state or
  filter tags.
- `DataFreshnessBar`: selected range, last successful update/source age, polling state
  and an error retry action. It never renders a permanent Dashboard refresh button.
- `Table`, `TableWrap` and data-grid cells: the primary surface of inventory, delivery,
  observability and audit pages. Deliberately wide tables scroll inside their wrapper.
- `LoadingState`, `ErrorState`, `EmptyState`: the only shared query states. They preserve
  the surrounding region's layout and explain recovery.
- `Sheet`, `Dialog`, `Tabs`, `Toast`: focused workflows and details. Radix owns focus
  capture, Escape, scrim dismissal and focus return.

## Query and freshness contract

- The AppShell owns the instance connection query. It exposes only instance version,
  connection/auth state and real token configuration status to the shell.
- Dashboard owns its snapshot, realtime, history, node-exit, platform, subscription and
  audit queries. Query keys must not be reused with incompatible response shapes.
- Dashboard polling uses the API's actual reported step/bucket cadence through the existing
  range helpers. Snapshot, node exits, platform, subscription and audit cadences remain
  bounded and data-driven.
- The Dashboard has no search and no permanent manual refresh control. The page shows the
  current range, last successful update/source age and a restrained fetching marker. A
  retry button appears only inside an error state.
- `range` remains in the URL. Existing feature filters and selection parameters remain in
  their owning routes.

## Dashboard contract

The Dashboard is the only primary Bento composition. DOM order is the mobile reading order:

1. context and freshness;
2. one Pool snapshot strip;
3. Traffic trend / Egress map, 7/5;
4. Latency / Platform / Subscription, 4/4/4;
5. Recent system changes, full width.

Traffic trend owns the single displayed average latency, success/error basis and active
lease reading. Pool snapshot owns node and egress-pool counts. Instance state is never a
Dashboard pane. Audit rows are recent changes, never a fabricated alert stream.

The map continues to use offline GeoJSON, `d3-geo`, `ResizeObserver`, fitted `viewBox`,
accessible marks, real origin/hub relationships and the viewport Dialog. No guessed
coordinates or runtime map service may be introduced.

## Page contracts

- Nodes and Subscriptions: one stable table work surface, one common toolbar, advanced
  Filter Sheet, and detail Sheet. Search belongs here because the task is entity location.
- Platforms, Platform detail, Exports, Endpoints and Rules: table/workflow first; focused
  Sheets for creation, editing, testing, token rotation and dangerous operations.
- Request Logs, Jobs and Audit: dense time/queue tables, owning filters, progress and
  details; no summary-card wall.
- GeoIP: database status and IP lookup as two related tasks, not invented KPI cards.
- System Config: category index plus one editor. RuntimeConfig remains editable; EnvConfig
  is a read-only deployment fact. Dirty forms use the shared SaveBar.
- Login and 404: quiet single work surfaces with clear recovery and shared focus states.

## Responsive contract

- At 1280px the rail may be narrow and the Dashboard still keeps Traffic and Map as the
  primary pair; at 1024px the rail becomes a drawer.
- At 390px Dashboard is one column in the same evidence order. Search, filters, details,
  common edits and save actions remain reachable. Complex filters/details/configuration use
  full-screen Sheets or work surfaces.
- Page-level `scrollWidth` must equal `clientWidth`. Only an intentional table/code/map
  wrapper may scroll horizontally, and it must retain a visible focus/scroll affordance.
- Loading, error and empty states do not cause unrelated columns to jump in height.

## Verification contract

Run from `internal/api/web` with the Linux-native Node toolchain:

- `npm run check:types`, `npm run lint`, `npm run build`, `npm run test:config`;
- `npm run check:kit`, `npm run check:contrast`, `npm run check:responsive`,
  `npm run check:slop`;
- real backend Chromium at 1440×900, 1280×800 and 390×844 in both themes.

The final browser audit must prove that Dashboard has no search input and no permanent
refresh button, that instance state appears only in Shell, that the traffic/map composition
is readable, and that mobile table overflow is confined to its wrapper.
