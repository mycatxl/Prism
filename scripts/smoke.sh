#!/usr/bin/env bash
#
# Prism smoke test (WP03 §7.3, reused by WP13).
#
# Boots bin/prism against a throw-away environment (temporary state/cache/log
# directories plus freshly generated tokens) and exercises the public surface:
# health, UI, API auth, environment-defined management listener, HTTP and SOCKS5
# forwarding through a local HTTP proxy node, the reverse proxy path and
# restart persistence.
#
# Environment:
#   PRISM_BIN     binary under test (default: <repo>/bin/prism)
#   PRISM_SMOKE_KEEP=1   keep the temporary work directory for debugging
#
# Note: the forwarding checks need outbound HTTPS access, because a freshly
# added node must first pass its egress probe
# (https://cloudflare.com/cdn-cgi/trace, see internal/probe/manager.go) through
# the local test proxy before it becomes routable. Without internet access the
# node stays circuit-open and those two checks fail.
# Exit code: 0 when every check passed (or was skipped), 1 otherwise.

set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${PRISM_BIN:-$ROOT_DIR/bin/prism}"

PASS=0
FAIL=0
SKIP=0

log() { printf '[smoke] %s\n' "$*"; }

pass() {
    PASS=$((PASS + 1))
    printf '[smoke] PASS  %s\n' "$1"
}

fail() {
    FAIL=$((FAIL + 1))
    printf '[smoke] FAIL  %s\n' "$1"
}

skip() {
    SKIP=$((SKIP + 1))
    printf '[smoke] SKIP  %s\n' "$1"
}

check_status() {
    # check_status <description> <expected-status> <actual-status>
    local description="$1" expected="$2" actual="$3"
    if [ "$expected" = "$actual" ]; then
        pass "$description (HTTP $actual)"
    else
        fail "$description (expected HTTP $expected, got $actual)"
    fi
}

require_tools() {
    local missing=0
    for tool in curl python3; do
        if ! command -v "$tool" >/dev/null 2>&1; then
            log "missing required tool: $tool"
            missing=1
        fi
    done
    if [ "$missing" -ne 0 ]; then
        exit 1
    fi
}

free_port() {
    python3 - <<'PY'
import socket
sock = socket.socket()
sock.bind(("127.0.0.1", 0))
print(sock.getsockname()[1])
sock.close()
PY
}

random_token() {
    python3 - <<'PY'
import secrets
print(secrets.token_hex(32))
PY
}

http_code() {
    # http_code <curl-args...>  -> prints the status code, body goes to $BODY_FILE
    local code
    code="$(curl -s -o "$BODY_FILE" -w '%{http_code}' --max-time 15 "$@" 2>/dev/null)"
    if [ -z "$code" ]; then
        code="000"
    fi
    printf '%s' "$code"
}

wait_for_healthz() {
    local base="$1" attempts=0
    while [ "$attempts" -lt 60 ]; do
        if [ "$(http_code "$base/healthz")" = "200" ]; then
            return 0
        fi
        attempts=$((attempts + 1))
        sleep 0.5
    done
    return 1
}

retry_request() {
    # retry_request <curl-args...> : repeat until HTTP 200 or the budget is used.
    local attempts=0
    while [ "$attempts" -lt 40 ]; do
        if [ "$(http_code "$@")" = "200" ]; then
            return 0
        fi
        attempts=$((attempts + 1))
        sleep 0.5
    done
    return 1
}

WORK_DIR="$(mktemp -d)"
PRISM_PID=""
TARGET_PID=""
PROXY_PID=""
BODY_FILE="$WORK_DIR/body.txt"
PRISM_LOG="$WORK_DIR/prism.log"
ADMIN_TOKEN=""
PROXY_TOKEN=""

