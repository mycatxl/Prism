# Prism console design rules

The durable design contract for `internal/api/web`. It is not a vision document:
every rule below is either enforced by a script in the gate list, or it is a
decision recorded so the next change does not re-litigate it. When the code and
this file disagree, one of them is a bug — and the burden is on the change to
say which.

The world is **the daylight desk**: a lit work surface. Cool neutral paper,
hairline rules instead of floating tiles, mono figures for everything that is
read or compared, one governed indigo for interaction, one narrow shadow ladder
spent on overlays. A region is a *band on that surface*, not a tile on a board:
separated by a rule and a stated head row, and it never lifts, blurs or glows.

The stylesheet does not use the words "glass", "card", "bento" or "board". Those
belonged to a previous world; a stylesheet that keeps a dead world's vocabulary
invites its shapes back. `check:responsive` fails on the retired names.

## 1. Composition

- A page composes `src/components/ui/`. It does not create its own control.
- Enforced by `check:kit` (native tags) and `tests/kit.test.mjs`. A native tag
  outside the kit fails the gate unless it is on the short, justified allow-list
  in `scripts/check-kit.mjs`. `type="hidden"` inputs are the routine case.
- 81 `.tsx` files currently pass. The count is the gate's output, not a target.

## 2. Control sizes come from tokens

- `--control-h-sm: 24px`, `--control-h: 28px`, `--control-h-lg: 32px`,
  `--control-h-xl: 36px`.
- A control takes its height from `var(--control-h…)`, never from an `h-*` step —
  not even `h-7`, which happens to equal 28px today. That coincidence is the
  trap: the token moves and the hard-coded control does not.
- Enforced by `check:kit` rule 2 (which bans `h-6.5/7/8/9/10` outright) and by
  `kit.test.mjs`.

## 3. Shape carries meaning

- `--radius-control: 6px`, `--radius-card-sm: 8px`, `--radius-panel: 8px`,
  `--radius-chip: 4px`.
- `rounded-full` belongs to `Badge` alone. Badge is the only pill in the system,
  so a second pill spends the signal that says "this is a status".
- A status chip clips at 4px. It must never read as a button.
- `Switch` is a rectangle (`rounded-control`): an 18px visual track, with the hit
  area padded to `--control-h` by a `before:` pseudo-element.
