#!/usr/bin/env bash
#
# Prism deployment script (WP04 §4.10).
#
#   * refuses to run without a compiled binary (<dir>/bin/prism)
#   * generates <dir>/.env through `prism init` when it is missing and never
#     overwrites an existing .env
#   * rewrites individual PRISM_* keys on request, after backing the file up
#   * installs a hardened systemd unit that runs as the dedicated "prism" user
#
# The script never prints tokens and never stops or kills a process: an
# occupied port is reported as an error.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_DEPLOY_DIR="$(dirname "$SCRIPT_DIR")"

DEPLOY_DIR="$DEFAULT_DEPLOY_DIR"
SERVICE_NAME="prism"
SERVICE_USER="prism"

INSTALL_SERVICE=true
DRY_RUN=false

OPT_LISTEN=""
OPT_PORT=""
OPT_STATE_DIR=""
OPT_CACHE_DIR=""
OPT_LOG_DIR=""

ENV_FILE=""
ENV_BACKUP=""
UNIT_PATH=""
STATE_ABS=""
CACHE_ABS=""
LOG_ABS=""
EFFECTIVE_LISTEN=""
EFFECTIVE_PORT=""
EFFECTIVE_STATE_DIR=""
EFFECTIVE_CACHE_DIR=""
EFFECTIVE_LOG_DIR=""
ORIGINAL_ARGS=()

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1" >&2
}

log_step() {
    echo -e "${BLUE}[STEP]${NC} $1"
}

usage() {
    cat <<EOF
Usage: $(basename "$0") [options]

Deploys a compiled Prism build. The deployment directory must contain
bin/prism (run "make build" in the repository first).

Options:
  --dir <path>           Deployment directory (default: $DEFAULT_DEPLOY_DIR).
                         Must contain bin/prism and receives .env.
  --listen <address>     Set PRISM_LISTEN_ADDRESS (e.g. 127.0.0.1 or 0.0.0.0).
  --port <port>          Set PRISM_PORT (default after init: 2260).
  --state-dir <path>     Set PRISM_STATE_DIR (relative paths resolve against
                         the deployment directory).
  --cache-dir <path>     Set PRISM_CACHE_DIR.
  --log-dir <path>       Set PRISM_LOG_DIR.
  --no-service           Only prepare .env and the data directories; do not
                         install the systemd unit.
  --dry-run              Print what would be done without changing anything.
  -h, --help             Show this help message.

The five --* override options touch only the matching PRISM_* key: every other
line of an existing .env is preserved, and the file is backed up to
<.env>.bak.<timestamp> before the first change. An existing .env is never
regenerated or overwritten.

Examples:
  ./scripts/deploy.sh
  ./scripts/deploy.sh --listen 127.0.0.1 --port 2260 --state-dir /var/lib/prism/state
  ./scripts/deploy.sh --dir /opt/prism --no-service
EOF
}

# --- helpers ------------------------------------------------------------------

