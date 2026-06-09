#!/usr/bin/env bash
set -euo pipefail

pattern="${1:-}"
root="${2:-/workspace}"
if [[ -z "$pattern" ]]; then
  echo "usage: bash /skills/repo-inspect/scripts/find-text.sh \"pattern\" [root]" >&2
  exit 2
fi

grep -RIn --exclude-dir=.git --exclude-dir=target -- "$pattern" "$root" 2>/dev/null | head -n 80 || true

