# WP01 · 仓库与构建修复

**前置**：无。**目标**：修正 `.gitignore`、`go.mod`、前端嵌入方式、Makefile、CI 和 Docker，让仓库具备可构建的基础。完整构建要等 WP02、WP03 补齐 `internal/state` 和 `cmd/prism`。

## 1. `.gitignore` 全量替换

用下面的内容**完整替换**根目录 `.gitignore`（所有路径都锚定到仓库根，不再误伤 `cmd/prism/` 和 `internal/state/`，也不再忽略测试）：

```gitignore
# Build output
/bin/
/prism
/prism.exe
*.test
*.out
coverage.*
*.coverprofile

# Local runtime data (repo root only)
/.local/
/state/
/data/
/backups/
*.db
*.db-shm
*.db-wal
*.log

# Secrets
.env
.env.local
.admin_token

# Frontend
node_modules/
/internal/api/web/dist/*
!/internal/api/web/dist/.gitkeep

# Private notes
/docs/design/
/references/

# Editors / OS
.vscode/
.idea/
*.swp
*~
.DS_Store
Thumbs.db

# Go workspace
go.work
go.work.sum
```

同时把 `internal/api/web/.gitignore` 中的 `dist` 一行改为 `dist/*` 和 `!dist/.gitkeep` 两行。

**恢复本地文件**：如果原开发机上还存在 Prism 版本的 `cmd/prism` 或 `internal/state`（例如 `/opt/Prism`），先在那台机器上执行：

```sh
git status --ignored --short | grep -E 'cmd/prism|internal/state|_test\.go'
git add -f cmd/prism internal/state $(git ls-files --others --ignored --exclude-standard | grep '_test\.go$')
```

然后把这些文件带入本分支。WP02 和 WP03 会说明如何与本方案对齐。如果找不到这些文件，就按 WP02、WP03 从上游重建。

## 2. `go.mod` 修复（已验证，事实 F2）

```sh
go mod edit -require=github.com/sagernet/gvisor@v0.0.0-20260727.0-sing-box-mod.1
go mod edit -dropreplace=github.com/sagernet/wireguard-go
go mod edit -require=github.com/sagernet/wireguard-go@v0.0.5-0.20260823125007-8bd032a91a30
git rm -r third_party/wireguard-go
```

- 在 `go.mod` 的 gvisor 那一行上方加注释：`// pinned: sagernet pseudo-version "20260727.0-sing-box-mod.1" sorts lower than older hash-style pseudo-versions under semver; keep explicit`。
- 更新 `THIRD_PARTY_NOTICES.md`：删除 WireGuard 本地补丁一节；sing-box 版本改为 `v1.14.0`；新增 mihomo `v1.19.31`（GPL-3.0，WP07 引入）。
- 更新 `docs/UPSTREAM_BASELINE.md`：写明 sing-box 版本为 1.14.0、Go 版本为 1.26.0，列出构建标签清单，删除 wireguard 替换相关的说明。
- 等 WP02 恢复 `internal/state` 之后再执行 `go mod tidy`。在此之前不要执行，否则会因为缺包而删错依赖。

## 3. 前端嵌入（让 `go build` 和 `go test` 在未构建前端时也能通过）

1. 新建空文件 `internal/api/web/dist/.gitkeep` 并提交。
2. 删除孤立的 `internal/api/web/embed.go`（包 `webui`，没有被任何代码引用）。
3. 删除没有任何地方提供服务的旧界面：`internal/api/web/static/` 和 `internal/api/web/templates/`。
4. 在 `internal/api/webui.go` 中把嵌入指令改为 `//go:embed all:web/dist`，并把 handler 替换为上游 Resin 的实现，逻辑如下：
   - 只接受 GET 和 HEAD；
   - 对路径做 `path.Clean`；
   - 文件存在就用 `http.ServeFileFS` 返回；
   - 带扩展名但找不到的路径返回 404；
   - 其余路径回退到 `index.html`（SPA）；
   - 如果 `index.html` 不存在，返回 **503**，响应体为 `WebUI not built. Run: make web`；
   - `/` 重定向到 `/ui/`，`/ui` 重定向到 `/ui/`。
