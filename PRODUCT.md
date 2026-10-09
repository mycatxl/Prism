---
id: prism-product
type: goal-and-requirements
title: Prism product context
status: draft
tags:
  - product
---
# Prism product context

> **Provenance.** This file exists because the design workflow reads product context
> before it decides anything, and the project had none. It was written by inference
> from the repository and from the brief that drove the work — no interview was held.
> Every assumption is marked **[assumed]** and should be corrected rather than
> trusted. Nothing here is invented product truth; where the repository did not
> answer a question, the gap is named.

## Users

**The operator** — one person, running Prism on their own machine or VPS. They import
nodes from subscriptions and public sources, they want to know what each node's real
egress is and how clean it is, and they consume the result through their own client
(HTTP / SOCKS5 / reverse proxy, or a subscription). They are looking
at the console the way a technician looks at a bench: which address a request came out
of, how fresh the reading is, what failed and why.

**Other people who deploy it** — the project is published to be self-hosted, so a
second operator runs it on hardware the author has never seen: different screen size,
different timezone, different node mix. **[assumed]** The brief said explicitly that
deployment targets must not be assumed to match the author's own device, so the
console is designed fluid, not against one laptop.

**Not a user: a multi-tenant audience.** There is no SaaS, no plan, no payment, no
tenancy in scope. **[assumed]** The scope is fixed at personal use plus open-source
distribution.

## Purpose

Turn a pile of imported nodes into a **measured, classified, attributable** pool, and
serve it back through rules the operator defines. Three outcomes matter:

1. **What actually leaves.** A node's configured address is a claim; its egress is a
   fact. Facts come from probes, and a probe result carries its timestamp, its source
   and its evidence.
2. **How clean that egress is.** Residential vs datacenter vs business, reputation,
   blocklists, purity bands. A node that reaches the internet from a flagged address
   is worth less than one that does not, and the operator should be able to see why.
3. **Which nodes a given surface gets.** A Platform is the operator's selection of
   nodes for one purpose. Selection is by node criteria (region, network type, purity
   band, subscription, protocol — **AND across criteria, OR within a criterion**), and
   what a Platform will serve is answerable before it is created.

The interface exists to be **read as evidence**. It is not a marketing surface and it
is not a dashboard for its own sake.

## Principles

- **Measured over claimed.** A number is shown with its age and its source. A stale
  reading is marked stale rather than displayed as current.
- **Nothing hidden.** No self-imposed quota on a via-node source that the source
  itself does not impose; a rate limit that applies to one node does not pause the
  other 300; a failure says what failed. **[assumed]** the brief's repeated objection
  to silently-applied limits is a product principle, not one incident's fix.
- **Bounded and quantified.** Resource pressure is answered by a stated policy —
  degrade or refuse — not by unbounded fan-out. Security checks are never skipped to
  buy throughput.
- **Documentation matches behaviour.** When a doc and the code disagree, one of them
  is a defect and it is fixed in the same change.
- **Local and auditable.** Runs on one machine, from a clone, with no external
  service required to boot. External lookups never enter the forwarding path.
- **No bypass to make a test green.** A flaky check is diagnosed and reported, not
  skipped, retried blindly, or whitelisted.

## Accessibility and inclusion

- **Numbers are the content, so they are the first accessibility problem.** Body text
  clears 4.5:1 and graphical objects clear 3:1 in *both* themes; a chart series is
  separable in greyscale, not only in hue.
- **Colour never carries meaning alone.** Every state also carries a word, a shape or
  a position.
- **A wall display is left on all day.** Dark is the default for that reason, motion
  is minimal, and `prefers-reduced-motion` is honoured — including the globe, which
  stops rotating rather than animating more slowly.
- **Touch is a first-class input.** An action hidden behind `opacity: 0` until hover
  does not exist on a tablet, so panel actions are visible, not revealed.
- **The console is resized, not the user.** Layout adapts to the viewport; dense
  tables scroll horizontally rather than compressing their columns into unreadability.

## Open questions

These were not answerable from the repository. They are listed rather than guessed
because they change design decisions:

1. **Is the console ever operated on a phone in an emergency?** Assumed no — the
   brief only required desktop fluidity down to a small laptop.
2. **Is a wall-mounted display a real deployment?** Assumed yes (dark default, no
   load choreography, no decorative motion). Confirm, since it drives contrast floors.
3. **Is English the primary console language, or Chinese?** Both are shipped
   **[assumed]** with English as the code and documentation language.
