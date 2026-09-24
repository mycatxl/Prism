GO ?= go
NPM ?= npm
WEB_DIR := internal/api/web
TAGS_BASE := with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy
# The mihomo fallback kernel was evaluated and rejected; see
# docs/ENGINE_DECISIONS.md. The with_mihomo build tag and its code seam remain
# in the tree so the decision can be revisited, but the tag is deliberately not
# part of any default build. sing-box is the only engine.
TAGS_FULL := $(TAGS_BASE)
BUILD_TAGS ?= $(TAGS_FULL)
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

.PHONY: build web backend backend-lite test test-race lint protocol-matrix verify test-ui init start clean

build: web backend

web:
	$(NPM) --prefix $(WEB_DIR) ci
	$(NPM) --prefix $(WEB_DIR) run build

backend:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -tags '$(BUILD_TAGS)' -ldflags '$(LDFLAGS)' -o bin/prism ./cmd/prism
	# Companion tool: collects nodes from public sources and either serves them as
	# a remote subscription or pushes them in as a local one. Built here so CI
	# catches breakage in both tag sets.
	CGO_ENABLED=0 $(GO) build -trimpath -tags '$(BUILD_TAGS)' -ldflags '$(LDFLAGS)' -o bin/public-source-sync ./cmd/public-source-sync

backend-lite:
	$(MAKE) backend BUILD_TAGS='$(TAGS_BASE)'

test:
	$(GO) test -tags '$(BUILD_TAGS)' ./cmd/... ./internal/...

test-race:
	$(GO) test -race -tags '$(BUILD_TAGS)' ./cmd/... ./internal/...

protocol-matrix:
	$(GO) test -tags '$(BUILD_TAGS)' -run 'TestProtocolMatrix' -count=1 -v ./internal/outbound/...

lint:
	$(GO) vet -tags '$(BUILD_TAGS)' ./cmd/... ./internal/...
	$(NPM) --prefix $(WEB_DIR) run lint

verify: lint test test-race protocol-matrix
	$(GO) vet -tags '$(TAGS_BASE)' ./cmd/... ./internal/...
	$(GO) test -tags '$(TAGS_BASE)' ./internal/...

test-ui:
	$(NPM) --prefix $(WEB_DIR) run test:config
	$(NPM) --prefix $(WEB_DIR) run test:e2e

init:
	./bin/prism init

start:
	./bin/prism

clean:
	rm -rf bin
