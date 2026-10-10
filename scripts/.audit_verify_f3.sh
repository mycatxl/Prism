#!/usr/bin/env bash
set -uo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
TAGS="with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy"
echo "=== 1) audit probe (F3 + #27) ==="
bash scripts/.audit_probe_proxy.sh
echo
echo "=== 2) go test ./internal/proxy/... ./cmd/prism/... ==="
go test -tags "$TAGS" -count=1 ./internal/proxy/... ./cmd/prism/...
echo
echo "=== 3) go test -race ./internal/proxy/... ./cmd/prism/... ==="
go test -tags "$TAGS" -race -count=1 ./internal/proxy/... ./cmd/prism/...
echo
echo "=== 4) flake check: -race -count=5 on the touched packages' half-close/upgrade tests ==="
go test -tags "$TAGS" -race -count=5 -run 'TestAudit_|TestReverseProxy_E2EWebSocketUpgrade|TestPumpPreparedTunnelReader|TestSocks5' ./internal/proxy/ ./cmd/prism/ 2>&1 | tail -10
echo
echo "=== 5) go vet ==="
go vet -tags "$TAGS" ./internal/proxy/ ./cmd/prism/ && echo "vet clean"
