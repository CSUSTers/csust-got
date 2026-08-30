#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
MATCHER="$SCRIPT_DIR/nft-agent-fetch-reject.awk"
FIXTURES="$SCRIPT_DIR/testdata/agent-runtime-nft-reject"
failures=0

check_match() {
  description=$1
  chain=$2
  prefix=$3
  fixture=$4
  if awk -v chain="$chain" -v prefix="$prefix" -f "$MATCHER" "$fixture"; then
    printf 'ok - %s\n' "$description"
  else
    printf 'not ok - %s\n' "$description" >&2
    failures=$((failures + 1))
  fi
}

check_no_match() {
  description=$1
  chain=$2
  prefix=$3
  fixture=$4
  if awk -v chain="$chain" -v prefix="$prefix" -f "$MATCHER" "$fixture"; then
    printf 'not ok - %s\n' "$description" >&2
    failures=$((failures + 1))
  else
    printf 'ok - %s\n' "$description"
  fi
}

input_prefix='iifname "br-agent-fetch"'
deny4_prefix='iifname "br-agent-fetch" ip daddr @deny4'
deny6_prefix='iifname "br-agent-fetch" ip6 daddr @deny6'

check_match 'plain input reject is accepted' input "$input_prefix" "$FIXTURES/plain-reject.nft"
check_match 'normalized IPv4 reject is accepted' forward "$deny4_prefix" "$FIXTURES/normalized-reject.nft"
check_match 'normalized IPv6 reject is accepted' forward "$deny6_prefix" "$FIXTURES/normalized-reject.nft"
check_no_match 'accept is not a reject verdict' input "$input_prefix" "$FIXTURES/invalid-rejects.nft"
check_no_match 'drop is not a reject verdict' input "$input_prefix" "$FIXTURES/invalid-rejects.nft"
check_no_match 'wrong input interface is not accepted' input "$input_prefix" "$FIXTURES/wrong-interface.nft"
check_no_match 'wrong IPv4 deny set is not accepted' forward "$deny4_prefix" "$FIXTURES/wrong-prefix.nft"
check_no_match 'reject after an extra expression is not accepted' forward "$deny6_prefix" "$FIXTURES/wrong-prefix.nft"

if [ "$failures" -ne 0 ]; then
  exit 1
fi

printf 'PASS: nft reject matcher accepts only the required semantic reject rules\n'
