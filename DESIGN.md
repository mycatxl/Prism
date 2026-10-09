---
id: prism-console-visual
type: module-design
title: Prism Precision Glass console visual system
status: active
parent: prism-product
tags:
  - frontend
  - visual-system
  - precision-glass
---

# Prism Precision Glass

Prism is an operator console for measured node egress, traffic, quality and routing.
The visual system makes that evidence easy to scan without turning the application into a
wall of decorative cards.

> **Design claim.** Use light glass to frame context, use a small number of unequal Bento
> regions to express real priority, and let tables, charts and forms carry the work.

This is an Operate surface. Familiar affordances, truthful data, durable reading rhythm
and complete keyboard/touch operation outrank visual novelty.

## Product truth

- Prism is single-user and self-hosted. Do not add tenant, plan, avatar, notification or
  marketing chrome.
- A displayed value must come from an existing API response or a documented calculation.
  Show age, source or statistical basis when the value can otherwise be misread.
- Dashboard is an all-day monitoring wall. Its first evidence is traffic and connections;
  it is not an entity search surface.
- The console supports Chinese and English, light and dark themes, loading/error/empty
  states, keyboard focus, reduced motion and complete mobile operation.

## Visual world

### Material

**Precision Glass** is a quiet SaaS workspace, not a glassmorphism demo.