cleanup() {
    local status=$?
    for pid in "$PRISM_PID" "$TARGET_PID" "$PROXY_PID"; do
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null
            wait "$pid" 2>/dev/null
        fi
    done
    if [ "$FAIL" -gt 0 ] && [ -f "$PRISM_LOG" ]; then
        printf '\n[smoke] --- last Prism log lines (tokens redacted) ---\n'
        sed -e "s/${ADMIN_TOKEN:-__none__}/***REDACTED***/g" \
            -e "s/${PROXY_TOKEN:-__none__}/***REDACTED***/g" "$PRISM_LOG" | tail -n 40
        printf '[smoke] --- end of log ---\n\n'
    fi
    if [ "${PRISM_SMOKE_KEEP:-0}" = "1" ]; then
        log "work directory kept: $WORK_DIR"
    else
        rm -rf "$WORK_DIR"
    fi
    log "results: $PASS passed, $FAIL failed, $SKIP skipped"
    if [ "$FAIL" -gt 0 ]; then
        exit 1
    fi
    exit "$status"
}
trap cleanup EXIT

require_tools

if [ ! -x "$BIN" ]; then
    log "binary not found: $BIN"
    log "build it first: make backend"
    exit 1
fi

ADMIN_TOKEN="$(random_token)"
PROXY_TOKEN="$(random_token)"
MAIN_PORT="$(free_port)"
ADMIN_PORT="$(free_port)"
TARGET_PORT="$(free_port)"
PROXY_PORT="$(free_port)"

MAIN_BASE="http://127.0.0.1:$MAIN_PORT"
ADMIN_BASE="http://127.0.0.1:$ADMIN_PORT"
PROXY_URL="http://Default:$PROXY_TOKEN@127.0.0.1:$MAIN_PORT"
SOCKS_URL="socks5h://Default:$PROXY_TOKEN@127.0.0.1:$MAIN_PORT"

mkdir -p "$WORK_DIR/state" "$WORK_DIR/cache" "$WORK_DIR/logs" "$WORK_DIR/www"
printf 'PRISM_SMOKE_TARGET\n' > "$WORK_DIR/www/index.html"

cat > "$WORK_DIR/.env" <<EOF
PRISM_ADMIN_TOKEN=$ADMIN_TOKEN
PRISM_PROXY_TOKEN=$PROXY_TOKEN
PRISM_LISTEN_ADDRESS=127.0.0.1
PRISM_PORT=$MAIN_PORT
PRISM_ADMIN_LISTEN=127.0.0.1:$ADMIN_PORT
PRISM_STATE_DIR=./state
PRISM_CACHE_DIR=./cache
PRISM_LOG_DIR=./logs
# The reverse-proxy check below dials a loopback target directly. That only
# happens for hosts matched by the operator bypass rules (upstream Resin
# behaviour); SSRF protection for this path is opt-in via
# PRISM_DIRECT_DENY_PRIVATE.
PRISM_PROXY_BYPASS=127.0.0.1
EOF
chmod 0600 "$WORK_DIR/.env"

# --- local test target server -------------------------------------------------
python3 -m http.server "$TARGET_PORT" --bind 127.0.0.1 --directory "$WORK_DIR/www" \
    > "$WORK_DIR/target.log" 2>&1 &
TARGET_PID=$!

# --- local HTTP proxy used as the single subscription node --------------------
cat > "$WORK_DIR/proxy.py" <<'PY'
"""Minimal HTTP proxy used as a Prism node: absolute-form requests + CONNECT."""
import socket
import sys
import threading
from urllib.parse import urlsplit


def pump(src, dst):
    try:
        while True:
            data = src.recv(65536)
            if not data:
                break
            dst.sendall(data)
    except OSError:
        pass
    finally:
        try:
            dst.shutdown(socket.SHUT_WR)
        except OSError:
            pass


