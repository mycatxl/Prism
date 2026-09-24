#!/usr/bin/env bash
#
# Prism backup and restore wrapper (WP04 §4.11).
#
# Delegates to `prism backup` and `prism restore`:
#   * `prism backup` snapshots state.db, cache.db and intel.db with
#     VACUUM INTO through read-only connections, so a running service is safe,
#   * `prism restore` verifies the manifest and every sha256 before it touches
#     the live databases, and refuses to run while Prism is active.
#
# The previous tar-based copy of a live SQLite database is gone: it could
# capture a torn WAL state and it never knew what a consistent snapshot is.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_DEPLOY_DIR="$(dirname "$SCRIPT_DIR")"

DEPLOY_DIR="$DEFAULT_DEPLOY_DIR"
OUT_DIR=""
KEEP="10"
FROM_DIR=""
FORCE=false
DRY_RUN=false

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
Prism backup and restore wrapper

Usage: $(basename "$0") <command> [options]

Commands:
  backup     Create a backup in <out-dir>/<timestamp> (default: backups/)
  restore    Restore a backup created by "backup" (requires --from)
  list       List the backups in <out-dir>
  clean      Remove old backups from <out-dir> (per --keep)

Options:
  --dir <path>         Deployment directory with .env (default: $DEFAULT_DEPLOY_DIR)
  -o, --out <path>     Backup directory holding one <timestamp> folder per run
                       (default: <deploy-dir>/backups)
  -k, --keep <n>       Keep the newest n backups (default: 10, 0 = keep all)
  -b, --from <path>    Backup folder to restore from (command: restore)
  -f, --force          Replace existing databases with the backup (command: restore)
  --dry-run            Print the underlying command without running it
  -h, --help           Show this help message

Backups contain state.db, cache.db and intel.db plus a manifest.json with the
sha256 of every file. .env and other files are never included.

Examples:
  ./scripts/prism-backup.sh backup
  ./scripts/prism-backup.sh backup --out /srv/prism-backups --keep 7
  ./scripts/prism-backup.sh list --out /srv/prism-backups
  sudo systemctl stop prism
  ./scripts/prism-backup.sh restore --from /srv/prism-backups/20260924T101500Z --force
  sudo systemctl start prism
EOF
}

# --- helpers ------------------------------------------------------------------

require_value() {
    local flag="$1"
    shift
    if [[ $# -eq 0 || -z "$1" ]]; then
        log_error "$flag requires a value"
        exit 1
    fi
    printf '%s' "$1"
}

parse_options() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --dir)
                DEPLOY_DIR="$(require_value "$1" "${2:-}")"
                shift 2
                ;;
            -o|--out|--output)
                OUT_DIR="$(require_value "$1" "${2:-}")"
                shift 2
                ;;
            -k|--keep)
                KEEP="$(require_value "$1" "${2:-}")"
                shift 2
                ;;
            -b|--from|--backup)
                FROM_DIR="$(require_value "$1" "${2:-}")"
                shift 2
                ;;
            -f|--force)
                FORCE=true
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

    if [[ ! "$KEEP" =~ ^[0-9]+$ ]]; then
        log_error "--keep must be a non-negative integer, got: $KEEP"
        exit 1
    fi
}

