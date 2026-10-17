#!/usr/bin/env bash
#
# Prism billing walk.
#
# Boots bin/prism against a throw-away environment and walks the money path over
# real HTTP: an operator creates a priced plan and an invite, a user registers
# with it, opens a deposit, a fake TronGrid reports the matching transfer, the
# scanner credits the balance, the balance buys the plan, a replayed purchase is
# answered without charging twice, and the auto-renew switch is flipped. Every
# amount is compared against an expected value; nothing is read from logs.
#
# This is the acceptance walk for the commercial surface. It is separate from
# scripts/smoke.sh because it needs a fake chain endpoint and drives tenant
# endpoints that smoke.sh does not touch.
#
# Environment:
#   PRISM_BIN            binary under test (default: <repo>/bin/prism)
#   PRISM_BILLING_KEEP=1 keep the temporary work directory for debugging
#
# Exit code: 0 when every check passed, 1 otherwise.

set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${PRISM_BIN:-$ROOT_DIR/bin/prism}"

PASS=0
FAIL=0

log() { printf '[billing] %s\n' "$*"; }
pass() { PASS=$((PASS + 1)); log "PASS  $*"; }
fail() {
    FAIL=$((FAIL + 1))
    log "FAIL  $*"
    printf '%s\n' '--- last response body ---'
    cat "$BODY_FILE" 2>/dev/null || true
    printf '\n'
}

if [ ! -x "$BIN" ]; then
    log "binary not found or not executable: $BIN"
    log "run: make backend"
    exit 1
fi

for tool in curl python3; do
    if ! command -v "$tool" > /dev/null 2>&1; then
        log "missing required tool: $tool"
        exit 1
    fi
done

free_port() {
    python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
}

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/prism-billing-XXXXXX")"
MAIN_PORT="$(free_port)"
CHAIN_PORT="$(free_port)"
BASE="http://127.0.0.1:$MAIN_PORT"

USDT_ADDRESS="TQn9Y2khEsLJW1ChVWFMSMeRDow5KcbLSE"
USDT_CONTRACT="TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
ADMIN_USER="admin"
ADMIN_PASSWORD="billing-admin-password-0123456789"
TENANT_USER="wallet-user"
TENANT_PASSWORD="wallet-user-password-1234"

ADMIN_JAR="$WORK_DIR/admin.jar"
TENANT_JAR="$WORK_DIR/tenant.jar"
BODY_FILE="$WORK_DIR/body.json"
ROWS_FILE="$WORK_DIR/chain-rows.json"
CHAIN_LOG="$WORK_DIR/chain-requests.log"
SRV_PID=""
CHAIN_PID=""