def handle(conn):
    try:
        conn.settimeout(30)
        buf = b""
        while b"\r\n\r\n" not in buf:
            chunk = conn.recv(4096)
            if not chunk:
                return
            buf += chunk
        head, _, rest = buf.partition(b"\r\n\r\n")
        lines = head.split(b"\r\n")
        method, target, version = lines[0].split(b" ", 2)
        method = method.upper()

        if method == b"CONNECT":
            host, _, port = target.decode().partition(":")
            upstream = socket.create_connection((host, int(port or 443)), timeout=30)
            conn.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
            threading.Thread(target=pump, args=(conn, upstream), daemon=True).start()
            pump(upstream, conn)
            return

        parts = urlsplit(target.decode())
        if not parts.hostname:
            conn.sendall(b"HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
            return
        path = parts.path or "/"
        if parts.query:
            path += "?" + parts.query
        upstream = socket.create_connection(
            (parts.hostname, parts.port or 80), timeout=30
        )
        forward = [b"%s %s %s" % (method, path.encode(), version)]
        for line in lines[1:]:
            if line.split(b":", 1)[0].strip().lower() in (
                b"connection",
                b"proxy-connection",
                b"keep-alive",
            ):
                continue
            forward.append(line)
        forward.append(b"Connection: close")
        upstream.sendall(b"\r\n".join(forward) + b"\r\n\r\n" + rest)
        pump(upstream, conn)
    except Exception as exc:  # noqa: BLE001 - smoke helper, report and drop
        print(f"proxy error: {exc}", file=sys.stderr)
    finally:
        try:
            conn.close()
        except OSError:
            pass


def main():
    server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind(("127.0.0.1", int(sys.argv[1])))
    server.listen(64)
    print("proxy ready", flush=True)
    while True:
        conn, _ = server.accept()
        threading.Thread(target=handle, args=(conn,), daemon=True).start()


main()
PY
python3 "$WORK_DIR/proxy.py" "$PROXY_PORT" > "$WORK_DIR/proxy.log" 2>&1 &
PROXY_PID=$!

# --- start Prism --------------------------------------------------------------
start_prism() {
    ( cd "$WORK_DIR" && exec "$BIN" ) >> "$PRISM_LOG" 2>&1 &
    PRISM_PID=$!
}

start_prism
if ! wait_for_healthz "$MAIN_BASE"; then
    fail "service did not become healthy on $MAIN_BASE/healthz"
    exit 1
fi
pass "service started (healthz reachable)"

# --- health, UI and API auth --------------------------------------------------
check_status "/healthz without token" 200 "$(http_code "$MAIN_BASE/healthz")"

ui_status="$(http_code "$MAIN_BASE/ui/")"
case "$ui_status" in
    200) pass "/ui/ serves the built frontend" ;;
    503) pass "/ui/ reports 503 because the frontend was not built (make web)" ;;
    *) fail "/ui/ returned HTTP $ui_status (expected 200 or 503)" ;;
esac

check_status "/api/v1/system/info rejects missing token" 401 \
    "$(http_code "$MAIN_BASE/api/v1/system/info")"
check_status "/api/v1/system/info accepts the admin token" 200 \
    "$(http_code -H "Authorization: Bearer $ADMIN_TOKEN" "$MAIN_BASE/api/v1/system/info")"
if grep -q '"build_tags"' "$BODY_FILE"; then
    pass "/api/v1/system/info reports build_tags"
else
    fail "/api/v1/system/info is missing build_tags"
fi

# --- optional independent management listener (PRISM_ADMIN_LISTEN) ------------
check_status "admin listener /healthz" 200 "$(http_code "$ADMIN_BASE/healthz")"
check_status "admin listener /api with token" 200 \
    "$(http_code -H "Authorization: Bearer $ADMIN_TOKEN" "$ADMIN_BASE/api/v1/system/info")"
admin_proxy_status="$(http_code -X CONNECT "$ADMIN_BASE/")"
if [ "$admin_proxy_status" = "404" ] || [ "$admin_proxy_status" = "405" ]; then
    pass "admin listener refuses CONNECT (HTTP $admin_proxy_status)"