- Enforced by `check:kit` rule 3 and `kit.test.mjs` ("the pill shape is reserved
  for Badge").

## 4. Colour

- Two themes, one token set, full parity. Every colour is a primitive (`--p-*`)
  declared twice in `design.css` — `:root` (light) and `[data-theme="dark"]`.
  Components reference the semantic alias (`--color-ink`, `--color-accent`),
  never the primitive.
- The surfaces that carry text — `--p-canvas`, `--p-sunk`, `--p-raised`,
  `--p-inset`, `--p-rail`, `--p-elevated` — stay **opaque hex**, so contrast is
  measured against what a reader actually sees. `check-ui-live` reads the first
  `:root` block and asserts the page painted those tokens rather than falling
  back to black on white.
- Text that sits *on* a filled colour takes `--color-on-accent`,
  `--color-on-signal`, `--color-on-alert` or `--color-on-live`. In dark, a bright
  state ink takes dark text; in light it takes white. These are tokens, not
  constants, for exactly that reason.
- `accent` means interaction, selection and focus. `signal` / `live` / `warn` /
  `alert` mean state. They are not interchangeable: colouring a status with
  `accent` removes it from the status vocabulary.
- State is never carried by colour alone. A state gets a word, or a shape, as
  well.
- Enforced by `check:contrast` (92 pairs, both themes, WCAG AA) and
  `tests/design-tokens.test.mjs` (the alias layer must stay `static`).

## 5. Seven states, every control

Every interactive component defines all of: default, hover, focus-visible,
active, disabled, loading, invalid/error. The list is the contract; a control
missing one is incomplete, not minimal.

- `Button` carries `loading`, which renders a spinner **and** sets `disabled`
  **and** sets `aria-busy`. A page writes `loading={mutation.isPending}`, never a
  hand-swapped label. `loading` is forbidden together with `asChild`: Radix `Slot`
  throws on two children, and neither `check:types` nor `eslint` sees it at
  runtime. That prohibition is typed and locked by `kit.test.mjs`.
- Enforced by `kit.test.mjs` ("no control is rendered without a way to operate
  it"), which reads the kit source. `check:kit`'s three rules do not cover it, so
  the test is the gate here.

## 6. The shell is three cells

    rail (brand, destinations, state foot)   instance bar
                                             work area

- **Rail** states the product and its state: mark, name, role, the destinations,
  then a fixed foot with the status line and the language/theme controls. The
  middle scrolls; the brand row and the foot do not.
- **Instance bar** states where you are and what this browser can do from here:
  the destination (breadcrumb), the command palette, the account menu. It is
  sticky and owns `--shell-bar-h`.
- **Work area** is the page. One `PageHeader`, then panels.
- No control appears in both cells. There is one place to look for a
  destination, one for the account, one for the instance's health.
- Rail groups are numbered (`01`…`06`) from the same `navItems` array the rail
  renders, so an operator can quote a group without a second list existing.
- Below 1100px the rail becomes a drawer (`z-60`, scrim `z-55`, `Escape` closes,
  focus returns to the trigger), the bar stays sticky at `z-40`, and
  `.page-header` offsets by `--shell-bar-h` so it docks under the bar instead of
  scrolling behind it. CSS owns both stickiness and fill: utilities win over
  component-layer rules and blocked the offset.
- The command palette is one keyboard surface (⌘K / Ctrl+K) over the rail plus
  the five actions the shell owns: go, refresh data, switch theme, switch
  language, account, sign out. It does not search nodes, users or requests —
  those are paginated server queries with their own filters, and a client-side
  "search" over the rows a page happens to hold answers a question nobody asked
  with data that is already stale. A palette that invents results is worse than
  no palette.
- The palette is lazy: `cmdk` arrives with its own chunk on first open, and the
  shell keeps the trigger and the shortcut.

## 7. Data surfaces

- A page's primary surface is a table, a form or a chart — not a tile wall.
- A panel is a band, not a floating card: top hairline rule and a stated head
  row, no radius, no shadow. `.panel--ruled` draws that rule 2px when the panel
  opens a page section; `.panel-group` is the one framed container, for a set of
  panels that genuinely belong together.
- Shadows belong to overlays only (dialog, sheet, menu, toast). A surface on the
  desk is separated by a rule.
- At most three surfaces deep: canvas → selected surface → inset.
- A sticky table head is opaque (`--color-paper-raised`) and covers the row
  passing under it. A translucent head is a legibility bug at speed.
- Numbers, IPs, hashes, ports and timestamps use `--font-mono`. Mono is for
  values that are read or compared, not decoration.
- Loading, error and empty states keep the height of the content they replace, so
  the page does not jump.

## 8. Status is a word

`Badge` renders the pill; `StatusMark` renders the dot-and-label. Different jobs:
a status list uses `StatusMark`, a label in a table uses `Badge`.

The shell's status line always states a verdict in words, and only about what
this browser can actually know:

| Situation | Line |
| --- | --- |
| Operator, probe answered | 实例在线 |
| Operator, probe in flight | 连接中 |
| Operator, probe failed, or the browser is offline | 连接中断 |
| Operator, 401 | 会话失效 |
| Operator, `proxy_token_set` false | 旧版令牌配置仍存在 |
| Tenant, reachable | 服务已连接 |
| Tenant, offline | 当前处于离线状态 |

A tenant has no instance probe to read, so their line reports the connection
instead of borrowing the operator's verdict. "Connecting…" shown forever was the
old behaviour; an unknown state reading as a pending one is the failure this
line exists to prevent.

## 9. Truth rules

- Never invent data. Empty is empty, error is error, loading is loading. A page
  renders what the server returned and says so when it returned nothing.
- Unknown is its own state. It is not healthy and it is not failure.
- A number on screen is reachable from its source: the query key is in
  `src/features/**/api.ts`, the money is integer micro-USDT, and nothing is
  re-derived on the client that the server already decided.
- Chinese and English are both first-class: no string ships in one language only,
  and no layout depends on one language's word lengths.
| navigation | `npm run check:nav` | the rail, the router and the page headings stay one table; every label and group is translated; groups stay scannable |
  390×844 with a keyboard-free path to every control.

## 10. Gates

| Gate | Command | What it proves |
| --- | --- | --- |
| kit | `npm run check:kit` | no native controls, no hard-coded heights, no stray pills |
| kit behaviour | `npm run test:config` | the `asChild`/`loading` prohibition, control heights, pill reservation, token aliases, i18n coverage, plan units |
| contrast | `npm run check:contrast` | both themes clear WCAG AA on every text pair |
| layout | `npm run check:responsive` | no fixed pane geometry, no 1536px layer, no retired world names |
| navigation | `npm run check:nav` | every destination has a section, a label and a role |
| anti-patterns | `npm run check:slop` | impeccable's detector, run over the built tree |
| types | `npm run check:types` | the component contracts hold |
| live | `npm run test:e2e` | routes render, no console error, no removed class token, tenant isolation, both themes, focus visible |
| build | `npm run build` | the console compiles with the real bundle graph |

## 11. Not allowed

- Gradient text, fake chrome, decorative icon tiles, ground halos, glass, blurred
  panels, two-axis grid textures.
- A hero on a working page, or a marketing layout on a login or 404 page.
- A panel nested in a panel purely for the border.
- Hard-coded "all systems operational".
- Presenting unknown as healthy or as failure.
- A card that exists because a grid wanted a fourth cell.
