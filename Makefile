GO ?= go
NPM ?= npm
BUILD_TAGS ?= with_quic with_wireguard with_grpc with_utls with_gvisor http2legacy
VERSION ?= dev
GIT_COMMIT ?= unknown
BUILD_TIME ?= unknown
LDFLAGS = -X prism/internal/buildinfo.Version=$(VERSION) -X prism/internal/buildinfo.GitCommit=$(GIT_COMMIT) -X prism/internal/buildinfo.BuildTime=$(BUILD_TIME)

.PHONY: build web backend test test-race lint test-ui init start

build:
	$(MAKE) web
	$(MAKE) backend

web:
	$(NPM) --prefix web run build

backend:
	mkdir -p bin
	$(GO) build -buildvcs=false -tags '$(BUILD_TAGS)' -ldflags '$(LDFLAGS)' -o bin/prism ./cmd/prism

test:
	$(GO) test -tags '$(BUILD_TAGS)' ./cmd/... ./internal/...

test-race:
	$(GO) test -race -tags '$(BUILD_TAGS)' ./cmd/... ./internal/...

lint:
	$(NPM) --prefix web run lint
	$(GO) vet -tags '$(BUILD_TAGS)' ./cmd/... ./internal/...

test-ui:
	$(NPM) --prefix web run test:config
	$(NPM) --prefix web run test:e2e

init:
	./bin/prism init

start:
	./bin/prism standalone
