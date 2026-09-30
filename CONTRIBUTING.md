# Contributing to Prism

Thanks for looking. This document covers how to build the project, what a
change is expected to carry, and the checks that have to pass before it lands.

## Before you start

- **Bugs and behaviour changes**: open an issue first. Prism is a proxy router
  with a security-relevant surface (SSRF policy, token handling, the reverse
  proxy path), so a short description of the failure and how you reproduced it
  is worth more than a patch that has to be reverse-engineered.
- **Security problems**: do not open a public issue. Follow [SECURITY.md](SECURITY.md).
- **Large refactors**: describe the intent before writing the code. The project
  deliberately keeps a small dependency set and a documented rationale for the
  engine choice, so a change that adds a dependency or a second engine needs the
  discussion to happen first.

## Building

Requirements are in the [README](README.md#requirements): Go 1.27 or later, and
Node.js/npm for the web UI.

```bash
make build          # web UI + backend -> bin/prism
make backend        # backend only, also -> bin/prism
```

## What has to pass

Run these before opening a pull request. They are the same steps CI runs.

```bash
make verify         # go vet, unit tests, -race, protocol matrix, frontend gates
make smoke          # end-to-end against the built binary (needs bin/prism)
```

`make verify` covers:

| Target | What it checks |
|---|---|
| `lint` | `go vet` over `./cmd/...` and `./internal/...`, plus ESLint for the panel |
| `test` | Unit tests with the release build tags |
| `test-race` | The same suite under the race detector |
| `protocol-matrix` | `TestProtocolMatrix`: every protocol's import/build outcome |
| `test-web` | Panel config tests, TypeScript, WCAG AA contrast, and the component-kit rules |

`make smoke` boots the compiled binary against a throw-away environment and
exercises health, the UI, API auth, the management listener, HTTP and SOCKS5
forwarding, the reverse-proxy path, online backup, `check-config` and restart
persistence. It stays on the loopback interface and takes about two seconds.

Two further targets are local-only, because they need tooling that CI does not
install:

```bash
make test-ui        # browser regression; needs: npx --prefix internal/api/web playwright install chromium
make test-slop      # anti-pattern detector; needs: npx impeccable install
```

## Changing the documentation

`docs/` is the authority for behaviour, and it is gated: `internal/docsguard`
fails the test suite when the documentation and the code disagree about
something mechanical — a subcommand or database described as missing while it
exists, a directory listing that names directories that are not there, a version
string that drifted from `go.mod`.

If a change alters documented behaviour, update the document in the same commit.
When you reference code from prose, **name the file and the symbol, not a line
number**: line numbers move, and a stale one is worse than none.

## Code conventions

- Go is `gofmt`-formatted with tabs. The build tag set is defined once in the
  `Makefile` (`TAGS`) and must stay in sync with the `Dockerfile`,
  `.github/workflows/release.yml` and `.github/Dockerfile.release`.
- The web panel has its own conventions in
  [`internal/api/web/DESIGN.md`](internal/api/web/DESIGN.md): pages compose the
  components in `src/components/ui/`, they do not hand-roll controls, and the
  component-kit gate (`check-kit.mjs`) enforces that mechanically.
- Comments explain *why*, not *what*. Where a decision was made against an
  obvious alternative, record the alternative and the reason — several files in
  `docs/` exist only for that.
- Line endings are LF everywhere; [`.gitattributes`](.gitattributes) is the
  authority.

## Commits and pull requests

- Keep a commit to one idea, and write the message so the reason is legible
  without the diff: what changed, and why that is the right change.
- A pull request that changes behaviour should say how it was verified. "It
  builds" is not verification for a routing, persistence or security change.
- New behaviour needs a test that fails without it. If a check is meant to catch
  a class of mistake, prove it can catch one before trusting it.

## License

Prism is released under GPL-3.0-or-later; see [LICENSE](LICENSE). By
contributing you agree that your contribution is licensed under the same terms.
Derived sources keep their original licences, listed in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
