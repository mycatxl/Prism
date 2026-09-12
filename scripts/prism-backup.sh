#!/bin/bash

# Prism Backup and Restore Tool
# Backs up and restores Prism state and configuration

set -euo pipefail

VERSION="1.0.0"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

usage() {
    cat <<EOF
Prism Backup and Restore Tool v${VERSION}

Usage: $0 <command> [options]

Commands:
    backup      Create a backup of Prism state
    restore     Restore Prism state from a backup
    list        List available backups
    clean       Remove old backups

Backup Options:
    -d, --dir <path>        State directory (default: ./state)
    -o, --output <path>     Output directory for backup (default: ./backups)
    -n, --name <name>       Backup name (default: timestamp)
    -c, --compress          Compress backup with gzip

Restore Options:
    -b, --backup <path>     Backup file to restore
    -d, --dir <path>        State directory to restore to (default: ./state)
    -f, --force             Force restore without confirmation

List Options:
    -o, --output <path>     Backup directory (default: ./backups)

Clean Options:
    -o, --output <path>     Backup directory (default: ./backups)
    -k, --keep <number>     Number of backups to keep (default: 10)

Examples:
    $0 backup -d ./state -o ./backups
    $0 backup -d ./state -o ./backups -n my-backup -c
    $0 restore -b ./backups/backup-20260912.tar.gz -d ./state
    $0 list -o ./backups
    $0 clean -o ./backups -k 5

EOF
    exit 1
}

backup_prism() {
    local state_dir="./state"
    local output_dir="./backups"
    local backup_name=""
    local compress=false

    while [[ $# -gt 0 ]]; do
        case $1 in
            -d|--dir)
                state_dir="$2"
                shift 2
                ;;
            -o|--output)
                output_dir="$2"
                shift 2
                ;;
            -n|--name)
                backup_name="$2"
                shift 2
                ;;
            -c|--compress)
                compress=true
                shift
                ;;
            *)
                log_error "Unknown option: $1"
                usage
                ;;
        esac
    done

    if [[ ! -d "$state_dir" ]]; then
        log_error "State directory does not exist: $state_dir"
        exit 1
    fi

    mkdir -p "$output_dir"

    if [[ -z "$backup_name" ]]; then
        backup_name="backup-$(date +%Y%m%d-%H%M%S)"
    fi

    local backup_path="$output_dir/$backup_name.tar"
    if [[ "$compress" == true ]]; then
        backup_path="$backup_path.gz"
    fi

    log_info "Creating backup of $state_dir..."
    log_info "Backup will be saved to: $backup_path"

    if [[ "$compress" == true ]]; then
        tar -czf "$backup_path" -C "$(dirname "$state_dir")" "$(basename "$state_dir")"
    else
        tar -cf "$backup_path" -C "$(dirname "$state_dir")" "$(basename "$state_dir")"
    fi

    local backup_size=$(du -h "$backup_path" | cut -f1)
    log_info "Backup created successfully: $backup_path ($backup_size)"

    # Create metadata file
    cat > "$backup_path.meta" <<META
{
  "backup_name": "$backup_name",
  "timestamp": "$(date -Iseconds)",
  "state_dir": "$state_dir",
  "compressed": $compress,
  "size": "$backup_size"
}
META

    log_info "Backup complete!"
}