- Light is the default theme. The canvas is a cool near-white (#f3f5fb) with white panes
  in light mode, and deep ink-blue in dark mode.
- Chrome (rail, top bar and overlays) may use a restrained translucent surface with a
  10–12px blur and a one-pixel hairline. It must have an opaque fallback.
- Dashboard lead evidence panes may use the same light glass treatment. Their charts and
  maps always sit on an opaque inset well so data stays legible.
- Ordinary data and configuration pages use opaque work surfaces with no backdrop blur.
- A pane is a region containing a real table, chart, form or state list. A pane may not
  exist only to wrap another pane.
- Main panes use a 16px radius, inset wells 12px, controls 8px. Shadows are quiet on
  ordinary panes and stronger only on Sheet/Dialog/Tooltip overlays.
- The dashboard welcome band is the one place a faint accent wash (two radial tints in
  its corner) is allowed. No other hero gradient, no gradient text, ground halo, decorative icon tile, zero-offset glow,
  or full-screen purple wash.

### Palette

These are the starting primitives. Contrast checks are authoritative and may require a
value to be solved while preserving the role.

| Token | Light | Dark | Role |
|---|---|---|---|
| `canvas` | `#e8eef6` | `#0a1220` | page ground |
| `rail/chrome` | `#f3f7fb` | `#101a2a` | navigation and top chrome |
| `surface` | `#f5f8fc` | `#182438` | selected/lead work surface |
| `inset` | `#dce6f0` | `#121d2e` | chart, map, table and code well |
| `elevated` | `#ffffff` | `#1d2b40` | overlay fallback |
| `ink` | `#142033` | `#f1f4f8` | primary text |
| `ink-soft` | `#4c5c72` | `#aab7c9` | secondary text |
| `accent` | `#3e5db8` | `#8294ff` | action, selection and focus only |
| `signal` | `#087254` | `#42c396` | healthy/success |
| `live` | `#0879a5` | `#58bfea` | current/in flight |
| `warn` | `#8a5700` | `#e1a13d` | stale/degraded |
| `alert` | `#b4382c` | `#f18a7e` | error/risk |

Accent never means healthy. State colour never stands alone: pair it with text, a shape,
position or a number. Chart series are separate from state colours and remain separable in
greyscale.

### Type and density

- Manrope remains the UI/body face; IBM Plex Mono is reserved for IPs, hashes, ports,
  timestamps, JSON and values that are compared.
- Body is 14–15px, page title 20–22px, pane title 14–16px, primary readings 24–30px.
  Sentence case is the default; micro labels are for table heads and compact metadata,
  not decorative eyebrows.
- Use an 8px rhythm with 4px adjustments. Page gutters are 24px at wide desktop, 16px
  around 1280px and 12px at 390px. Normal section gaps are 16px; tight groups are 12px;
  pane content is normally 16px.
- Whitespace must separate a heading from its evidence and preserve a reading rhythm. Do
  not use 40–60px empty bands to imply luxury, and do not compress unrelated controls into
  one dense strip.

## Shell and navigation

- The top bar contains current location, the one authoritative instance connection state,
  token warning when real, theme, language and logout. It does not contain global search,
  an unscoped refresh button or a hard-coded "all systems operational" statement.
- Dashboard has no search. Search belongs beside the table or workflow that owns the
  entity: nodes, subscriptions, platforms, request logs, jobs, audit, rules and config.
- Navigation groups destinations by work: Workspace (overview, nodes, subscriptions,
  platforms, jobs) and Observability & Settings (request logs, endpoints, rules,
  GeoIP, system config, audit).
- Expanded rail is about 232–248px; below 1440px it may collapse to an icon rail, and
  below 1024px it becomes a drawer. The active destination uses a quiet filled wash and
  text weight, never a thick coloured side tab.

## Automatic freshness

Dashboard data updates from the real query cadence already supplied by the metrics API:
realtime series use their reported step, history uses its bucket cadence, snapshots and
node exits use their existing intervals. The UI exposes:

- selected range;
- last successful data time or source timestamp when available;
- a restrained updating indicator while polling;
- an error message and retry action only when polling fails.

There is no permanent "Refresh data" button on Dashboard. A retry button belongs to an
error state because it recovers a known failure; it is not a second refresh model.

## Dashboard composition

Dashboard is the one primary Bento surface. Its DOM order is also its mobile order:

1. **Page context** — range, freshness, map expansion and node-pool link. No search.
2. **Pool snapshot strip** — one compact readout band for healthy/total nodes,
   healthy/total egress IPs, covered regions and subscription health.
3. **Traffic trend, 7/12** — the main monitoring evidence. Requests, estimated average
   latency, error rate and active leases appear once above the real ingress/egress/
   connections chart.
4. **Egress map, 5/12** — offline GeoJSON/d3-geo map, real origin-to-hub relationships
   and a readable region index. Expand remains a viewport Dialog.
5. **Latency, Platform, Subscription, 4/12 each** — compact diagnostic and outcome
   evidence. Platform distribution may use a real donut with a readable list; it is not
   decorative.
6. **Recent system changes, 12/12** — the recent audit write history. It is not called
   an alert feed and does not imply a threshold engine.

Do not add independent instance, quick-action, popular-region or recently-added-node cards.
Those facts have an owning page or belong in the evidence group above.

## Page families

- **Inventory:** Nodes and Subscriptions use one stable table work surface. Common search
  and filters stay in one toolbar; advanced filters use a Filter Sheet; details use a Sheet.
- **Delivery:** Platforms, Platform detail, Exports, Endpoints and Rules use table/workflow
  surfaces and focused detail Sheets. Platform detail may use tabs for monitor/access/
  config/ops; it does not flatten every concern into cards.
- **Observability:** Request Logs, Jobs and Audit prioritize dense tables, filters, queue
  progress and details. Their searches live beside their own records.
- **Resources & Settings:** GeoIP is a database-status and IP-lookup workbench. System
  Config is a category index plus one current editor; RuntimeConfig stays editable and
  EnvConfig stays a clearly read-only deployment fact.
- **Auth/fallback:** Login and 404 are quiet single work surfaces with clear recovery.

## Bento boundaries

- Dashboard may use one unequal grid: Traffic 7/12 + Map 5/12, then Latency/Platform/
  Subscription 4/4/4, then Recent changes 12/12.
- Platform detail and System Config may use two-column workspaces because the columns have
  distinct tasks. This is not permission to create a decorative card wall.
- Every other page is primarily normal flow: one main work surface, one table/form/diagram,
  and detail overlays where needed.
- Maximum surface depth is canvas → selected surface → inset. Readout cells are part of a
  strip, not individual panels. Tables remain tables; form fields remain a semantic field
  flow; pagination and empty/error states remain workflow regions.

## Data, accessibility and motion

- Keep the existing API, routes, URL parameters (`range`, node filters, `selected`,
  `category`), calculations, Recharts, offline d3-geo map, pagination, SSE/polling,
  RuntimeConfig PATCH and EnvConfig read-only semantics.
- Loading, empty and error states must preserve the surrounding track and explain recovery.
- Focus is visible with one shared accent ring. Touch actions are visible without hover.
- Motion communicates polling updates, sorting, saving, opening/closing or real map activity.
  No page-load choreography, decorative pulsing dots or automatic camera motion. Reduced
  motion disables map pulses, count-up and nonessential transitions.

## Verification

- `npm run check:types`, `npm run lint`, `npm run build`, `npm run test:config`.
- `npm run check:kit`, `npm run check:contrast`, `npm run check:responsive`,
  `npm run check:slop`.
- Real backend Chromium verification at 1440×900, 1280×800 and 390×844 in both themes.
  Dashboard must contain no search input and no permanent refresh button; only an error
  state may expose retry. Page-level horizontal overflow is forbidden; intentional table
  overflow is confined to its wrapper.