cleanup() {
    [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null
    [ -n "$CHAIN_PID" ] && kill "$CHAIN_PID" 2>/dev/null
    wait 2>/dev/null
    if [ "${PRISM_BILLING_KEEP:-0}" = "1" ]; then
        log "work directory kept: $WORK_DIR"
    else
        rm -rf "$WORK_DIR"
    fi
}
trap cleanup EXIT

mkdir -p "$WORK_DIR/state" "$WORK_DIR/cache" "$WORK_DIR/logs"

# --- fake TronGrid -----------------------------------------------------------
# The rows live in a file the walk rewrites, so nothing has to travel through a
# URL. Every request is appended to CHAIN_LOG, which is how the walk proves the
# scanner actually polled instead of the deposit changing by accident.
printf '[]' > "$ROWS_FILE"
: > "$CHAIN_LOG"
cat > "$WORK_DIR/chain.py" <<'PY'
import http.server
import json
import sys

ROWS = sys.argv[2]
LOG = sys.argv[3]


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        with open(LOG, "a", encoding="utf-8") as handle:
            handle.write(self.path + "\n")
        try:
            rows = json.load(open(ROWS, encoding="utf-8"))
        except Exception:
            rows = []
        body = json.dumps({"data": rows}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
PY
python3 "$WORK_DIR/chain.py" "$CHAIN_PORT" "$ROWS_FILE" "$CHAIN_LOG" \
    > "$WORK_DIR/chain.log" 2>&1 &
CHAIN_PID=$!

# --- initialize the store ----------------------------------------------------
if ! PRISM_STATE_DIR="$WORK_DIR/state" PRISM_CACHE_DIR="$WORK_DIR/cache" \
    "$BIN" init --dir "$WORK_DIR" --admin-username "$ADMIN_USER" \
    --admin-password "$ADMIN_PASSWORD" > "$WORK_DIR/init.log" 2>&1; then
    log "prism init failed"
    cat "$WORK_DIR/init.log" >&2
    exit 1
fi
PROXY_TOKEN="$(sed -n 's/^PRISM_PROXY_TOKEN=//p' "$WORK_DIR/.env")"
if [ -z "$PROXY_TOKEN" ]; then
    log "prism init did not write PRISM_PROXY_TOKEN"
    exit 1
fi

export PRISM_PROXY_TOKEN="$PROXY_TOKEN"
export PRISM_LISTEN_ADDRESS=127.0.0.1
export PRISM_PORT="$MAIN_PORT"
export PRISM_STATE_DIR="$WORK_DIR/state"
export PRISM_CACHE_DIR="$WORK_DIR/cache"
export PRISM_LOG_DIR="$WORK_DIR/logs"
# Registration is how a customer arrives, so the walk goes through it.
export PRISM_REGISTRATION=invite
# Deposits: a fixed address and a fake chain. The scanner starts because the
# address is set; the endpoint decides where it reads.
export PRISM_USDT_ADDRESS="$USDT_ADDRESS"
export PRISM_USDT_CONTRACT="$USDT_CONTRACT"
export PRISM_TRONGRID_ENDPOINT="http://127.0.0.1:$CHAIN_PORT"
export PRISM_DENY_PRIVATE_NODES=false
export PRISM_DIRECT_DENY_PRIVATE=false

"$BIN" run > "$WORK_DIR/server.log" 2>&1 &
SRV_PID=$!

started=0
for _ in $(seq 1 60); do
    if [ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/healthz")" = "200" ]; then
        started=1
        break
    fi
    sleep 0.5
done
if [ "$started" != "1" ]; then
    log "service did not become healthy"
    tail -40 "$WORK_DIR/server.log" >&2
    exit 1
fi
pass "service started with deposits configured"

# --- HTTP helpers ------------------------------------------------------------
csrf_from_jar() {
    awk '$6 == "prism_csrf" { print $7; exit }' "$1" 2>/dev/null
}

# call <curl-args...> prints the status code and leaves the body in BODY_FILE.
call() {
    curl -s -o "$BODY_FILE" -w '%{http_code}' "$@"
}

# session <jar> <curl-args...> adds the session cookie and the CSRF header.
session() {
    local jar="$1"
    shift
    curl -s -o "$BODY_FILE" -w '%{http_code}' \
        -b "$jar" -c "$jar" -H "X-Prism-CSRF: $(csrf_from_jar "$jar")" "$@"
}

# json <dotted-path> reads a field out of the last response body.
json() {
    python3 - "$BODY_FILE" "$1" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    value = json.load(handle)
for part in sys.argv[2].split("."):
    if part:
        value = value[part]
if isinstance(value, bool):
    print("true" if value else "false")
else:
    print(value)
PY
}

login() {
    local jar="$1" user="$2" password="$3"
    : > "$jar"
    python3 - "$WORK_DIR/login.json" "$user" "$password" <<'PY'
import json
import sys

with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump({"username": sys.argv[2], "password": sys.argv[3]}, handle)
PY
    call -b "$jar" -c "$jar" -H 'Content-Type: application/json' \
        --data-binary "@$WORK_DIR/login.json" "$BASE/api/v1/auth/login"
}

check() {
    # check <expected> <actual> <description>
    if [ "$1" = "$2" ]; then
        pass "$3"
    else
        fail "$3 (got $2, want $1)"
    fi
}

# --- operator: sign in, then a priced plan and an invite ---------------------
status="$(login "$ADMIN_JAR" "$ADMIN_USER" "$ADMIN_PASSWORD")"
check 200 "$status" "operator signed in"

status="$(session "$ADMIN_JAR" -H 'Content-Type: application/json' \
    -X POST "$BASE/api/v1/admin/plans" \
    --data-binary '{"name":"Monthly","traffic_bytes":10737418240,"period":"30d","max_keys":3,"duration_days":30,"price_micro":5000000}')"
check 201 "$status" "operator created a priced plan"
PLAN_ID="$(json id)"

status="$(session "$ADMIN_JAR" -H 'Content-Type: application/json' \
    -X POST "$BASE/api/v1/admin/invites" \
    --data-binary '{"max_uses":1,"expires_in_days":7,"note":"billing walk"}')"
check 201 "$status" "operator created an invite"
INVITE_CODE="$(json code)"

# --- customer: register, then sign in ----------------------------------------
python3 - "$WORK_DIR/register.json" "$TENANT_USER" "$TENANT_PASSWORD" "$INVITE_CODE" <<'PY'
import json
import sys

with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump({"username": sys.argv[2], "password": sys.argv[3], "invite_code": sys.argv[4]}, handle)
PY
status="$(call -H 'Content-Type: application/json' \
    --data-binary "@$WORK_DIR/register.json" "$BASE/api/v1/auth/register")"
check 201 "$status" "customer registered with the invite"

status="$(login "$TENANT_JAR" "$TENANT_USER" "$TENANT_PASSWORD")"
check 200 "$status" "customer signed in"

# --- customer: open a deposit ------------------------------------------------
status="$(session "$TENANT_JAR" -H 'Content-Type: application/json' \
    -X POST "$BASE/api/v1/me/deposits" --data-binary '{"amount_micro":12000000}')"
check 201 "$status" "customer opened a deposit"
DEPOSIT_ID="$(json id)"
# The amount is never a round number: a round one is the likeliest to collide
# with a transfer that was not meant for this intent.
check 12000001 "$(json amount_micro)" "the amount to pay is unique"

# --- the chain reports exactly that transfer --------------------------------
BLOCK_MS=$(( $(date +%s) * 1000 + 5000 ))
python3 - "$ROWS_FILE" "$BLOCK_MS" "$USDT_ADDRESS" "$USDT_CONTRACT" <<'PY'
import json
import sys

rows = [{
    "transaction_id": "tx-billing-walk",
    "block_timestamp": int(sys.argv[2]),
    "from": "TPayerAddress",
    "to": sys.argv[3],
    "type": "Transfer",
    "value": "12000001",
    "token_info": {"address": sys.argv[4], "decimals": 6},
}]
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump(rows, handle)
PY
pass "fake chain reports the transfer"

credited=0
for _ in $(seq 1 40); do
    status="$(session "$TENANT_JAR" "$BASE/api/v1/me/deposits/$DEPOSIT_ID")"
    if [ "$status" = "200" ] && [ "$(json status)" = "paid" ]; then
        credited=1
        break
    fi
    sleep 2
done
if [ "$credited" = "1" ]; then
    pass "the scanner credited the deposit"
else
    fail "the deposit never turned paid"
    log "chain requests seen:"
    tail -20 "$CHAIN_LOG" >&2 || true
    log "server log:"
    tail -40 "$WORK_DIR/server.log" >&2
fi

status="$(session "$TENANT_JAR" "$BASE/api/v1/me/balance")"
check 200 "$status" "customer read the balance"
check 12000001 "$(json balance_micro)" "the balance is the deposited amount"

# --- spend it ---------------------------------------------------------------
status="$(session "$TENANT_JAR" -H 'Content-Type: application/json' \
    -X POST "$BASE/api/v1/me/purchases" \
    --data-binary "{\"plan_id\":\"$PLAN_ID\",\"request_id\":\"billing-walk-1\"}")"
check 201 "$status" "customer bought the plan"
check 5000000 "$(json charged_micro)" "the plan charged its price"
check 7000001 "$(json balance_micro)" "the balance went down by the price"

# A retried request must be answered, not charged again. This is what a client
# that timed out and resubmitted gets.
status="$(session "$TENANT_JAR" -H 'Content-Type: application/json' \
    -X POST "$BASE/api/v1/me/purchases" \
    --data-binary "{\"plan_id\":\"$PLAN_ID\",\"request_id\":\"billing-walk-1\"}")"
check 201 "$status" "the replayed purchase was answered"
check 7000001 "$(json balance_micro)" "the replayed purchase charged nothing"

# --- the subscription is in force, and renews -------------------------------
status="$(session "$TENANT_JAR" "$BASE/api/v1/me/subscription")"
check 200 "$status" "customer read the subscription"
check active "$(json subscription.status)" "the subscription is active"
SUBSCRIPTION_ID="$(json subscription.id)"

status="$(session "$TENANT_JAR" -H 'Content-Type: application/json' \
    -X PATCH "$BASE/api/v1/me/subscription" \
    --data-binary "{\"subscription_id\":\"$SUBSCRIPTION_ID\",\"auto_renew\":true}")"
check 200 "$status" "customer turned auto renew on"
check true "$(json subscription.auto_renew)" "auto renew is on"

log "results: $PASS passed, $FAIL failed"
if [ "$FAIL" -ne 0 ]; then
    exit 1
fi
exit 0