else
    fail "admin listener must not accept proxy traffic (CONNECT returned HTTP $admin_proxy_status)"
fi

# --- local subscription with one HTTP proxy node ------------------------------
create_body="$WORK_DIR/create-sub.json"
cat > "$create_body" <<EOF
{"name":"local-smoke","source_type":"local","content":"127.0.0.1:$PROXY_PORT","enabled":true}
EOF
create_status="$(http_code -X POST \
    -H "Authorization: Bearer $ADMIN_TOKEN" \
    -H "Content-Type: application/json" \
    --data-binary "@$create_body" \
    "$MAIN_BASE/api/v1/subscriptions")"
SUB_ID="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("id",""))' "$BODY_FILE" 2>/dev/null)"
if [ "$create_status" = "201" ] || [ "$create_status" = "200" ]; then
    pass "local subscription created (HTTP $create_status)"
else
    fail "subscription creation failed (HTTP $create_status)"
fi

if [ -n "$SUB_ID" ]; then
    check_status "subscription refresh" 200 \
        "$(http_code -X POST -H "Authorization: Bearer $ADMIN_TOKEN" \
            "$MAIN_BASE/api/v1/subscriptions/$SUB_ID/actions/refresh")"
fi

nodes_ready=0
attempts=0
while [ "$attempts" -lt 60 ]; do
    if [ "$(http_code -H "Authorization: Bearer $ADMIN_TOKEN" \
        "$MAIN_BASE/api/v1/nodes?limit=10")" = "200" ] \
        && python3 -c 'import json,sys;sys.exit(0 if json.load(open(sys.argv[1])).get("total",0)>=1 else 1)' "$BODY_FILE" 2>/dev/null; then
        nodes_ready=1
        break
    fi
    attempts=$((attempts + 1))
    sleep 0.5
done
if [ "$nodes_ready" -eq 1 ]; then
    pass "subscription node is registered in the pool"
else
    fail "subscription node never appeared in /api/v1/nodes"
fi

# --- forwarding through the local HTTP proxy node -----------------------------
if retry_request -x "$PROXY_URL" "http://127.0.0.1:$TARGET_PORT/"; then
    if grep -q 'PRISM_SMOKE_TARGET' "$BODY_FILE"; then
        pass "HTTP forward proxy (-x http://) reached the local target"
    else
        fail "HTTP forward proxy returned 200 without the target body"
    fi
else
    fail "HTTP forward proxy (-x http://) did not succeed"
fi

if retry_request --proxy "$SOCKS_URL" "http://127.0.0.1:$TARGET_PORT/"; then
    if grep -q 'PRISM_SMOKE_TARGET' "$BODY_FILE"; then
        pass "SOCKS5 inbound (--proxy socks5h://) reached the local target"
    else
        fail "SOCKS5 inbound returned 200 without the target body"
    fi
else
    fail "SOCKS5 inbound (--proxy socks5h://) did not succeed"
fi

# Reverse proxy to a loopback target. The dot segment is the platform-identity
# placeholder, so the request must be sent with --path-as-is: a client that
# normalises "/./" away loses that segment and the server answers 400
# INVALID_PROTOCOL.
reverse_status="$(http_code --path-as-is "$MAIN_BASE/$PROXY_TOKEN/./http/127.0.0.1:$TARGET_PORT/")"
if [ "$reverse_status" = "200" ] && grep -q 'PRISM_SMOKE_TARGET' "$BODY_FILE"; then
    pass "reverse proxy path /<token>/./http/127.0.0.1:$TARGET_PORT/ reached the local target"
else
    fail "reverse proxy path /<token>/./http/127.0.0.1:$TARGET_PORT/ returned HTTP $reverse_status"
fi

# --- online backup while the service is running --------------------------------
backup_exit=0
( cd "$WORK_DIR" && "$BIN" backup --out ./backup ) > "$WORK_DIR/backup.log" 2>&1 || backup_exit=$?
if [ "$backup_exit" -eq 0 ]; then
    pass "prism backup succeeded against the running service"
