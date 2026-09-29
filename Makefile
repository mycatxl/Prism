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

.PHONY: build web backend test test-race test-web capacity lint protocol-matrix verify test-ui init start clean

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
capacity:
	# In-process capacity figures for the platform view, the routing table and the
	# node pool. Honours testing.Short(), so `go test -short` skips these cases and
	# the normal verify target stays fast. Recorded results: docs/PERFORMANCE.md.
	$(GO) test -tags '$(BUILD_TAGS)' -run 'Capacity' -count=1 -v ./internal/platform/... ./internal/routing/... ./internal/topology/...

verify: lint test test-race protocol-matrix test-web

# Frontend checks that need no browser: the panel's server modules, the
# intel-scope mapping, and the design system's contrast gate (which reads the
# tokens in src/styles/design.css and fails on any pair below WCAG AA). Part of
# `verify`, so a regression in the panel or in the palette fails CI like any Go
# test would.
test-web:
	$(NPM) --prefix $(WEB_DIR) run test:config
	$(NPM) --prefix $(WEB_DIR) run check:types
	$(NPM) --prefix $(WEB_DIR) run check:contrast

# The browser-driven checks. They need Playwright's browser binaries, which no
# workflow installs (CI has no browser step), so this is a local/on-demand target
# rather than part of `verify`. Install them once with:
#   npx --prefix internal/api/web playwright install chromium
test-ui:
	$(NPM) --prefix $(WEB_DIR) run test:e2e

init:
	./bin/prism init

start:
	./bin/prism

clean:
	rm -rf bin
