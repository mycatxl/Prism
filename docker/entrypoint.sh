#!/bin/sh
set -eu

cache_dir="${PRISM_CACHE_DIR:-/var/cache/prism}"
state_dir="${PRISM_STATE_DIR:-/var/lib/prism}"
log_dir="${PRISM_LOG_DIR:-/var/log/prism}"

# Export the resolved directories. Without this the exec'd Prism process sees no
# PRISM_*_DIR at all and falls back to its own defaults (./.local/state,
# ./.local/cache, ./.local/logs). The runtime stage sets no WORKDIR, so those
# defaults resolve against "/" — outside the declared volumes and not writable by
# the unprivileged prism user. The volumes below would silently stay empty.
export PRISM_CACHE_DIR="$cache_dir"
export PRISM_STATE_DIR="$state_dir"
export PRISM_LOG_DIR="$log_dir"

if [ "$#" -eq 0 ]; then
  set -- /usr/local/bin/prism
fi

require_writable_dir() {
  dir="$1"
  label="$2"

  if [ ! -d "$dir" ]; then
    mkdir -p "$dir"
  fi
  if [ ! -w "$dir" ]; then
    cat >&2 <<EOF
fatal: ${label} directory is not writable: ${dir}
hint: mount it with write permission, or use Docker named volumes.
EOF
    exit 1
  fi
}

if [ "$(id -u)" -eq 0 ]; then
  mkdir -p "$cache_dir" "$state_dir" "$log_dir"
  if [ "${PRISM_SKIP_CHOWN:-0}" != "1" ]; then
    chown -R prism:prism "$cache_dir" "$state_dir" "$log_dir"
  fi
  exec su-exec prism:prism "$@"
fi

require_writable_dir "$cache_dir" "cache"
require_writable_dir "$state_dir" "state"
require_writable_dir "$log_dir" "log"

exec "$@"
