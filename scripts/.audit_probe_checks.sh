#!/usr/bin/env bash
set -uo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
TAGS="with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy"
echo "=== audit probes (checks engine) ==="
go test -tags "$TAGS" -run 'TestAudit_' -count=1 -v ./internal/intel/checks/ 2>&1 \
  | grep -E '^(=== RUN|--- |ok|FAIL|PASS|# )|audit_probe|\.go:[0-9]+:' | head -40
