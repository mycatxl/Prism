#!/usr/bin/env bash
#
# Prism smoke test.
#
# Boots bin/prism against a throw-away environment (temporary state/cache/log
# directories plus an admin account and a subscribed tenant access key) and exercises the
# health, UI, API auth, environment-defined management listener, HTTP and SOCKS5
# forwarding through a local HTTP proxy node, the reverse proxy path and
# restart persistence.
#
# Environment:
#   PRISM_BIN     binary under test (default: <repo>/bin/prism)
#   PRISM_SMOKE_KEEP=1   keep the temporary work directory for debugging
#
# Note: the forwarding checks need a node that has passed its egress probe. This
# script answers that probe from a loopback endpoint and points
# PRISM_EGRESS_TRACE_URL at it, so the whole run works without outbound internet
# access.
# Exit code: 0 when every check passed (or was skipped), 1 otherwise.

set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${PRISM_BIN:-$ROOT_DIR/bin/prism}"
source "$ROOT_DIR/scripts/test-env.sh"

# Historical flag: a direct public-provider diagnostic, never independent-node
# or three-protocol acceptance. It must fail the direct/own-asset boundary.
if [ "${1:-}" = "--live-purity" ]; then
    cd "$ROOT_DIR"
    exec env PRISM_LIVE_PURITY_SMOKE=1 go test -count=1 -v -timeout=90s \
        -tags 'with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy' \
        ./internal/intel -run '^TestLivePuritySmoke$'
fi

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
    for tool in curl python3 openssl; do
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

csrf_from_jar() {
    # csrf_from_jar <cookie-jar> -> prints the readable double-submit token.
    awk '$6 == "prism_csrf" { print $7; exit }' "$1" 2>/dev/null
}

login_session() {
    # login_session <base> <cookie-jar>
    local base="$1" jar="$2" status
    printf '{"username":"%s","password":"%s"}\n' "$ADMIN_USER" "$ADMIN_PASSWORD" > "$WORK_DIR/login.json"
    : > "$jar"
    status="$(http_code -b "$jar" -c "$jar" \
        -H 'Content-Type: application/json' \
        --data-binary "@$WORK_DIR/login.json" \
        "$base/api/v1/auth/login")"
    if [ "$status" != "200" ]; then
        log "login failed for $base (HTTP $status)"
        sed "s/${ADMIN_PASSWORD}/***REDACTED***/g" "$BODY_FILE" >&2 || true
        return 1
    fi
    if [ -z "$(csrf_from_jar "$jar")" ]; then
        log "login for $base did not issue a CSRF cookie"
        return 1
    fi
}