restore_prism() {
    local backup_path=""
    local state_dir="./state"
    local force=false

    while [[ $# -gt 0 ]]; do
        case $1 in
            -b|--backup)
                backup_path="$2"
                shift 2
                ;;
            -d|--dir)
                state_dir="$2"
                shift 2
                ;;
            -f|--force)
                force=true
                shift
                ;;
            *)
                log_error "Unknown option: $1"
                usage
                ;;
        esac
    done

    if [[ -z "$backup_path" ]]; then
        log_error "Backup path required (-b/--backup)"
        usage
    fi

    if [[ ! -f "$backup_path" ]]; then
        log_error "Backup file does not exist: $backup_path"
        exit 1
    fi

    if [[ -d "$state_dir" ]] && [[ "$force" != true ]]; then
        log_warn "State directory already exists: $state_dir"
        read -p "This will overwrite existing state. Continue? (yes/no): " confirm
        if [[ "$confirm" != "yes" ]]; then
            log_info "Restore cancelled"
            exit 0
        fi
    fi

    # Backup existing state if it exists
    if [[ -d "$state_dir" ]]; then
        local backup_existing="$state_dir.backup-$(date +%Y%m%d-%H%M%S)"
        log_info "Backing up existing state to: $backup_existing"
        mv "$state_dir" "$backup_existing"
    fi

    log_info "Restoring from: $backup_path"
    log_info "Restoring to: $state_dir"

    mkdir -p "$(dirname "$state_dir")"

    if [[ "$backup_path" == *.gz ]]; then
        tar -xzf "$backup_path" -C "$(dirname "$state_dir")"
    else
        tar -xf "$backup_path" -C "$(dirname "$state_dir")"
    fi

    log_info "Restore complete!"
    log_info "State directory: $state_dir"
}

list_backups() {
    local output_dir="./backups"

    while [[ $# -gt 0 ]]; do
        case $1 in
            -o|--output)
                output_dir="$2"
                shift 2
                ;;
            *)
                log_error "Unknown option: $1"
                usage
                ;;
        esac
    done

    if [[ ! -d "$output_dir" ]]; then
        log_warn "Backup directory does not exist: $output_dir"
        exit 0
    fi

    log_info "Backups in $output_dir:"
    echo ""

    local count=0
    for backup in "$output_dir"/*.tar "$output_dir"/*.tar.gz; do
        if [[ -f "$backup" ]]; then
            count=$((count + 1))
            local size=$(du -h "$backup" | cut -f1)
            local date=$(stat -c %y "$backup" | cut -d' ' -f1,2 | cut -d'.' -f1)
            echo "  [$count] $(basename "$backup")"
            echo "      Size: $size"
            echo "      Date: $date"

            if [[ -f "$backup.meta" ]]; then
                echo "      Meta: $(cat "$backup.meta" | tr '\n' ' ')"
            fi
            echo ""
        fi
    done

    if [[ $count -eq 0 ]]; then
        log_warn "No backups found"
    else
        log_info "Total backups: $count"
    fi
}

clean_backups() {
    local output_dir="./backups"
    local keep=10

    while [[ $# -gt 0 ]]; do
        case $1 in
            -o|--output)
                output_dir="$2"
                shift 2
                ;;
            -k|--keep)
                keep="$2"
                shift 2
                ;;
            *)
                log_error "Unknown option: $1"
                usage
                ;;
        esac
    done

    if [[ ! -d "$output_dir" ]]; then
        log_warn "Backup directory does not exist: $output_dir"
        exit 0
    fi

    log_info "Cleaning backups in $output_dir (keeping $keep most recent)..."

    # Count backups
    local count=$(find "$output_dir" -name "*.tar" -o -name "*.tar.gz" | wc -l)
    if [[ $count -le $keep ]]; then
        log_info "No cleanup needed (found $count backups, keeping $keep)"
        exit 0
    fi

    # Remove old backups
    local to_remove=$((count - keep))
    log_info "Removing $to_remove old backup(s)..."

    find "$output_dir" \( -name "*.tar" -o -name "*.tar.gz" \) -printf '%T+ %p\n' | \
        sort | \
        head -n "$to_remove" | \
        cut -d' ' -f2- | \
        while read -r backup; do
            log_info "Removing: $(basename "$backup")"
            rm -f "$backup" "$backup.meta"
        done

    log_info "Cleanup complete!"
}

# Main command dispatcher
if [[ $# -eq 0 ]]; then
    usage
fi

command=$1
shift

case $command in
    backup)
        backup_prism "$@"
        ;;
    restore)
        restore_prism "$@"
        ;;
    list)
        list_backups "$@"
        ;;
    clean)
        clean_backups "$@"
        ;;
    -h|--help|help)
        usage
        ;;
    *)
        log_error "Unknown command: $command"
        usage
        ;;
esac