else
    fail "prism backup failed with exit code $backup_exit"
fi

if [ ! -e "$WORK_DIR/backup/.env" ]; then
    pass "backup does not contain .env"
else
    fail "backup must not contain .env"
fi

if python3 - "$WORK_DIR/backup" <<'PY'
import json
import pathlib
import sqlite3
import sys

root = pathlib.Path(sys.argv[1])
manifest = json.loads((root / "manifest.json").read_text())
names = {entry["name"] for entry in manifest["files"]}
if not {"state.db", "cache.db"} <= names:
    raise SystemExit(f"manifest is missing databases: {sorted(names)}")
if not names <= {"state.db", "cache.db", "intel.db"}:
    raise SystemExit(f"manifest lists unexpected files: {sorted(names)}")
for entry in manifest["files"]:
    conn = sqlite3.connect(root / entry["name"])
    check = conn.execute("PRAGMA quick_check").fetchone()[0]
    conn.close()
    if check != "ok":
        raise SystemExit(f"quick_check {entry['name']}: {check}")
PY
then
    pass "online backup databases are consistent (quick_check ok)"
else
    fail "online backup databases failed quick_check"
fi
# --- restore refuses to touch a live installation -----------------------------
restore_exit=0
( cd "$WORK_DIR" && "$BIN" restore --from ./backup --force ) > "$WORK_DIR/restore.log" 2>&1 || restore_exit=$?
if [ "$restore_exit" -ne 0 ] && grep -q 'active' "$WORK_DIR/restore.log"; then
    pass "restore refuses to run while the service is active"
else
    fail "restore must refuse while the service is active (exit code $restore_exit)"
fi

# --- check-config exit code and secret masking --------------------------------
check_exit=0
( cd "$WORK_DIR" && "$BIN" check-config ) > "$WORK_DIR/check-config.log" 2>&1 || check_exit=$?
if [ "$check_exit" -eq 0 ] \
    && ! grep -q "$ADMIN_TOKEN" "$WORK_DIR/check-config.log" \
    && ! grep -q "$PROXY_TOKEN" "$WORK_DIR/check-config.log"; then
    pass "check-config succeeds without printing any token"
else
    fail "check-config must exit 0 and mask the tokens (exit code $check_exit)"
fi

invalid_exit=0
( cd "$WORK_DIR" && PRISM_ADMIN_LISTEN=not-a-host-port "$BIN" check-config ) \
    > "$WORK_DIR/check-config-invalid.log" 2>&1 || invalid_exit=$?
if [ "$invalid_exit" -eq 1 ]; then
    pass "check-config exits 1 on an invalid PRISM_ADMIN_LISTEN"
else
    fail "invalid configuration must exit with code 1 (got $invalid_exit)"
fi


# --- restart persistence ------------------------------------------------------
kill "$PRISM_PID" 2>/dev/null
wait "$PRISM_PID" 2>/dev/null
PRISM_PID=""

start_prism
if ! wait_for_healthz "$MAIN_BASE"; then
    fail "service did not come back after a restart"
    exit 1
fi

if [ "$(http_code -H "Authorization: Bearer $ADMIN_TOKEN" "$MAIN_BASE/api/v1/platforms")" = "200" ] \
    && python3 -c 'import json,sys;sys.exit(0 if json.load(open(sys.argv[1])).get("total",0)>=1 else 1)' "$BODY_FILE" 2>/dev/null; then
    pass "platform survived the restart"
else
    fail "platforms were not restored after the restart"
fi

if [ "$(http_code -H "Authorization: Bearer $ADMIN_TOKEN" "$MAIN_BASE/api/v1/subscriptions")" = "200" ] \
    && grep -q 'local-smoke' "$BODY_FILE"; then
    pass "subscription survived the restart"
else
    fail "subscription was not restored after the restart"
fi

exit 0