5. 验收：`rm -rf internal/api/web/dist/*`（保留 `.gitkeep`）之后，`go build ./internal/api` 能通过。

## 4. Makefile 全量替换

```make
GO ?= go
NPM ?= npm
WEB_DIR := internal/api/web
TAGS_BASE := with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy
TAGS_FULL := $(TAGS_BASE) with_mihomo
BUILD_TAGS ?= $(TAGS_FULL)
VERSION ?= dev
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
  -X prism/internal/buildinfo.Version=$(VERSION) \
  -X prism/internal/buildinfo.GitCommit=$(GIT_COMMIT) \
  -X prism/internal/buildinfo.BuildTime=$(BUILD_TIME)

.PHONY: build web backend backend-lite test test-race lint protocol-matrix verify clean

build: web backend

web:
	$(NPM) --prefix $(WEB_DIR) ci
	$(NPM) --prefix $(WEB_DIR) run build

backend:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -tags '$(BUILD_TAGS)' -ldflags '$(LDFLAGS)' -o bin/prism ./cmd/prism

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

clean:
	rm -rf bin
```

说明：

- 构建标签 `with_mihomo` 由 Prism 自己定义（WP07）。在 WP07 完成之前，它不影响任何代码。
- 在 WP06/WP07 建立 `TestProtocolMatrix` 之前，`protocol-matrix` 目标会报 "no tests to run" 而不是失败，这是允许的。
- 删除 `scripts/verify.mjs`，以 Makefile 作为唯一的验证入口。
- `internal/api/web/package.json` 里的 e2e 脚本保持不变。

## 5. CI：`.github/workflows/ci.yml`

在 push 和 pull_request 时触发。Job 使用 `ubuntu-latest`：

1. `actions/checkout@v4`；
2. `actions/setup-node@v4`，node-version 22，并缓存 `internal/api/web/package-lock.json`；
3. `actions/setup-go@v5`，`go-version-file: go.mod`；
4. `make web`；
5. `make verify`；
6. `make backend` 和 `make backend-lite`（两个变体都必须能编译）。

## 6. Docker 与发布

- 以上游 `Dockerfile`、`docker/entrypoint.sh`、`docker-compose.yml.example` 和 `.github/workflows/release.yml` 为蓝本移植，替换规则如下：
  - `resin` 改为 `prism`；
  - `RESIN_` 改为 `PRISM_`；
  - 目录改为 `/var/lib/prism`、`/var/cache/prism`、`/var/log/prism`；
  - 暴露端口 2260；
  - 构建标签使用 `$(TAGS_FULL)`；
  - Go 版本使用 `go.mod` 中的版本。
- `release.yml` 的矩阵：linux amd64/arm64、darwin amd64/arm64、windows amd64。每个平台构建完整版，linux 额外构建 `-lite` 变体。**不要**加 `with_embedded_tor` 和 `with_naive_outbound`（事实 F10）。
- `.dockerignore` 至少包含：`.git`、`node_modules`、`bin`、`.local`、`internal/api/web/dist`。

## 7. 验收

```sh
git check-ignore cmd/prism/main.go internal/state/engine.go internal/node/hash_test.go; echo "exit=$?"  # 必须输出 exit=1（均未被忽略）
make web
go build -tags "with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy" \
  ./internal/outbound ./internal/subscription ./internal/proxy ./internal/probe ./internal/topology ./internal/routing ./internal/api/...
# internal/api 仍依赖 state，等 WP02 完成后才能通过；此处允许只报 "prism/internal/state" 缺失这一类错误
```

## 8. 不要做

- 不要重新引入 `third_party/wireguard-go`，也不要加任何 `replace`。
- 不要把 `with_embedded_tor`、`with_naive_outbound`、`with_tailscale` 加入默认标签。
