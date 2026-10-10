#!/usr/bin/env bash
# Proves the F7 tail test is sensitive to the missing rebuild in startIntel:
# temporarily remove the RebuildAllPlatforms call, expect the test to FAIL,
# then restore the file byte-for-byte.
set -uo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
TAGS="with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy"
FILE=cmd/prism/intel_runtime.go
cp "$FILE" /tmp/intel_runtime.go.bak
ORIG_MD5=$(md5sum "$FILE" | cut -d' ' -f1)

# Remove the two lines that perform the rebuild.
python3 - <<'PY'
import re
p = "cmd/prism/intel_runtime.go"
s = open(p).read()
needle = """	if a.topoRuntime != nil && a.topoRuntime.pool != nil {
		a.topoRuntime.pool.RebuildAllPlatforms()
	}
"""
assert needle in s, "rebuild block not found"
s = s.replace(needle, "", 1)
open(p, "w").write(s)
PY

echo "=== mutation applied (rebuild removed); expect FAIL ==="
go test -tags "$TAGS" -run 'TestStartIntelRebuildsPlatformsOnceTheProjectionIsLoaded' -count=1 -v ./cmd/prism/ 2>&1 \
  | grep -E '^(=== RUN|--- |ok|FAIL|PASS)|view size' | head -20

cp /tmp/intel_runtime.go.bak "$FILE"
NEW_MD5=$(md5sum "$FILE" | cut -d' ' -f1)
if [ "$ORIG_MD5" != "$NEW_MD5" ]; then
  echo "RESTORE FAILED: md5 $ORIG_MD5 -> $NEW_MD5"
  exit 2
fi
echo "RESTORE OK (md5 $NEW_MD5)"

echo "=== restored; expect PASS ==="
go test -tags "$TAGS" -run 'TestStartIntelRebuildsPlatformsOnceTheProjectionIsLoaded' -count=1 -v ./cmd/prism/ 2>&1 \
  | grep -E '^(=== RUN|--- |ok|FAIL|PASS)|view size' | head -20
