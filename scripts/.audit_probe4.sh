#!/usr/bin/env bash
set -uo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
TAGS="with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy"
echo "=== stranded-item probe (F1 x F4 x F5 interaction) ==="
go test -tags "$TAGS" -run 'TestAudit_CanceledJobDoesNotStrandAnInFlightItem' -count=1 -v ./internal/intel/jobs/ 2>&1 | head -25
