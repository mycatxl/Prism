#!/usr/bin/env bash
#
# Coverage gate.
#
# The repository had no coverage measurement at all before this script: the
# `test` target did not pass -coverprofile and nothing consumed one. The gate
# therefore does two things:
#
#   1. A repository-wide floor, so a change that deletes tests cannot pass.
#   2. A per-package floor for the packages this project treats as the
#      correctness-critical core.
#
# Both numbers are set from a measured baseline rather than picked by feel:
# run `make coverage` to print the current figures. Keep the floor a few points
# below the baseline so ordinary churn does not fail the build, but high enough
# that losing a package's tests does.
#
# Usage: scripts/coverage-gate.sh <coverprofile> [min-total] [min-package]
set -euo pipefail

profile="${1:-}"
min_total="${2:-70}"
min_package="${3:-70}"

if [ -z "$profile" ]; then
  echo "usage: $0 <coverprofile> [min-total-percent] [min-package-percent]" >&2
  exit 2
fi
if [ ! -f "$profile" ]; then
  echo "coverage profile not found: $profile" >&2
  exit 2
fi

# Packages held to the per-package floor: the correctness-critical core this
# project is built around (multi-tenant state, credentials, quota accounting).
gated_packages="prism/internal/state prism/internal/keys prism/internal/usage prism/internal/auth prism/internal/model"

# Total, straight from the tool that owns the format.
total="$(go tool cover -func="$profile" | awk '/^total:/ {gsub(/%/, "", $NF); print $NF}')"
if [ -z "$total" ]; then
  echo "could not read the total from $profile" >&2
  exit 2
fi

awk -v t="$total" -v min="$min_total" 'BEGIN { exit !(t >= min) }' || {
  echo "FAIL total coverage: ${total}% < ${min_total}%"
  exit 1
}
echo "ok   total coverage: ${total}% (floor ${min_total}%)"

# Per-package figures come from the profile itself: sum the statement counts per
# package. go tool cover does not aggregate per package, and re-running the
# tests per package would double the runtime.
per_pkg="$(awk '
  NR == 1 { next }
  {
    file = $1
    sub(/:.*$/, "", file)
    slash = file
    sub(/\/[^\/]*$/, "", slash)
    total[slash] += $2
    if ($3 > 0) covered[slash] += $2
  }
  END {
    for (pkg in total) {
      if (total[pkg] > 0) printf "%s %.2f\n", pkg, 100 * covered[pkg] / total[pkg]
    }
  }
' "$profile")"

status=0
for pkg in $gated_packages; do
  line="$(printf '%s\n' "$per_pkg" | awk -v p="$pkg" '$1 == p')"
  if [ -z "$line" ]; then
    echo "FAIL ${pkg}: no coverage data in the profile (tests missing or package moved)"
    status=1
    continue
  fi
  pct="$(printf '%s\n' "$line" | awk '{print $2}')"
  if awk -v p="$pct" -v min="$min_package" 'BEGIN { exit !(p >= min) }'; then
    echo "ok   ${pkg}: ${pct}% (floor ${min_package}%)"
  else
    echo "FAIL ${pkg}: ${pct}% < ${min_package}%"
    status=1
  fi
done

exit "$status"
