// Package docsguard holds the repository gate that keeps the documentation tree
// honest about the code it describes.
//
// An audit of the documentation tree found four classes of drift that no
// existing check could see:
//
//   - the declared Go baseline was restated as 1.26 in three files while go.mod
//     said 1.27 (DOC-01);
//   - the panel port was documented as 8080 while server/config.mjs defaulted to
//     1262 (DOC-02);
//   - two shipped features (intel.db, `prism restore`) were described as
//     "not implemented yet" (DOC-03);
//   - the directory listing in docs/DESIGN.md §4.3 named seven paths that do not
//     exist and omitted nine that do (DOC-04).
//
// Each one is cheap to assert mechanically, so it is asserted here instead of
// waiting for the next manual audit. The checks are deliberately scoped: a
// document may legitimately describe what this version does not have, so the
// "unavailable" check only fires when the sentence names a subcommand or a
// configuration key that actually exists.
//
// This package contains no production code. The checks run as part of
// `make test`, which `make verify` and CI both execute, so a drift turns the
// build red rather than a reader's afternoon.
package docsguard
