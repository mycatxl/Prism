# public-source-sync

Standalone collector that gathers public proxy nodes and feeds them into an
already deployed Prism instance, either through the Prism Admin API (push) or as
a remote subscription Prism pulls from this process (serve).

Three ways to feed it, freely combinable:

- **Built-in presets** — tested lists of well-known public sources:
  `classic` / `http` / `socks` / `nodes` / `all` / `none`.
- **Custom URLs** — `PUBLIC_SOURCE_URLS`, any text, URI-line, base64 or
  Clash / sing-box subscription.
- **GitHub Gist hunting** — search public gists for proxy protocol links sorted
  by most recently updated, then extract node URIs from the hits.

## Two ways to deliver the aggregate

- **Push mode** (default): creates or updates one local subscription over the
  Admin API and triggers a refresh. It only needs `PRISM_ADMIN_TOKEN`; it never
  touches `PRISM_PROXY_TOKEN`.
- **Pull mode** (`PUBLIC_SOURCE_SERVE_ADDR` set): serves the payload as a plain
  HTTP GET on `PUBLIC_SOURCE_SERVE_PATH` (default `/sub`), and Prism fetches it
  as a remote subscription. No admin token is needed at all, and Prism decides
  the cadence through the subscription's own `update_interval`.

## Quick start

```bash
cp cmd/public-source-sync/public-source-sync.env.example .env  # fill in the URL and token
go build -o prism-public-source-sync ./cmd/public-source-sync
./prism-public-source-sync
```

`public-source-sync.env.example` documents every variable and already ships a
working `all` + gist configuration, so filling in two values is enough.

The upstream Resin spelling of the two variables that name the target instance
(`PUBLIC_SOURCE_RESIN_URL`, `PUBLIC_SOURCE_RESIN_ADMIN_TOKEN`) is still accepted,
so a `.env` written for Resin keeps working after the rename.

## Live checks

Two test files are opt-in because they need the public internet:

```bash
PUBLIC_SOURCE_LIVE_PRESET_TEST=1 go test ./internal/publicsource/ -run PresetSources -v
PUBLIC_SOURCE_LIVE_GIST_TEST=1   go test ./internal/publicsource/ -run LiveGist -v
```