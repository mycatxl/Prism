# Prism console design rules

This is the durable design contract for `internal/api/web`. It is not a vision
document: every rule below is either enforced by a script in `npm run verify`
terms or it is a decision recorded so the next change does not re-litigate it.
When the code and this file disagree, the code is the bug unless the rule says
otherwise.

The direction is **Prism Precision Glass**: dense instrument panel, real
evidence over decorative cards, glass only where it separates chrome from
content.

## 1. Composition

- A page composes `src/components/ui/`. It does not create its own control.
- Enforced by `npm run check:kit` (rule 1, native tags) and `tests/kit.test.mjs`.
  A native tag outside the kit fails the build unless it is on the short,
  justified allow-list in `scripts/check-kit.mjs`. `type="hidden"` inputs are
  the one routine case.
- 73 `.tsx` files currently pass. The number is the gate's output, not a target.

## 2. Control sizes come from tokens

- `--control-h-sm: 24px`, `--control-h: 28px`, `--control-h-lg: 32px`,
  `--control-h-xl: 36px` (`src/styles/design.css`).
- A control takes its height from `var(--control-h…)`. Never from a `h-*` step,
  even `h-7` — it happens to equal 28px today, which is exactly why hard-coding
  it is a trap: the token moves and the control does not.
- Enforced by `check:kit` rule 2 and by `kit.test.mjs` ("the kit declares the
  control height it uses").

## 3. Shape carries meaning

- `rounded-full` belongs to `Badge` alone. Badge is the only pill in the system,
  so a second pill spends the signal that "this is a status".
- `Switch` is a rectangle (`rounded-control`). Its visual track is 18px; the
  touch target is padded to `--control-h` (28px) with a `before:` pseudo-element,
  because a 18px hit area fails on touch.
- Radii: `--radius-control: 8px`, `--radius-card-sm: 12px`,
  `--radius-panel: 16px`, `--radius-chip: 999px` (Badge only).
- Enforced by `check:kit` rule 3 and `kit.test.mjs` ("the pill shape is reserved
  for Badge").

## 4. Colour

- Two themes, one token set. Every colour is a primitive (`--p-*`) defined twice
  in `design.css`, once per theme. Components reference the semantic alias
  (`--color-ink`, `--color-accent`), never the primitive.
- The surfaces that carry text — `--p-canvas`, `--p-sunk`, `--p-raised`,
  `--p-inset`, `--p-rail`, `--p-elevated` — stay **opaque hex** equal to the
  composited glass, so contrast is computable rather than guessed.
- `accent` means interaction, selection and focus. `signal` / `live` / `warn` /
  `alert` mean state. They are not interchangeable: colouring a status with
  `accent` removes it from the status vocabulary.
- State is never carried by colour alone. A state gets a word or a shape as well.
- Enforced by `npm run check:contrast`, which reads the tokens in `design.css`
  and fails any pair below WCAG AA.

## 5. Seven states, every control

Every interactive component defines all of: default, hover, focus-visible,
active, disabled, loading, invalid/error. The list is the contract; a control
that lacks one is incomplete rather than minimal.

- `Button` carries `loading`, which renders a spinner **and** sets `disabled`
  **and** sets `aria-busy`. A page writes `loading={mutation.isPending}`, not a
  hand-swapped label. The `loading` prop is forbidden together with `asChild` —
  Radix `Slot` throws on two children, and neither `check:types` nor `eslint`
  sees it at runtime. That prohibition is typed and locked by `kit.test.mjs`.
- Enforced by `kit.test.mjs` ("no control is rendered without a way to operate
  it"), which reads the kit source. `check:kit` has three rules and none of them
  covers this, so the test is the gate here.

## 6. Data surfaces

- A page's primary surface is a table, a form or a chart. Not a card wall.
- Bento grids are for the dashboard's composite evidence only. Everywhere else a
  panel directly carries a real table, chart, form or state list; a panel that
  exists only to draw a border around another panel is a defect.
- At most three surfaces deep: canvas → selected surface → inset.
- Numbers, IPs, hashes, ports and timestamps use the mono face. Mono is for
  values that are read or compared, not decoration.
- Loading, error and empty states keep the height of the content they replace, so
  the page does not jump.

## 7. Meaning of a status

`Badge` renders the pill; `StatusMark` renders the dot-and-label. They are
different jobs. A status list uses `StatusMark`; a label in a table uses `Badge`.

## 8. Accessibility and motion

- Every control is reachable and operable by keyboard, with a visible focus ring.
- `prefers-reduced-motion` removes transitions; it never removes the state
  change itself.
- No text is conveyed only by colour, and no page overflows horizontally at any
  supported width.

## 9. What this file is checked against

| Gate | Command | What it proves |
| --- | --- | --- |
| kit rules | `npm run check:kit` | no native controls, no hard-coded heights, no stray pills |
| kit behaviour | `npm run test:config` | the `asChild`/`loading` prohibition, control heights, pill reservation |
| contrast | `npm run check:contrast` | both themes clear WCAG AA on every text pair |
| responsiveness | `npm run check:responsive` | no unintended horizontal overflow at the supported widths |
| types | `npm run check:types` | the component contracts hold |
| live panel | `npm run test:e2e` | routes render, rail reaches every destination, tenant isolation, themes legible, focus visible |

## 10. Not allowed

- Gradient text, fake chrome, decorative icon tiles, ground halos.
- A hero on a working page, or a marketing layout on a login or 404 page.
- A panel nested in a panel purely for the border.
- Hard-coded "all systems operational".
- Presenting unknown as healthy or as failure. Unknown is its own state.
