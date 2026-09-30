## What this changes

<!-- One paragraph. If it fixes a bug, say what the bug was. -->

## Why

<!-- The reason for this approach, and what you considered instead. -->

## How it was verified

<!--
Which checks ran, and any manual step that was needed. "make verify passes" is
not enough for a routing, persistence, security or protocol change: say what
behaviour you exercised and what you observed.
-->

- [ ] `make verify` passes
- [ ] `make smoke` passes (needs `make backend` first)
- [ ] A test fails without this change, and passes with it
- [ ] `docs/` updated if documented behaviour changed

## Scope

<!--
Anything the reviewer should know: a migration, a compatibility change, a
follow-up deliberately left out, or a part of the change that is unverified.
-->
