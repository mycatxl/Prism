GO ?= go
NPM ?= npm
WEB_DIR := internal/api/web
# One tag set for every build path (this file, the root Dockerfile and
# .github/workflows/release.yml). The mihomo fallback kernel was evaluated and
# rejected; see docs/ENGINE_DECISIONS.md. The with_mihomo build tag and its code
# seam remain in the tree so the decision can be revisited, but the tag is
# deliberately not part of any build. sing-box is the only engine.
TAGS := with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy
BUILD_TAGS ?= $(TAGS)
VERSION ?= dev
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

empty :=
space := $(empty) $(empty)
comma := ,

LDFLAGS := -s -w \
  -X prism/internal/buildinfo.Version=$(VERSION) \
  -X prism/internal/buildinfo.GitCommit=$(GIT_COMMIT) \
  -X prism/internal/buildinfo.BuildTime=$(BUILD_TIME) \
  -X prism/internal/buildinfo.Tags=$(subst $(space),$(comma),$(BUILD_TAGS))

.PHONY: build web backend test test-race test-web test-slop test-ui capacity lint lint-go protocol-matrix verify smoke init start clean

build: web backend

web:
	$(NPM) --prefix $(WEB_DIR) ci
	$(NPM) --prefix $(WEB_DIR) run build

backend:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -tags '$(BUILD_TAGS)' -ldflags '$(LDFLAGS)' -o bin/prism ./cmd/prism
	# Companion tool: collects nodes from public sources and either serves them as
	# a remote subscription or pushes them in as a local one. Built here so CI
	# catches breakage in it too.
	CGO_ENABLED=0 $(GO) build -trimpath -tags '$(BUILD_TAGS)' -ldflags '$(LDFLAGS)' -o bin/public-source-sync ./cmd/public-source-sync

test:
	$(GO) test -tags '$(BUILD_TAGS)' ./cmd/... ./internal/...

test-race:
	$(GO) test -race -tags '$(BUILD_TAGS)' ./cmd/... ./internal/...

protocol-matrix:
	$(GO) test -tags '$(BUILD_TAGS)' -run 'TestProtocolMatrix' -count=1 -v ./internal/outbound/...

lint:
	$(GO) vet -tags '$(BUILD_TAGS)' ./cmd/... ./internal/...
	$(NPM) --prefix $(WEB_DIR) run lint

# The stricter Go linter set (.golangci.yml). It is a separate target rather
# than part of `lint` because golangci-lint is not in the base toolchain: CI
# installs it, a workstation has to. It fails closed with an install hint
# instead of skipping, so a green run always means the check ran.
lint-go:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo 'golangci-lint is not installed. Install the pinned version:'; \
		echo '  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0'; \
		echo 'then make sure $$(go env GOPATH)/bin is on PATH.'; \
		exit 1; \
	}
	golangci-lint run --build-tags '$(BUILD_TAGS)' --timeout 10m ./cmd/... ./internal/...

capacity:
	# In-process capacity figures for the platform view, the routing table and the
	# node pool. Honours testing.Short(), so `go test -short` skips these cases and
	# the normal verify target stays fast. Recorded results: docs/PERFORMANCE.md.
	$(GO) test -tags '$(BUILD_TAGS)' -run 'Capacity' -count=1 -v ./internal/platform/... ./internal/routing/... ./internal/topology/...

verify: lint test test-race protocol-matrix test-web

# End-to-end smoke test: boots the built binary against a throw-away
# environment (temporary state/cache/log directories, freshly generated tokens,
# a loopback egress probe) and exercises health, the UI, API auth, the
# management listener, HTTP and SOCKS5 forwarding, the reverse-proxy path,
# online backup, check-config and restart persistence. 23 checks, about two
# seconds, and it never reaches the network. It needs bin/prism, so it runs
# after `make backend`; CI runs it as its own step rather than folding it into
# `verify`, which stays free of build artifacts.
smoke:
	bash scripts/smoke.sh

# Frontend checks that need no browser: the panel's server modules, the kit's own
# invariants, the design system's contrast gate (which reads the tokens in
# src/styles/design.css and fails on any pair below WCAG AA), and the
# component-kit gate (which reads the .tsx sources directly, because the
# anti-pattern detector does not scan them — DESIGN.md:235-236). Part of
# `verify`, so a regression in the panel, in the kit or in the palette fails CI
# like any Go test would.
test-web:
	$(NPM) --prefix $(WEB_DIR) run test:config
	$(NPM) --prefix $(WEB_DIR) run check:types
	$(NPM) --prefix $(WEB_DIR) run check:contrast
	$(NPM) --prefix $(WEB_DIR) run check:kit
	$(NPM) --prefix $(WEB_DIR) run check:responsive

# The browser-driven checks. They need Playwright's browser binaries, which no
# workflow installs (CI has no browser step), so this is a local/on-demand target
# rather than part of `verify`. Install them once with:
#   npx --prefix internal/api/web playwright install chromium
test-ui:
	$(NPM) --prefix $(WEB_DIR) run test:e2e

# The anti-pattern gate: impeccable's deterministic detector (61 checks for the
# defaults an agent reaches for before a design exists) over the console. It needs
# the external engine, and it scans a built tree when one is present, so it is a
# local/on-demand target rather than part of `verify`. Install once with:
#   npx impeccable install
test-slop:
	$(NPM) --prefix $(WEB_DIR) run check:slop

init:
	./bin/prism init

start:
	./bin/prism

clean:
	rm -rf bin
