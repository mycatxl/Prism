#!/bin/bash

# Prism deployment script
# Deploys Prism with management interface on specified port

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

# Default configuration
PORT="${PORT:-8080}"
STATE_DIR="${STATE_DIR:-$PROJECT_DIR/state}"
LOG_LEVEL="${LOG_LEVEL:-info}"
ADMIN_TOKEN="${ADMIN_TOKEN:-}"
DNS_UPSTREAMS="${DNS_UPSTREAMS:-https://1.1.1.1/dns-query,https://8.8.8.8/dns-query}"

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
    echo -e "${RED}[ERROR]${NC} $1"
}

log_step() {
    echo -e "${BLUE}[STEP]${NC} $1"
}

check_binary() {
    if [[ ! -f "$PROJECT_DIR/prism" ]]; then
        log_error "Prism binary not found at $PROJECT_DIR/prism"
        log_info "Please build it first: cd $PROJECT_DIR && go build -buildvcs=false ./cmd/prism"
        exit 1
    fi
    log_info "Found Prism binary"
}

check_port() {
    if lsof -Pi :$PORT -sTCP:LISTEN -t >/dev/null 2>&1; then
        log_warn "Port $PORT is already in use"
        read -p "Stop the existing service and continue? (yes/no): " confirm
        if [[ "$confirm" == "yes" ]]; then
            local pid=$(lsof -Pi :$PORT -sTCP:LISTEN -t)
            log_info "Stopping process $pid on port $PORT"
            kill $pid || true
            sleep 2
        else
            log_error "Cannot proceed with port $PORT in use"
            exit 1
        fi
    fi
}

setup_state_dir() {
    if [[ ! -d "$STATE_DIR" ]]; then
        log_step "Creating state directory: $STATE_DIR"
        mkdir -p "$STATE_DIR"
    else
        log_info "Using existing state directory: $STATE_DIR"
    fi
}

generate_admin_token() {
    if [[ -z "$ADMIN_TOKEN" ]]; then
        ADMIN_TOKEN=$(openssl rand -hex 32 2>/dev/null || cat /dev/urandom | tr -dc 'a-zA-Z0-9' | fold -w 64 | head -n 1)
        log_step "Generated admin token: $ADMIN_TOKEN"
        echo "$ADMIN_TOKEN" > "$STATE_DIR/.admin_token"
        chmod 600 "$STATE_DIR/.admin_token"
    else
        log_info "Using provided admin token"
    fi
}

create_systemd_service() {
    log_step "Creating systemd service file..."

    local service_file="/etc/systemd/system/prism.service"

    sudo tee "$service_file" > /dev/null <<EOF
[Unit]
Description=Prism Proxy Service
After=network.target
Documentation=https://github.com/yourusername/Prism

[Service]
Type=simple
User=$USER
WorkingDirectory=$PROJECT_DIR
ExecStart=$PROJECT_DIR/prism standalone
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal
SyslogIdentifier=prism

# Security
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=$STATE_DIR

[Install]
WantedBy=multi-user.target
EOF

    sudo systemctl daemon-reload
    log_info "Systemd service created at $service_file"
}

create_env_file() {
    log_step "Creating environment file..."

    cat > "$PROJECT_DIR/.env" <<EOF
# Prism Configuration
PORT=$PORT
STATE_DIR=$STATE_DIR
LOG_LEVEL=$LOG_LEVEL
ADMIN_TOKEN=$ADMIN_TOKEN
DNS_UPSTREAMS=$DNS_UPSTREAMS
EOF

    chmod 600 "$PROJECT_DIR/.env"
    log_info "Environment file created at $PROJECT_DIR/.env"
}

start_service() {
    local use_systemd=false

    if command -v systemctl >/dev/null 2>&1; then
        read -p "Install as systemd service? (yes/no): " confirm
        if [[ "$confirm" == "yes" ]]; then
            use_systemd=true
        fi
    fi

    if [[ "$use_systemd" == true ]]; then
        create_systemd_service
        log_step "Starting Prism service..."
        sudo systemctl enable prism
        sudo systemctl start prism
        sleep 2
        sudo systemctl status prism --no-pager || true
    else
        log_step "Starting Prism in foreground..."
        log_info "Press Ctrl+C to stop"
        exec "$PROJECT_DIR/prism" standalone
    fi
}

print_success() {
    echo ""
    echo -e "${GREEN}╔════════════════════════════════════════════════════════╗${NC}"
    echo -e "${GREEN}║         Prism Deployed Successfully! 🚀               ║${NC}"
    echo -e "${GREEN}╚════════════════════════════════════════════════════════╝${NC}"
    echo ""
    echo -e "${BLUE}Management Interface:${NC}"
    echo -e "  URL:    http://localhost:$PORT/ui/"
    echo -e "  Token:  $ADMIN_TOKEN"
    echo ""
    echo -e "${BLUE}API Endpoint:${NC}"
    echo -e "  URL:    http://localhost:$PORT/api/v1/"
    echo ""
    echo -e "${BLUE}State Directory:${NC}"
    echo -e "  Path:   $STATE_DIR"
    echo ""
    echo -e "${BLUE}Quick Commands:${NC}"
    echo -e "  Status:  sudo systemctl status prism"
    echo -e "  Logs:    sudo journalctl -u prism -f"
    echo -e "  Stop:    sudo systemctl stop prism"
    echo -e "  Restart: sudo systemctl restart prism"
    echo ""
    echo -e "${BLUE}Backup:${NC}"
    echo -e "  $PROJECT_DIR/scripts/prism-backup.sh backup -d $STATE_DIR -o $PROJECT_DIR/backups -c"
    echo ""
}

main() {
    log_info "Prism Deployment Script"
    log_info "Port: $PORT"
    log_info "State Directory: $STATE_DIR"
    echo ""

    check_binary
    check_port
    setup_state_dir
    generate_admin_token
    create_env_file

    print_success

    start_service
}

# Handle script arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --port)
            PORT="$2"
            shift 2
            ;;
        --state-dir)
            STATE_DIR="$2"
            shift 2
            ;;
        --admin-token)
            ADMIN_TOKEN="$2"
            shift 2
            ;;
        --log-level)
            LOG_LEVEL="$2"
            shift 2
            ;;
        --dns-upstreams)
            DNS_UPSTREAMS="$2"
            shift 2
            ;;
        -h|--help)
            cat <<EOF
Usage: $0 [options]

Options:
  --port <port>              Management port (default: 8080)
  --state-dir <path>         State directory (default: ./state)
  --admin-token <token>      Admin token (default: auto-generated)
  --log-level <level>        Log level (default: info)
  --dns-upstreams <urls>     DNS upstreams (comma-separated)
  -h, --help                 Show this help message

Environment Variables:
  PORT                       Same as --port
  STATE_DIR                  Same as --state-dir
  ADMIN_TOKEN                Same as --admin-token
  LOG_LEVEL                  Same as --log-level
  DNS_UPSTREAMS              Same as --dns-upstreams

Example:
  $0 --port 8080 --state-dir /opt/prism/state
  PORT=8080 STATE_DIR=/opt/prism/state $0

EOF
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            exit 1
            ;;
    esac
done

main