session_code() {
    # session_code <cookie-jar> <curl-args...>
    local jar="$1"
    shift
    local csrf
    csrf="$(csrf_from_jar "$jar")"
    http_code -b "$jar" -c "$jar" -H "X-Prism-CSRF: $csrf" "$@"
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
TRACE_PID=""
BODY_FILE="$WORK_DIR/body.txt"
PRISM_LOG="$WORK_DIR/prism.log"
PROXY_TOKEN=""
PROXY_CREDENTIAL=""
MAIN_JAR="$WORK_DIR/main.cookies"
ADMIN_JAR="$WORK_DIR/admin.cookies"
MAIN_CSRF=""
ADMIN_CSRF=""

cleanup() {
    local status=$?
    for pid in "$PRISM_PID" "$TARGET_PID" "$PROXY_PID" "$TRACE_PID"; do
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null
            wait "$pid" 2>/dev/null
        fi
    done
    if [ "$FAIL" -gt 0 ] && [ -f "$PRISM_LOG" ]; then
        printf '\n[smoke] --- last Prism log lines (secrets redacted) ---\n'
        sed -e "s/${PROXY_TOKEN:-__none__}/***REDACTED***/g" \
            -e "s/${PROXY_CREDENTIAL:-__none__}/***REDACTED***/g" \
            -e "s/${ADMIN_PASSWORD:-__none__}/***REDACTED***/g" "$PRISM_LOG" | tail -n 40
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

PROXY_TOKEN=""
ADMIN_USER="admin"
ADMIN_PASSWORD="smoke-admin-password-0123456789"
MAIN_PORT="$(free_port)"
ADMIN_PORT="$(free_port)"
TARGET_PORT="$(free_port)"
TRACE_PORT="$(free_port)"
PROXY_PORT="$(free_port)"

MAIN_BASE="http://127.0.0.1:$MAIN_PORT"
ADMIN_BASE="http://127.0.0.1:$ADMIN_PORT"

mkdir -p "$WORK_DIR/state" "$WORK_DIR/cache" "$WORK_DIR/logs" "$WORK_DIR/www"
cd "$WORK_DIR" || exit 1
printf 'PRISM_SMOKE_TARGET\n' > "$WORK_DIR/www/index.html"

if ! PRISM_STATE_DIR="$WORK_DIR/state" PRISM_CACHE_DIR="$WORK_DIR/cache" \
    "$BIN" init --dir "$WORK_DIR" --admin-username "$ADMIN_USER" \
    --admin-password "$ADMIN_PASSWORD" > "$WORK_DIR/init.log" 2>&1; then
    fail "prism init failed"
    cat "$WORK_DIR/init.log" >&2
    exit 1
fi
PROXY_TOKEN="$(sed -n 's/^PRISM_PROXY_TOKEN=//p' "$WORK_DIR/.env")"
if [ -z "$PROXY_TOKEN" ]; then
    fail "prism init did not write PRISM_PROXY_TOKEN"
    exit 1
fi

# Existing process variables win over the generated .env, so the smoke run can
# use dynamically reserved ports while still exercising the initialized store
# and pepper.
export PRISM_PROXY_TOKEN="$PROXY_TOKEN"
export PRISM_LISTEN_ADDRESS=127.0.0.1
export PRISM_PORT="$MAIN_PORT"
export PRISM_ADMIN_LISTEN="127.0.0.1:$ADMIN_PORT"
export PRISM_STATE_DIR="$WORK_DIR/state"
export PRISM_CACHE_DIR="$WORK_DIR/cache"
export PRISM_LOG_DIR="$WORK_DIR/logs"
export PRISM_EGRESS_TRACE_URL="https://localhost:$TRACE_PORT/cdn-cgi/trace"
# All data-plane requests traverse the TLS node; direct resale is forbidden.
# The fixture maps example.com to its local target without external traffic.
export PRISM_PROXY_BYPASS=""
export PRISM_RESOURCE_FETCH_ALLOW_PRIVATE=true
export PRISM_DENY_PRIVATE_NODES=false
export PRISM_DIRECT_DENY_PRIVATE=false

# A temporary CA is trusted only by this smoke process. No production TLS
# verification or privacy gate is disabled to make the fixture routable.
if ! openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=localhost \
    -addext subjectAltName=DNS:localhost,DNS:my.ippure.com -keyout "$WORK_DIR/fixture.key" \
    -out "$WORK_DIR/fixture.crt" > "$WORK_DIR/tls.log" 2>&1; then
    fail "could not create fixture TLS certificate"
    exit 1
fi
export SSL_CERT_FILE="$WORK_DIR/fixture.crt"
# Explicit deterministic runtime settings, including a direct self-IP fixture.
python3 "$ROOT_DIR/scripts/seed-test-privacy.py" "$WORK_DIR/state/state.db" "https://localhost:$TRACE_PORT" || exit 1

# --- local test target server -------------------------------------------------
python3 -m http.server "$TARGET_PORT" --bind 127.0.0.1 --directory "$WORK_DIR/www" \
    > "$WORK_DIR/target.log" 2>&1 &
TARGET_PID=$!

# --- local egress-trace endpoint ---------------------------------------------
# A node only becomes routable after its egress probe succeeds. The probe target
# is configurable (egress_trace_url / PRISM_EGRESS_TRACE_URL), so this run points
# it at a loopback server that answers the Cloudflare trace shape. That is what
# keeps the forwarding checks working with no outbound internet access.
cat > "$WORK_DIR/trace.py" <<'PY'
"""Minimal Cloudflare-trace endpoint for the offline smoke run."""
import http.server
import json
import sys
import socket
import ssl
import threading


class TraceHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = b"fl=smoke\nh=trace.local\nip=1.1.1.1\nts=0\nloc=SM\ncolo=SMOKE\n"
        if self.path.startswith("/v1/info"):
            body = json.dumps({"ip":"1.1.1.1","fraudScore":30,"isResidential":True,"countryCode":"US"}).encode()
        if self.path == "/self":
            body = b"2001:db8::254" if ":" in self.client_address[0] else b"203.0.113.254"
        if self.path == "/check":
            body = json.dumps({"observed_address":"1.1.1.1", "marker":self.headers.get("X-Prism-Privacy-Marker"),
                               "marker_survived":True, "headers":{}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(sys.argv[2],sys.argv[3])
class IPv6Server(http.server.ThreadingHTTPServer):
    address_family=socket.AF_INET6
for host,server_class in [('127.0.0.1',http.server.ThreadingHTTPServer),('::1',IPv6Server)]:
    try:
        server=server_class((host,int(sys.argv[1])),TraceHandler)
        server.socket=ctx.wrap_socket(server.socket,server_side=True)
        threading.Thread(target=server.serve_forever,daemon=True).start()
    except OSError:
        if host=='127.0.0.1':raise
threading.Event().wait()
PY
python3 "$WORK_DIR/trace.py" "$TRACE_PORT" "$WORK_DIR/fixture.crt" "$WORK_DIR/fixture.key" > "$WORK_DIR/trace.log" 2>&1 &
TRACE_PID=$!

# --- local HTTP proxy used as the single subscription node --------------------
cat > "$WORK_DIR/proxy.py" <<'PY'
"""Minimal HTTP proxy used as a Prism node: absolute-form requests + CONNECT."""
import socket
import ssl
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
            if host not in ("my.ippure.com", "example.com", "localhost", "127.0.0.1"):
                conn.sendall(b"HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
                return
            if host == "my.ippure.com":
                host, port = "127.0.0.1", sys.argv[4]
            upstream = socket.create_connection(("127.0.0.1" if host == "example.com" else host, int(port or 443)), timeout=30)
            conn.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
            threading.Thread(target=pump, args=(conn, upstream), daemon=True).start()
            pump(upstream, conn)
            return

        parts = urlsplit(target.decode())
        if parts.hostname not in ("example.com", "localhost", "127.0.0.1"):
            conn.sendall(b"HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
            return
        path = parts.path or "/"
        if parts.query:
            path += "?" + parts.query
        upstream = socket.create_connection(
            ("127.0.0.1" if parts.hostname == "example.com" else parts.hostname, parts.port or 80), timeout=30
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
    ctx=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain(sys.argv[2],sys.argv[3])
    server=ctx.wrap_socket(server,server_side=True)
    print("proxy ready", flush=True)
    while True:
        conn, _ = server.accept()
        threading.Thread(target=handle, args=(conn,), daemon=True).start()


main()
PY
python3 "$WORK_DIR/proxy.py" "$PROXY_PORT" "$WORK_DIR/fixture.crt" "$WORK_DIR/fixture.key" "$TRACE_PORT" > "$WORK_DIR/proxy.log" 2>&1 &
PROXY_PID=$!

# Own-asset discovery runs once before Prism opens its listeners. Do not race
# that startup probe against the Python TLS fixture binding its socket.
if ! retry_request --noproxy '*' --ipv4 --cacert "$WORK_DIR/fixture.crt" \
    "https://localhost:$TRACE_PORT/self" || ! grep -qx '203.0.113.254' "$BODY_FILE"; then
    fail "local TLS self-discovery fixture did not become ready"
    exit 1
fi

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

check_status "/api/v1/system/info rejects missing session" 401 \
    "$(http_code "$MAIN_BASE/api/v1/system/info")"
if login_session "$MAIN_BASE" "$MAIN_JAR"; then
    pass "main listener login issued session and CSRF cookies"
else
    fail "main listener login failed"
fi
check_status "/api/v1/system/info accepts the session" 200 \
    "$(session_code "$MAIN_JAR" "$MAIN_BASE/api/v1/system/info")"
if grep -q '"build_tags"' "$BODY_FILE"; then
    pass "/api/v1/system/info reports build_tags"
else
    fail "/api/v1/system/info is missing build_tags"
fi

# --- optional independent management listener (PRISM_ADMIN_LISTEN) ------------
check_status "admin listener /healthz" 200 "$(http_code "$ADMIN_BASE/healthz")"
if login_session "$ADMIN_BASE" "$ADMIN_JAR"; then
    pass "admin listener login issued session and CSRF cookies"
else
    fail "admin listener login failed"
fi
check_status "admin listener /api with session" 200 \
    "$(session_code "$ADMIN_JAR" "$ADMIN_BASE/api/v1/system/info")"
admin_proxy_status="$(http_code -X CONNECT "$ADMIN_BASE/")"
if [ "$admin_proxy_status" = "404" ] || [ "$admin_proxy_status" = "405" ]; then
    pass "admin listener refuses CONNECT (HTTP $admin_proxy_status)"
else
    fail "admin listener must not accept proxy traffic (CONNECT returned HTTP $admin_proxy_status)"
fi

# --- local subscription with one HTTP proxy node ------------------------------
create_body="$WORK_DIR/create-sub.json"
python3 - "$PROXY_PORT" > "$create_body" <<'PYNODE'
import json,sys
node={'type':'http','tag':'smoke-tls','server':'127.0.0.1','server_port':int(sys.argv[1]),'tls':{'enabled':True,'server_name':'localhost'}}
print(json.dumps({'name':'local-smoke','source_type':'local','content':json.dumps({'outbounds':[node]}),'enabled':True,'auto_intel':False}))
PYNODE
create_status="$(session_code "$MAIN_JAR" -X POST \
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
        "$(session_code "$MAIN_JAR" -X POST \
            "$MAIN_BASE/api/v1/subscriptions/$SUB_ID/actions/refresh")"
fi

nodes_ready=0
attempts=0
while [ "$attempts" -lt 60 ]; do
    if [ "$(session_code "$MAIN_JAR" "$MAIN_BASE/api/v1/nodes?limit=10")" = "200" ] \
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

# --- subscribed tenant credential for the data plane --------------------------
# PRISM_PROXY_TOKEN is a deployment setting, not a usable access key. Exercise
# the same plan, owner and credential checks as real proxy clients.
plan_status="$(session_code "$MAIN_JAR" -X POST -H 'Content-Type: application/json' \
    --data-binary '{"name":"smoke-plan","traffic_bytes":10485760,"period":"30d","max_keys":1,"duration_days":30}' \
    "$MAIN_BASE/api/v1/admin/plans")"
SMOKE_PLAN_ID="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("id",""))' "$BODY_FILE" 2>/dev/null)"
if [ "$plan_status" != "201" ] || [ -z "$SMOKE_PLAN_ID" ]; then
    fail "proxy fixture plan creation failed (HTTP $plan_status)"
    exit 1
fi
pass "proxy fixture plan created"
python3 - "$SMOKE_PLAN_ID" > "$WORK_DIR/proxy-user.json" <<'PY'
import json, secrets, sys
print(json.dumps({"username": "smoke-tenant", "password": secrets.token_hex(24),
                  "role": "user", "plan_id": sys.argv[1], "duration_days": 30}))
PY
user_status="$(session_code "$MAIN_JAR" -X POST -H 'Content-Type: application/json' \
    --data-binary "@$WORK_DIR/proxy-user.json" "$MAIN_BASE/api/v1/admin/users")"
SMOKE_USER_ID="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("id",""))' "$BODY_FILE" 2>/dev/null)"
if [ "$user_status" != "201" ] || [ -z "$SMOKE_USER_ID" ]; then
    fail "proxy fixture tenant creation failed (HTTP $user_status)"
    exit 1
fi
pass "proxy fixture tenant has an active subscription"
key_status="$(session_code "$MAIN_JAR" -X POST -H 'Content-Type: application/json' \
    --data-binary '{"name":"smoke-client"}' "$MAIN_BASE/api/v1/admin/users/$SMOKE_USER_ID/keys")"
PROXY_CREDENTIAL="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("credential",""))' "$BODY_FILE" 2>/dev/null)"
if [ "$key_status" != "201" ] || [[ "$PROXY_CREDENTIAL" != pk_* ]]; then
    fail "proxy fixture key creation failed (HTTP $key_status)"
    exit 1
fi
pass "proxy fixture access key issued"
PROXY_URL="http://Default:$PROXY_CREDENTIAL@127.0.0.1:$MAIN_PORT"
SOCKS_URL="socks5h://Default:$PROXY_CREDENTIAL@127.0.0.1:$MAIN_PORT"

# Exercise the offline pipeline with the local TLS provider fixture.
# This does not verify public IPPure or qualify as live purity acceptance.
python3 - "$MAIN_BASE" "$MAIN_JAR" <<'PYMEASURE'
import http.cookiejar,json,sys,time,urllib.request,urllib.error
jar=http.cookiejar.MozillaCookieJar(sys.argv[2]);jar.load(ignore_discard=True,ignore_expires=True)
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(jar))
def call(method,path,body=None):
    headers={'Content-Type':'application/json'}
    for c in jar:
        if c.name=='prism_csrf':headers['X-Prism-CSRF']=c.value
    req=urllib.request.Request(sys.argv[1]+path,method=method,headers=headers,data=None if body is None else json.dumps(body).encode())
    try:
        with opener.open(req,timeout=15) as response:return json.load(response)
    except urllib.error.HTTPError as err:
        print('[smoke] FAIL  measurement API '+method+' '+path.split('?')[0]+' status='+str(err.code))
        if path=='/api/v1/system/config':
            print('[smoke] FAIL  measurement config: '+str(json.load(err).get('error',{}).get('message','')))
        raise
try:
    defaults=call('GET','/api/v1/system/config/default')
    current=call('GET','/api/v1/system/config')
    # Measurement must leave payment/notification credentials and their
    # read-only presence flags out of a runtime configuration PATCH.
    patch={k:v for k,v in defaults.items() if v is not None and k not in ('node_privacy','payment_config_initialized') and not k.startswith(('ldc_','tron_','alert_telegram_'))}
    patch.update({k:current[k] for k in ('latency_test_url','egress_trace_url','latency_authorities','cache_flush_interval')})
    patch['intel_enabled']=True
    call('PATCH','/api/v1/system/config',patch)
    providers=call('GET','/api/v1/intel/providers')
    providers=providers.get('items',providers.get('providers',[])) if isinstance(providers,dict) else providers
    for provider in providers:
        pid=provider.get('id',provider.get('spec',{}).get('id'))
        call('PATCH','/api/v1/intel/providers/'+pid,{'enabled':pid=='ippure'})
    nodes=call('GET','/api/v1/nodes')['items']
    hashes=[n.get('hash',n.get('node_hash')) for n in nodes]
    for h in hashes:call('POST','/api/v1/nodes/'+h+'/actions/probe-latency',{})
    job=call('POST','/api/v1/intel/jobs',{'kind':'intel','scope':{'node_hashes':hashes},'providers':['ippure']})
    deadline=time.monotonic()+45
    while time.monotonic()<deadline:
        state=call('GET','/api/v1/intel/jobs/'+job['job']['id'])
        if state.get('job',state).get('status')=='succeeded':break
        time.sleep(.2)
    else:raise RuntimeError('measurement did not complete')
except Exception as err:
    print('[smoke] FAIL  offline fixture measurement did not complete: '+type(err).__name__+(' status='+str(err.code) if isinstance(err,urllib.error.HTTPError) else ''))
    raise SystemExit(1)
PYMEASURE
if [ "$?" -ne 0 ]; then
    fail "offline provider fixture failed"
    exit 1
fi
pass "offline provider fixture completed (not live IPPure acceptance)"

# --- forwarding through the local HTTP proxy node -----------------------------
if retry_request --noproxy '' -x "$PROXY_URL" "http://example.com:$TARGET_PORT/"; then
    if grep -q 'PRISM_SMOKE_TARGET' "$BODY_FILE"; then
        pass "HTTP forward proxy (-x http://) reached the local target"
    else
        fail "HTTP forward proxy returned 200 without the target body"
    fi
else
    fail "HTTP forward proxy (-x http://) did not succeed"
fi

if retry_request --noproxy '' --proxy "$SOCKS_URL" "http://example.com:$TARGET_PORT/"; then
    if grep -q 'PRISM_SMOKE_TARGET' "$BODY_FILE"; then
        pass "SOCKS5 inbound (--proxy socks5h://) reached the local target"
    else
        fail "SOCKS5 inbound returned 200 without the target body"
    fi
else
    fail "SOCKS5 inbound (--proxy socks5h://) did not succeed"
fi

# Reverse proxy to a loopback target with an explicit platform/account identity.
# Dot segments are rejected by the current route grammar.
reverse_status="$(http_code "$MAIN_BASE/$PROXY_CREDENTIAL/Default:smoke/http/example.com:$TARGET_PORT/")"
if [ "$reverse_status" = "200" ] && grep -q 'PRISM_SMOKE_TARGET' "$BODY_FILE"; then
    pass "reverse proxy path /<key>/Default:smoke/http/example.com:$TARGET_PORT/ reached the local target"
else
    fail "reverse proxy path /<key>/Default:smoke/http/example.com:$TARGET_PORT/ returned HTTP $reverse_status"
fi

# Run the actual acceptance CLI against this disposable binary. Fixture mode is
# permanently labelled NOT_RUN for live acceptance, even when every check passes.
if python3 -B "$ROOT_DIR/scripts/live_acceptance_binary_test.py" "$MAIN_BASE" "$MAIN_JAR" \
    "$WORK_DIR/fixture.crt" "https://localhost:$TRACE_PORT" "$WORK_DIR" \
    3< <(printf '%s\n' "$PROXY_CREDENTIAL"); then
    pass "acceptance CLI correlates all three protocols and rejects a bad key (offline only)"
else
    fail "acceptance CLI binary fixture regression"
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
if not names <= {"state.db", "cache.db", "intel.db", "pepper"}:
    raise SystemExit(f"manifest lists unexpected files: {sorted(names)}")
# pepper is not a database; it is the key the password hashes are derived from,
# so a backup without it cannot verify a single stored credential.
if "pepper" not in names:
    raise SystemExit("manifest is missing the password pepper")
for name in sorted(names & {"state.db", "cache.db", "intel.db"}):
    conn = sqlite3.connect(root / name)
    check = conn.execute("PRAGMA quick_check").fetchone()[0]
    conn.close()
    if check != "ok":
        raise SystemExit(f"quick_check {name}: {check}")
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
    && ! grep -q "$ADMIN_PASSWORD" "$WORK_DIR/check-config.log" \
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

if [ "$(session_code "$MAIN_JAR" "$MAIN_BASE/api/v1/platforms")" = "200" ] \
    && python3 -c 'import json,sys;sys.exit(0 if json.load(open(sys.argv[1])).get("total",0)>=1 else 1)' "$BODY_FILE" 2>/dev/null; then
    pass "platform survived the restart"
else
    fail "platforms were not restored after the restart"
fi

if [ "$(session_code "$MAIN_JAR" "$MAIN_BASE/api/v1/subscriptions")" = "200" ] \
    && grep -q 'local-smoke' "$BODY_FILE"; then
    pass "subscription survived the restart"
else
    fail "subscription was not restored after the restart"
fi

exit 0