# abs_path resolves a path against the deployment directory.
abs_path() {
    local path="$1"
    if [[ "$path" == /* ]]; then
        printf '%s' "$path"
        return 0
    fi
    path="${path#./}"
    printf '%s/%s' "${DEPLOY_DIR%/}" "$path"
}

resolve_paths() {
    if [[ ! -d "$DEPLOY_DIR" ]]; then
        log_error "Deployment directory does not exist: $DEPLOY_DIR"
        exit 1
    fi
    DEPLOY_DIR="$(cd "$DEPLOY_DIR" && pwd)"

    if [[ -z "$OUT_DIR" ]]; then
        OUT_DIR="$DEPLOY_DIR/backups"
    else
        OUT_DIR="$(abs_path "$OUT_DIR")"
    fi
    if [[ -n "$FROM_DIR" ]]; then
        FROM_DIR="$(abs_path "$FROM_DIR")"
    fi
}

require_binary() {
    local binary="$DEPLOY_DIR/bin/prism"
    if [[ ! -x "$binary" ]]; then
        log_error "Prism binary not found or not executable: $binary"
        log_info "Build it first: make build   (this produces bin/prism in the repository root)"
        exit 1
    fi
}

require_env_file() {
    if [[ ! -f "$DEPLOY_DIR/.env" ]]; then
        log_error "No .env in $DEPLOY_DIR: the state and cache directories are unknown"
        log_info "Create one with: $DEPLOY_DIR/scripts/deploy.sh   (or: cd $DEPLOY_DIR && ./bin/prism init)"
        exit 1
    fi
}

# run_prism runs the binary from the deployment directory so that ./.env is
# loaded exactly like the service does.
run_prism() {
    ( cd "$DEPLOY_DIR" && exec ./bin/prism "$@" )
}

# list_backup_dirs prints the timestamped backup directories, newest first.
list_backup_dirs() {
    if [[ ! -d "$OUT_DIR" ]]; then
        return 0
    fi
    find "$OUT_DIR" -mindepth 1 -maxdepth 1 -type d -name '[0-9]*T*Z' -print |
        LC_ALL=C sort -r
}

prune_backups() {
    if (( KEEP < 1 )); then
        log_info "Keeping every backup (--keep 0)"
        return 0
    fi

    local -a entries=()
    local entry
    while IFS= read -r entry; do
        if [[ -n "$entry" ]]; then
            entries+=("$entry")
        fi
    done < <(list_backup_dirs)

    local total="${#entries[@]}"
    if (( total <= KEEP )); then
        log_info "Keeping all $total backup(s) (--keep $KEEP)"
        return 0
    fi

    local i path
    for (( i = KEEP; i < total; i++ )); do
        path="${entries[i]}"
        case "$path" in
            "$OUT_DIR"/*) ;;
            *)
                log_warn "Refusing to remove path outside $OUT_DIR: $path"
                continue
                ;;
        esac
        if [[ "$DRY_RUN" == true ]]; then
            log_info "[dry-run] would remove old backup: $path"
            continue
        fi
        log_info "Removing old backup: $path"
        rm -rf -- "$path"
    done
}

# --- commands -----------------------------------------------------------------

do_backup() {
    require_binary
    require_env_file

    local stamp target
    stamp="$(date -u +%Y%m%dT%H%M%SZ)"
    target="$OUT_DIR/$stamp"

    if [[ -e "$target" ]]; then
        log_error "Backup target already exists: $target"
        exit 1
    fi

    log_step "Creating backup: $target"
    if [[ "$DRY_RUN" == true ]]; then
        log_info "[dry-run] would run: (cd $DEPLOY_DIR && ./bin/prism backup --out $target)"
        return 0
    fi

    run_prism backup --out "$target"
    log_info "Backup written to $target"

    prune_backups
}

do_restore() {
    require_binary
    require_env_file

    if [[ -z "$FROM_DIR" ]]; then
        log_error "restore requires --from <backup-directory>"
        usage >&2
        exit 1
    fi
    if [[ ! -d "$FROM_DIR" ]]; then
        log_error "Backup directory does not exist: $FROM_DIR"
        exit 1
    fi
    if [[ ! -f "$FROM_DIR/manifest.json" ]]; then
        log_error "$FROM_DIR/manifest.json is missing; that is not a backup created by 'prism backup'"
        exit 1
    fi

    log_warn "'prism restore' refuses to run while a Prism instance is active: stop the service first"
    log_info "Restoring from $FROM_DIR (existing databases are moved to *.pre-restore-<timestamp>)"

    local -a args=(restore --from "$FROM_DIR")
    if [[ "$FORCE" == true ]]; then
        args+=(--force)
    fi

    if [[ "$DRY_RUN" == true ]]; then
        log_info "[dry-run] would run: (cd $DEPLOY_DIR && ./bin/prism ${args[*]})"
        return 0
    fi

    run_prism "${args[@]}"
    log_info "Restore complete"
}

do_list() {
    if [[ ! -d "$OUT_DIR" ]]; then
        log_warn "Backup directory does not exist: $OUT_DIR"
        return 0
    fi

    log_info "Backups in $OUT_DIR:"
    local count=0 entry size created
    while IFS= read -r entry; do
        if [[ -z "$entry" ]]; then
            continue
        fi
        count=$((count + 1))
        size="$(du -sh -- "$entry" 2>/dev/null | cut -f1 || true)"
        created=""
        if [[ -f "$entry/manifest.json" ]]; then
            created="$(grep -m1 -o '"created_at": *"[^"]*"' "$entry/manifest.json" 2>/dev/null | sed 's/.*"\([^"]*\)"$/\1/' || true)"
        fi
        echo "  $((count)). $(basename "$entry")  (${size:-?}${created:+, created $created})"
    done < <(list_backup_dirs)

    if (( count == 0 )); then
        log_warn "No backups found"
    else
        log_info "Total backups: $count"
    fi
}

do_clean() {
    if [[ ! -d "$OUT_DIR" ]]; then
        log_warn "Backup directory does not exist: $OUT_DIR"
        return 0
    fi
    log_step "Cleaning $OUT_DIR (keeping the newest $KEEP)"
    prune_backups
    log_info "Cleanup complete"
}

main() {
    local command="${1:-}"

    case "$command" in
        backup|restore|list|clean)
            shift
            ;;
        -h|--help|help)
            usage
            exit 0
            ;;
        "")
            usage >&2
            exit 1
            ;;
        *)
            log_error "Unknown command: $command"
            usage >&2
            exit 1
            ;;
    esac

    parse_options "$@"
    resolve_paths

    case "$command" in
        backup) do_backup ;;
        restore) do_restore ;;
        list) do_list ;;
        clean) do_clean ;;
    esac
}

main "$@"