require_value() {
    # require_value <flag> <value>; reads the next argument for a flag.
    local flag="$1"
    shift
    if [[ $# -eq 0 || -z "$1" ]]; then
        log_error "$flag requires a value"
        exit 1
    fi
    printf '%s' "$1"
}

# env_get <key> prints the value of KEY from ENV_FILE (empty when unset).
env_get() {
    local key="$1" line value
    if [[ ! -f "$ENV_FILE" ]]; then
        return 0
    fi
    line="$(grep -m1 "^${key}=" -- "$ENV_FILE" || true)"
    if [[ -z "$line" ]]; then
        return 0
    fi
    value="${line#*=}"
    # Strip the optional double quotes used for values with whitespace.
    if [[ "$value" == \"*\" ]]; then
        value="${value#\"}"
        value="${value%\"}"
    fi
    printf '%s' "$value"
}

# env_value_for_file <value> prints value prepared for a KEY=VALUE line.
env_value_for_file() {
    local value="$1"
    if [[ "$value" != *[[:space:]]* ]]; then
        printf '%s' "$value"
        return 0
    fi
    if [[ "$value" == *'"'* || "$value" == *'\'* || "$value" == *'#'* ]]; then
        log_error "value must not contain quotes, backslashes or '#': $value"
        exit 1
    fi
    printf '"%s"' "$value"
}

# abs_path resolves path against the deployment directory.
abs_path() {
    local path="$1"
    if [[ "$path" == /* ]]; then
        printf '%s' "$path"
        return 0
    fi
    path="${path#./}"
    printf '%s/%s' "${DEPLOY_DIR%/}" "$path"
}

# ensure_env_backup copies ENV_FILE aside once, before the first modification.
ensure_env_backup() {
    if [[ -n "$ENV_BACKUP" ]]; then
        return 0
    fi
    [[ -f "$ENV_FILE" ]] || return 0

    local stamp candidate i=1
    stamp="$(date +%Y%m%d-%H%M%S)"
    candidate="$ENV_FILE.bak.$stamp"
    while [[ -e "$candidate" ]]; do
        candidate="$ENV_FILE.bak.$stamp.$i"
        i=$((i + 1))
    done
    ENV_BACKUP="$candidate"

    if [[ "$DRY_RUN" == true ]]; then
        log_info "[dry-run] would back up $ENV_FILE to $ENV_BACKUP"
        return 0
    fi
    cp -p -- "$ENV_FILE" "$ENV_BACKUP"
    chmod 0600 -- "$ENV_BACKUP"
    log_info "Backed up $ENV_FILE to $ENV_BACKUP"
}

# set_env_key rewrites a single KEY=VALUE line and keeps everything else.
set_env_key() {
    local key="$1" value="$2" encoded tmp
    encoded="$(env_value_for_file "$value")"

    ensure_env_backup

    if [[ "$DRY_RUN" == true ]]; then
        log_info "[dry-run] would set $key=$value in $ENV_FILE"
        return 0
    fi

    tmp="$(mktemp "${ENV_FILE}.tmp.XXXXXX")"
    if ! awk -v key="$key" -v val="$encoded" '
        index($0, key "=") == 1 {
            if (!done) { print key "=" val; done = 1 }
            next
        }
        { print }
        END { if (!done) print key "=" val }
    ' "$ENV_FILE" > "$tmp"; then
        rm -f -- "$tmp"
        log_error "Failed to update $key in $ENV_FILE"
        exit 1
    fi
    chmod 0600 -- "$tmp"
    mv -f -- "$tmp" "$ENV_FILE"
    log_info "Set $key in $ENV_FILE"
}

# port_in_use <port>: 0 = in use, 1 = free, 2 = could not be determined.
port_in_use() {
    local port="$1"
    if command -v ss >/dev/null 2>&1; then
        ss -H -ltn "sport = :$port" 2>/dev/null | grep -q .
        return $?
    fi
    if command -v lsof >/dev/null 2>&1; then
        lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1
        return $?
    fi
    if command -v netstat >/dev/null 2>&1; then
        netstat -ltn 2>/dev/null | awk '{print $4}' | grep -qE "[:.]${port}\$"
        return $?
    fi
    return 2
}

show_port_owner() {
    local port="$1"
    if command -v ss >/dev/null 2>&1; then
        ss -H -ltn "sport = :$port" 2>/dev/null || true
        return 0
    fi
    if command -v lsof >/dev/null 2>&1; then
        lsof -nP -iTCP:"$port" -sTCP:LISTEN 2>/dev/null || true
        return 0
    fi
    return 0
}

# --- steps --------------------------------------------------------------------

parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --dir)
                DEPLOY_DIR="$(require_value "$1" "${2:-}")"
                shift 2
                ;;
            --listen)
                OPT_LISTEN="$(require_value "$1" "${2:-}")"
                shift 2
                ;;
            --port)
                OPT_PORT="$(require_value "$1" "${2:-}")"
                shift 2
                ;;
            --state-dir)
                OPT_STATE_DIR="$(require_value "$1" "${2:-}")"
                shift 2
                ;;
            --cache-dir)
                OPT_CACHE_DIR="$(require_value "$1" "${2:-}")"
                shift 2
                ;;
            --log-dir)
                OPT_LOG_DIR="$(require_value "$1" "${2:-}")"
                shift 2
                ;;
            --no-service)
                INSTALL_SERVICE=false
                shift
                ;;
            --dry-run)
                DRY_RUN=true
                shift
                ;;
            -h|--help)
                usage
                exit 0
                ;;
            *)
                log_error "Unknown option: $1"
                usage >&2
                exit 1
                ;;
        esac
    done
}

check_deploy_dir() {
    if [[ ! -d "$DEPLOY_DIR" ]]; then
        log_error "Deployment directory does not exist: $DEPLOY_DIR"
        exit 1
    fi
    DEPLOY_DIR="$(cd "$DEPLOY_DIR" && pwd)"
    ENV_FILE="$DEPLOY_DIR/.env"

    if [[ "$DEPLOY_DIR" == *[[:space:]]* ]]; then
        log_error "Deployment directory must not contain whitespace: $DEPLOY_DIR"
        exit 1
    fi
}

check_binary() {
    local binary="$DEPLOY_DIR/bin/prism"
    if [[ ! -e "$binary" ]]; then
        log_error "Prism binary not found at $binary"
        log_info "Build it first: make build   (this produces bin/prism in the repository root)"
        exit 1
    fi
    if [[ ! -x "$binary" ]]; then
        log_error "Prism binary is not executable: $binary"
        log_info "Fix it with: chmod +x $binary"
        exit 1
    fi
    log_info "Found Prism binary: $binary"
}

check_service_requirements() {
    if [[ "$INSTALL_SERVICE" != true ]]; then
        return 0
    fi
    if ! command -v systemctl >/dev/null 2>&1; then
        log_warn "systemctl not found: skipping systemd installation"
        INSTALL_SERVICE=false
        return 0
    fi
    if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
        log_error "Installing the systemd unit requires root"
        log_info "Re-run with: sudo $0 ${ORIGINAL_ARGS[*]:-}"
        log_info "Or use --no-service to only prepare .env and the data directories."
        exit 1
    fi
}

generate_env_if_missing() {
    if [[ -e "$ENV_FILE" ]]; then
        log_info "Keeping existing $ENV_FILE (never overwritten)"
        return 0
    fi
    if [[ "$DRY_RUN" == true ]]; then
        log_step "[dry-run] would run: $DEPLOY_DIR/bin/prism init --dir $DEPLOY_DIR"
        return 0
    fi

    log_step "Generating $ENV_FILE with fresh tokens (prism init)"
    # `prism init` prints the admin token on stdout. Capture it instead of
    # echoing it: the token stays in the 0600 .env file only.
    if ! init_output="$(cd "$DEPLOY_DIR" && ./bin/prism init --dir "$DEPLOY_DIR" 2>&1)"; then
        log_error "prism init failed"
        printf '%s\n' "$init_output" | sed 's/[A-Za-z0-9]\{32,\}/***REDACTED***/g' >&2
        exit 1
    fi
    unset init_output
    chmod 0600 -- "$ENV_FILE"
    log_info "Wrote $ENV_FILE (mode 0600); the admin and proxy tokens are only stored there"
}

apply_overrides() {
    if [[ -z "$OPT_LISTEN$OPT_PORT$OPT_STATE_DIR$OPT_CACHE_DIR$OPT_LOG_DIR" ]]; then
        return 0
    fi
    if [[ ! -f "$ENV_FILE" ]]; then
        if [[ "$DRY_RUN" == true ]]; then
            log_info "[dry-run] would generate $ENV_FILE first, then set the requested keys"
            return 0
        fi
        log_error "Cannot apply key overrides: $ENV_FILE does not exist"
        exit 1
    fi

    log_step "Applying environment overrides"
    if [[ -n "$OPT_LISTEN" ]]; then
        set_env_key "PRISM_LISTEN_ADDRESS" "$OPT_LISTEN"
    fi
    if [[ -n "$OPT_PORT" ]]; then
        set_env_key "PRISM_PORT" "$OPT_PORT"
    fi
    if [[ -n "$OPT_STATE_DIR" ]]; then
        set_env_key "PRISM_STATE_DIR" "$OPT_STATE_DIR"
    fi
    if [[ -n "$OPT_CACHE_DIR" ]]; then
        set_env_key "PRISM_CACHE_DIR" "$OPT_CACHE_DIR"
    fi
    if [[ -n "$OPT_LOG_DIR" ]]; then
        set_env_key "PRISM_LOG_DIR" "$OPT_LOG_DIR"
    fi
}

check_port_free() {
    local port="$1" rc=0
    if port_in_use "$port"; then
        log_error "Port $port is already in use"
        show_port_owner "$port"
        log_error "Refusing to continue: this script never stops another process"
        log_info "If the Prism service is running, restart it with: systemctl restart $SERVICE_NAME"
        exit 1
    else
        rc=$?
    fi
    if [[ "$rc" -eq 2 ]]; then
        log_warn "Could not determine whether port $port is free (ss, lsof and netstat are missing)"
    else
        log_info "Port $port is free"
    fi
}

resolve_config() {
    local listen port state_dir cache_dir log_dir
    listen="$(env_get "PRISM_LISTEN_ADDRESS")"
    port="$(env_get "PRISM_PORT")"
    state_dir="$(env_get "PRISM_STATE_DIR")"
    cache_dir="$(env_get "PRISM_CACHE_DIR")"
    log_dir="$(env_get "PRISM_LOG_DIR")"

    [[ -n "$listen" ]] || listen="127.0.0.1"
    [[ -n "$port" ]] || port="2260"
    [[ -n "$state_dir" ]] || state_dir="./.local/state"
    [[ -n "$cache_dir" ]] || cache_dir="./.local/cache"
    [[ -n "$log_dir" ]] || log_dir="./.local/logs"

    if [[ ! "$port" =~ ^[0-9]+$ ]] || ((port < 1 || port > 65535)); then
        log_error "Invalid PRISM_PORT in $ENV_FILE: $port (expected 1-65535)"
        exit 1
    fi

    EFFECTIVE_LISTEN="$listen"
    EFFECTIVE_PORT="$port"
    EFFECTIVE_STATE_DIR="$state_dir"
    EFFECTIVE_CACHE_DIR="$cache_dir"
    EFFECTIVE_LOG_DIR="$log_dir"
    STATE_ABS="$(abs_path "$state_dir")"
    CACHE_ABS="$(abs_path "$cache_dir")"
    LOG_ABS="$(abs_path "$log_dir")"
}

create_data_dirs() {
    local dir
    for dir in "$STATE_ABS" "$CACHE_ABS" "$LOG_ABS"; do
        if [[ -d "$dir" ]]; then
            log_info "Using existing directory: $dir"
            continue
        fi
        if [[ "$DRY_RUN" == true ]]; then
            log_step "[dry-run] would create directory: $dir"
            continue
        fi
        log_step "Creating directory: $dir"
        mkdir -p -- "$dir"
    done
}

ensure_service_user() {
    if id -u "$SERVICE_USER" >/dev/null 2>&1; then
        log_info "System user '$SERVICE_USER' already exists"
        return 0
    fi
    if [[ "$DRY_RUN" == true ]]; then
        log_step "[dry-run] would run: useradd --system --user-group --home-dir $DEPLOY_DIR --shell /usr/sbin/nologin $SERVICE_USER"
        return 0
    fi

    local shell="/usr/sbin/nologin"
    if [[ ! -x "$shell" ]]; then
        shell="/sbin/nologin"
    fi
    if [[ ! -x "$shell" ]]; then
        shell="/bin/false"
    fi

    log_step "Creating system user '$SERVICE_USER' (useradd --system)"
    useradd --system --user-group --home-dir "$DEPLOY_DIR" --shell "$shell" "$SERVICE_USER"
}

render_unit() {
    cat <<EOF
[Unit]
Description=Prism proxy and control plane
Documentation=https://github.com/mycatxl/Prism
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
Group=$SERVICE_USER
WorkingDirectory=$DEPLOY_DIR
ExecStart=$DEPLOY_DIR/bin/prism run
Restart=on-failure
RestartSec=5
SyslogIdentifier=$SERVICE_NAME
StandardOutput=journal
StandardError=journal

# Hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=true
ReadWritePaths=$STATE_ABS $CACHE_ABS $LOG_ABS

[Install]
WantedBy=multi-user.target
EOF
}

install_service() {
    if [[ "$INSTALL_SERVICE" != true ]]; then
        return 0
    fi

    UNIT_PATH="/etc/systemd/system/$SERVICE_NAME.service"

    log_step "Preparing the systemd unit"
    ensure_service_user

    if [[ "$DRY_RUN" == true ]]; then
        log_step "[dry-run] would write $UNIT_PATH:"
        render_unit
        log_step "[dry-run] would run: systemctl daemon-reload && systemctl enable --now $SERVICE_NAME"
        return 0
    fi

    log_step "Granting '$SERVICE_USER' access to the data directories"
    chown -R "$SERVICE_USER:$SERVICE_USER" -- "$STATE_ABS" "$CACHE_ABS" "$LOG_ABS"
    if [[ -f "$ENV_FILE" ]]; then
        chown "$SERVICE_USER:$SERVICE_USER" -- "$ENV_FILE"
        chmod 0600 -- "$ENV_FILE"
    fi

    local tmp_unit
    tmp_unit="$(mktemp "${TMPDIR:-/tmp}/prism-unit.XXXXXX")"
    render_unit > "$tmp_unit"
    chmod 0644 -- "$tmp_unit"
    if command -v install >/dev/null 2>&1; then
        install -m 0644 -- "$tmp_unit" "$UNIT_PATH"
    else
        cp -f -- "$tmp_unit" "$UNIT_PATH"
        chmod 0644 -- "$UNIT_PATH"
    fi
    rm -f -- "$tmp_unit"
    log_info "Wrote $UNIT_PATH"

    log_step "Reloading systemd and (re)starting $SERVICE_NAME"
    systemctl daemon-reload
    systemctl enable "$SERVICE_NAME" >/dev/null 2>&1 || log_warn "systemctl enable $SERVICE_NAME failed"
    if ! systemctl restart "$SERVICE_NAME"; then
        log_error "Failed to start $SERVICE_NAME"
        log_info "Inspect it with: systemctl status $SERVICE_NAME (logs: journalctl -u $SERVICE_NAME)"
        exit 1
    fi
}

print_summary() {
    local state=""
    echo ""
    echo -e "${GREEN}Prism deployment prepared${NC}"
    echo ""
    echo -e "${BLUE}Configuration:${NC}"
    echo "  File:       $ENV_FILE"
    echo "  Listen:     $EFFECTIVE_LISTEN:$EFFECTIVE_PORT"
    echo "  State dir:  $STATE_ABS"
    echo "  Cache dir:  $CACHE_ABS"
    echo "  Log dir:    $LOG_ABS"
    if [[ -n "$ENV_BACKUP" ]]; then
        echo "  Backup:     $ENV_BACKUP"
    fi
    echo ""

    if [[ "$INSTALL_SERVICE" == true ]]; then
        if [[ "$DRY_RUN" == true ]]; then
            state="not installed (dry-run)"
        else
            state="$(systemctl is-active "$SERVICE_NAME" 2>/dev/null || true)"
        fi
        echo -e "${BLUE}systemd:${NC}"
        echo "  Unit:       $UNIT_PATH"
        echo "  State:      ${state:-unknown}"
        echo "  Logs:       journalctl -u $SERVICE_NAME -f"
        echo "  Restart:    systemctl restart $SERVICE_NAME"
    else
        echo -e "${BLUE}Manual start:${NC}"
        echo "  cd $DEPLOY_DIR && ./bin/prism run"
    fi
    echo ""
    echo -e "${BLUE}Backup:${NC}"
    echo "  $SCRIPT_DIR/prism-backup.sh backup --dir $DEPLOY_DIR"
    echo ""
    echo "Tokens are stored in $ENV_FILE (mode 0600) and are never printed by this script."
    echo ""
}

main() {
    ORIGINAL_ARGS=("$@")
    parse_args "$@"

    log_info "Prism deployment script"
    check_deploy_dir
    check_service_requirements
    check_binary

    if [[ -n "$OPT_PORT" ]]; then
        if [[ ! "$OPT_PORT" =~ ^[0-9]+$ ]] || ((OPT_PORT < 1 || OPT_PORT > 65535)); then
            log_error "--port must be a number between 1 and 65535, got: $OPT_PORT"
            exit 1
        fi
    fi

    generate_env_if_missing
    resolve_config

    # The port is checked before .env is touched: an occupied port is a hard
    # error, and this script never stops the process that holds it.
    check_port_free "${OPT_PORT:-$EFFECTIVE_PORT}"

    apply_overrides
    # Re-read the file: the overrides above are the authoritative values.
    resolve_config

    create_data_dirs
    install_service
    print_summary
}

main "$@"
