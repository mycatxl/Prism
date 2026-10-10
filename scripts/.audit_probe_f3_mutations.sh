#!/usr/bin/env bash
# Evidence that the F3 / #27 regression tests are defect-sensitive: each mutation
# below restores the defect (or the "looks fixed, still broken" variant) in a
# production file, runs the regression test, and restores the file afterwards.
# Every case must FAIL before the restore; a PASS means the test does not cover
# the defect.
set -uo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
TAGS="with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy"

run_case() {
  local label="$1" src="$2" patch="$3" test_pkg="$4" test_re="$5"
  local backup="/tmp/$(basename "$src").mutation.bak"
  cp "$src" "$backup"
  python3 - "$src" "$patch" <<'PY'
import sys
path, mode = sys.argv[1], sys.argv[2]
src = open(path).read()
if mode == "f3-strip-halfclose":
    import re
    for name in ("CloseWrite", "CloseRead"):
        src = re.sub(r"\nfunc \(c \*countingReadWriteCloser\) %s\(\) error \{(?:.|\n)*?\n\}\n" % name, "\n", src, count=1)
    for name in ("closeWriteIfSupported", "closeReadIfSupported"):
        src = re.sub(r"\nfunc %s\(rwc io\.ReadWriteCloser\) error \{(?:.|\n)*?\n\}\n" % name, "\n", src, count=1)
elif mode == "f3-unsupported-error":
    old = """func closeWriteIfSupported(rwc io.ReadWriteCloser) error {
	closeWriter, ok := rwc.(interface{ CloseWrite() error })
	if !ok {
		return nil
	}"""
    new = """func closeWriteIfSupported(rwc io.ReadWriteCloser) error {
	closeWriter, ok := rwc.(interface{ CloseWrite() error })
	if !ok {
		return errHalfCloseUnsupported
	}"""
    assert old in src, "anchor not found"
    src = src.replace(old, new, 1)
elif mode == "f27-strip-halfclose":
    import re
    src, n = re.subn(r"\nfunc \(o \*socks5AuthObserver\) CloseWrite\(\) error \{(?:.|\n)*?\n\}\n", "\n", src, count=1)
    assert n == 1, "CloseWrite not found"
elif mode == "f27-no-delegation":
    old = """	return closeWriteIfSupported(o.Conn)
}"""
    new = """	return errHalfCloseUnsupported
}"""
    assert old in src, "anchor not found"
    src = src.replace(old, new, 1)
else:
    raise SystemExit("unknown mode " + mode)
open(path, "w").write(src)
PY

  echo "--- $label"
  go test -tags "$TAGS" -run "$test_re" -count=1 "$test_pkg" 2>&1 \
    | grep -E '^(--- |    --- |ok|FAIL|PASS|.*undefined)' | head -12
  cp "$backup" "$src"
  echo "    [restored $src]"
}

run_case "F3 mutation 1: wrapper has no CloseWrite/CloseRead (pre-fix method set)" \
  internal/proxy/request_log_capture.go f3-strip-halfclose ./internal/proxy/ \
  'TestAudit_UpgradeStreamWrapperKeepsHalfClose$'
run_case "F3 mutation 2: CloseWrite reports errHalfCloseUnsupported for unsupported backends" \
  internal/proxy/request_log_capture.go f3-unsupported-error ./internal/proxy/ \
  'TestAudit_UpgradeStreamWrapperHalfCloseSemantics$'
run_case "#27 mutation 1: socks5AuthObserver has no CloseWrite (pre-fix method set)" \
  cmd/prism/proxy_auth_guard.go f27-strip-halfclose ./cmd/prism/ \
  'TestSocks5AuthObserverKeepsHalfClose$'
run_case "#27 mutation 2: CloseWrite never delegates" \
  cmd/prism/proxy_auth_guard.go f27-no-delegation ./cmd/prism/ \
  'TestSocks5AuthObserverKeepsHalfClose$'
