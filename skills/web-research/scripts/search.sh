#!/usr/bin/env bash
set -euo pipefail

query="${1:-}"
if [[ -z "$query" ]]; then
  echo "usage: bash /skills/web-research/scripts/search.sh \"query\"" >&2
  exit 2
fi

cat <<EOF
research_query: ${query}

source_plan:
- Find the primary source or official documentation first.
- Cross-check with at least one independent recent source when the topic is time-sensitive.
- Record publication dates and distinguish facts from inference.

report_template:
1. Short answer
2. Evidence with source names and dates
3. Caveats or open questions
EOF

