#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
NFT_REJECT_MATCHER="$SCRIPT_DIR/nft-agent-fetch-reject.awk"

fail() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

require_env() {
  name=$1
  eval "value=\${$name-}"
  [ -n "$value" ] || fail "$name must be set and nonblank"
  case "$value" in
    *"
"*) fail "$name must not contain a newline" ;;
  esac
}

for name in \
  AGENT_RUNTIME_CGROUP_PARENT \
  AGENT_RUNTIME_CGROUP_HOST_ROOT \
  AGENT_RUNTIME_WORKSPACE_HOST_ROOT \
  AGENT_RUNTIME_LOG_HOST_ROOT \
  AGENT_FETCH_AUDIT_HOST_ROOT \
  AGENT_FETCH_HMAC_SECRET_FILE \
  AGENT_RUNTIME_WORKSPACE_MAX_BYTES \
  AGENT_RUNTIME_WORKSPACE_FS_MAX_BYTES \
  AGENT_RUNTIME_LOG_FS_MAX_BYTES \
  AGENT_FETCH_AUDIT_FS_MAX_BYTES \
  AGENT_FETCH_DNS_SERVERS \
  AGENT_FETCH_EXTRA_DENY_CIDRS; do
  require_env "$name"
done

for tool in awk cat df find grep id mktemp mountpoint nft realpath rm rmdir sed setpriv stat tr uname; do
  command -v "$tool" >/dev/null 2>&1 || fail "required tool not found: $tool"
done
[ -x /usr/bin/test ] || fail 'required executable not found: /usr/bin/test'

[ "$(uname -s)" = Linux ] || fail 'host validation requires Linux'
[ "$(id -u)" -eq 0 ] || fail 'run this read-only validator as root to test UID 10001/10002 access and nftables'
[ "$(stat -fc %T /sys/fs/cgroup)" = cgroup2fs ] || fail '/sys/fs/cgroup is not a cgroup v2 filesystem'

positive_integer() {
  name=$1
  eval "value=\${$name}"
  case "$value" in
    *[!0-9]*|'') fail "$name must be a positive integer" ;;
  esac
  [ "$value" -gt 0 ] || fail "$name must be greater than zero"
}

for name in \
  AGENT_RUNTIME_WORKSPACE_MAX_BYTES \
  AGENT_RUNTIME_WORKSPACE_FS_MAX_BYTES \
  AGENT_RUNTIME_LOG_FS_MAX_BYTES \
  AGENT_FETCH_AUDIT_FS_MAX_BYTES; do
  positive_integer "$name"
done

[ "$AGENT_RUNTIME_WORKSPACE_MAX_BYTES" -le "$AGENT_RUNTIME_WORKSPACE_FS_MAX_BYTES" ] ||
  fail 'AGENT_RUNTIME_WORKSPACE_MAX_BYTES must not exceed AGENT_RUNTIME_WORKSPACE_FS_MAX_BYTES'

case "$AGENT_RUNTIME_CGROUP_PARENT" in
  /*|.|..|*/../*|../*|*/..|*/./*|./*|*/.|*//* )
    fail 'AGENT_RUNTIME_CGROUP_PARENT must be a normalized relative cgroup path'
    ;;
esac

resolve_directory() {
  name=$1
  eval "path=\${$name}"
  case "$path" in
    /*) ;;
    *) fail "$name must be an absolute path" ;;
  esac
  resolved=$(realpath -e -- "$path") || fail "$name does not resolve: $path"
  [ -d "$resolved" ] || fail "$name must resolve to a directory: $resolved"
  printf '%s\n' "$resolved"
}

resolve_regular_file() {
  name=$1
  eval "path=\${$name}"
  case "$path" in
    /*) ;;
    *) fail "$name must be an absolute path" ;;
  esac
  [ ! -L "$path" ] || fail "$name must not be a symbolic link"
  resolved=$(realpath -e -- "$path") || fail "$name does not resolve: $path"
  [ -f "$resolved" ] || fail "$name must resolve to a regular file: $resolved"
  printf '%s\n' "$resolved"
}

CGROUP_BASE=$(realpath -e -- /sys/fs/cgroup)
AGGREGATE_CGROUP=$(realpath -e -- "/sys/fs/cgroup/$AGENT_RUNTIME_CGROUP_PARENT") ||
  fail "aggregate cgroup does not exist: /sys/fs/cgroup/$AGENT_RUNTIME_CGROUP_PARENT"
[ -d "$AGGREGATE_CGROUP" ] || fail "aggregate cgroup is not a directory: $AGGREGATE_CGROUP"
case "$AGGREGATE_CGROUP/" in
  "$CGROUP_BASE/"*) ;;
  *) fail 'aggregate cgroup resolves outside /sys/fs/cgroup' ;;
esac
[ "$AGGREGATE_CGROUP" != "$CGROUP_BASE" ] || fail 'aggregate cgroup must not be the cgroup v2 root'
[ "$AGGREGATE_CGROUP" = "$CGROUP_BASE/$AGENT_RUNTIME_CGROUP_PARENT" ] ||
  fail 'AGENT_RUNTIME_CGROUP_PARENT must name the canonical aggregate cgroup without symlink aliases'

COMMANDS_CGROUP=$(resolve_directory AGENT_RUNTIME_CGROUP_HOST_ROOT)
[ "$AGENT_RUNTIME_CGROUP_HOST_ROOT" = "$COMMANDS_CGROUP" ] ||
  fail 'AGENT_RUNTIME_CGROUP_HOST_ROOT must be the canonical commands cgroup path'
[ "$COMMANDS_CGROUP" != "$AGGREGATE_CGROUP" ] || fail 'delegated commands cgroup must not equal the aggregate cgroup'
[ "${COMMANDS_CGROUP##*/}" = commands ] ||
  fail 'AGENT_RUNTIME_CGROUP_HOST_ROOT basename must be commands'
[ "$COMMANDS_CGROUP" = "$AGGREGATE_CGROUP/commands" ] ||
  fail 'AGENT_RUNTIME_CGROUP_HOST_ROOT must be the direct commands child of the aggregate cgroup'
[ -z "$(cat -- "$AGGREGATE_CGROUP/cgroup.procs")" ] ||
  fail 'aggregate cgroup must contain no direct process before Runtime Compose activation'
aggregate_children=$(find "$AGGREGATE_CGROUP" -mindepth 1 -maxdepth 1 -type d -printf '%f\n')
[ "$aggregate_children" = commands ] ||
  fail 'aggregate cgroup must contain exactly the caller-prepared commands child before activation'
[ -z "$(find "$COMMANDS_CGROUP" -mindepth 1 -maxdepth 1 -type d -print -quit)" ] ||
  fail 'delegated commands cgroup must contain no command child before activation'

assert_file_value() {
  path=$1
  expected=$2
  description=$3
  [ -r "$path" ] || fail "$description is not readable: $path"
  actual=$(cat -- "$path")
  [ "$actual" = "$expected" ] || fail "$description must be '$expected', got '$actual'"
}

assert_file_value "$AGGREGATE_CGROUP/pids.max" 512 'aggregate pids.max'
assert_file_value "$AGGREGATE_CGROUP/memory.max" 1073741824 'aggregate memory.max'
assert_file_value "$AGGREGATE_CGROUP/memory.swap.max" 0 'aggregate memory.swap.max'
assert_file_value "$AGGREGATE_CGROUP/cpu.max" '200000 100000' 'aggregate cpu.max'

file_has_word() {
  path=$1
  expected=$2
  awk -v expected="$expected" '
    {
      for (i = 1; i <= NF; i++) {
        value = $i
        sub(/^\+/, "", value)
        if (value == expected) {
          found = 1
        }
      }
    }
    END { exit(found ? 0 : 1) }
  ' "$path"
}

for controller in pids memory cpu; do
  file_has_word "$AGGREGATE_CGROUP/cgroup.controllers" "$controller" ||
    fail "$controller controller is not available at the aggregate cgroup"
  file_has_word "$AGGREGATE_CGROUP/cgroup.subtree_control" "$controller" ||
    fail "$controller controller is not enabled at the aggregate cgroup"
  file_has_word "$COMMANDS_CGROUP/cgroup.controllers" "$controller" ||
    fail "$controller controller is not available at the delegated commands cgroup"
  file_has_word "$COMMANDS_CGROUP/cgroup.subtree_control" "$controller" ||
    fail "$controller controller is not enabled at the delegated commands cgroup"
done

[ "$(stat -c %u -- "$COMMANDS_CGROUP")" = 10001 ] ||
  fail 'delegated commands cgroup owner UID must be 10001'
[ "$(stat -c %u -- "$COMMANDS_CGROUP/cgroup.procs")" = 10001 ] ||
  fail 'delegated commands cgroup.procs owner UID must be 10001'

uid_can_access() {
  uid=$1
  gid=$2
  mode=$3
  path=$4
  setpriv --reuid="$uid" --regid="$gid" --clear-groups /usr/bin/test "$mode" "$path"
}

uid_can_access 10001 10001 -x "$COMMANDS_CGROUP" ||
  fail 'UID 10001 cannot traverse the delegated commands cgroup'
uid_can_access 10001 10001 -w "$COMMANDS_CGROUP" ||
  fail 'UID 10001 cannot create command children in the delegated commands cgroup'
uid_can_access 10001 10001 -w "$COMMANDS_CGROUP/cgroup.procs" ||
  fail 'UID 10001 cannot write the delegated commands cgroup.procs'
uid_can_access 10001 10001 -w "$AGGREGATE_CGROUP/cgroup.procs" ||
  fail 'UID 10001 lacks cgroup.procs permission at the source/target common ancestor'

WORKSPACE_ROOT=$(resolve_directory AGENT_RUNTIME_WORKSPACE_HOST_ROOT)
RUNTIME_LOG_ROOT=$(resolve_directory AGENT_RUNTIME_LOG_HOST_ROOT)
AUDIT_ROOT=$(resolve_directory AGENT_FETCH_AUDIT_HOST_ROOT)

[ "$WORKSPACE_ROOT" != "$RUNTIME_LOG_ROOT" ] || fail 'workspace and Runtime log roots must be different paths'
[ "$WORKSPACE_ROOT" != "$AUDIT_ROOT" ] || fail 'workspace and Broker audit roots must be different paths'
[ "$RUNTIME_LOG_ROOT" != "$AUDIT_ROOT" ] || fail 'Runtime log and Broker audit roots must be different paths'

ROOT_DEVICE=$(stat -c %d -- /)
WORKSPACE_DEVICE=$(stat -c %d -- "$WORKSPACE_ROOT")
RUNTIME_LOG_DEVICE=$(stat -c %d -- "$RUNTIME_LOG_ROOT")
AUDIT_DEVICE=$(stat -c %d -- "$AUDIT_ROOT")

for path in "$WORKSPACE_ROOT" "$RUNTIME_LOG_ROOT" "$AUDIT_ROOT"; do
  mountpoint -q -- "$path" || fail "required bounded filesystem is not a mountpoint: $path"
done

[ "$WORKSPACE_DEVICE" != "$ROOT_DEVICE" ] || fail 'workspace filesystem must use a different device from /'
[ "$RUNTIME_LOG_DEVICE" != "$ROOT_DEVICE" ] || fail 'Runtime log filesystem must use a different device from /'
[ "$AUDIT_DEVICE" != "$ROOT_DEVICE" ] || fail 'Broker audit filesystem must use a different device from /'
[ "$WORKSPACE_DEVICE" != "$RUNTIME_LOG_DEVICE" ] || fail 'workspace and Runtime log filesystems must use different devices'
[ "$WORKSPACE_DEVICE" != "$AUDIT_DEVICE" ] || fail 'workspace and Broker audit filesystems must use different devices'
[ "$RUNTIME_LOG_DEVICE" != "$AUDIT_DEVICE" ] || fail 'Runtime log and Broker audit filesystems must use different devices'

validate_filesystem_ceiling() {
  path=$1
  ceiling=$2
  description=$3
  size=$(df --block-size=1 --output=size -- "$path" | awk 'NR > 1 { print $1; exit }')
  case "$size" in
    *[!0-9]*|'') fail "could not read $description filesystem size" ;;
  esac
  [ "$size" -le "$ceiling" ] ||
    fail "$description filesystem size $size exceeds required ceiling $ceiling"
}

validate_filesystem_ceiling "$WORKSPACE_ROOT" "$AGENT_RUNTIME_WORKSPACE_FS_MAX_BYTES" 'workspace'
validate_filesystem_ceiling "$RUNTIME_LOG_ROOT" "$AGENT_RUNTIME_LOG_FS_MAX_BYTES" 'Runtime log'
validate_filesystem_ceiling "$AUDIT_ROOT" "$AGENT_FETCH_AUDIT_FS_MAX_BYTES" 'Broker audit'

uid_can_access 10001 10001 -x "$WORKSPACE_ROOT" ||
  fail 'workspace root must be searchable by Runtime UID:GID 10001:10001'
uid_can_access 10001 10001 -w "$WORKSPACE_ROOT" ||
  fail 'workspace root must be writable by Runtime UID:GID 10001:10001'
uid_can_access 10001 10001 -x "$RUNTIME_LOG_ROOT" ||
  fail 'Runtime log root must be searchable by Runtime UID:GID 10001:10001'
uid_can_access 10001 10001 -w "$RUNTIME_LOG_ROOT" ||
  fail 'Runtime log root must be writable by Runtime UID:GID 10001:10001'
uid_can_access 10002 10001 -x "$AUDIT_ROOT" ||
  fail 'Broker audit root must be searchable by Broker UID:GID 10002:10001'
uid_can_access 10002 10001 -w "$AUDIT_ROOT" ||
  fail 'Broker audit root must be writable by Broker UID:GID 10002:10001'

SECRET_FILE=$(resolve_regular_file AGENT_FETCH_HMAC_SECRET_FILE)
[ "$(stat -c %u -- "$SECRET_FILE")" = 10001 ] || fail 'HMAC secret owner UID must be 10001'
[ "$(stat -c %g -- "$SECRET_FILE")" = 10001 ] || fail 'HMAC secret owner GID must be 10001'
[ "$(stat -c %a -- "$SECRET_FILE")" = 440 ] || fail 'HMAC secret mode must be exactly 0440'
uid_can_access 10001 10001 -r "$SECRET_FILE" || fail 'Runtime UID 10001 cannot read the HMAC secret'
uid_can_access 10002 10001 -r "$SECRET_FILE" || fail 'Broker UID 10002 cannot read the HMAC secret through GID 10001'

case "$AGENT_FETCH_DNS_SERVERS" in
  *,|,*|*,,*) fail 'AGENT_FETCH_DNS_SERVERS must be a comma-separated list without empty entries' ;;
esac
case "$AGENT_FETCH_EXTRA_DENY_CIDRS" in
  *,|,*|*,,*) fail 'AGENT_FETCH_EXTRA_DENY_CIDRS must be a comma-separated list without empty entries' ;;
esac

TMP_DIR=$(mktemp -d)
cleanup() {
  [ -n "$TMP_DIR" ] || return
  rm -f -- "$TMP_DIR/agent-fetch.table"
  rm -f -- "$TMP_DIR/deny4.set"
  rm -f -- "$TMP_DIR/deny6.set"
  rm -f -- "$TMP_DIR/deny4.tokens"
  rm -f -- "$TMP_DIR/deny6.tokens"
  rm -f -- "$TMP_DIR/extra-cidrs"
  rmdir -- "$TMP_DIR"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

NFT_TABLE="$TMP_DIR/agent-fetch.table"
NFT_DENY4="$TMP_DIR/deny4.set"
NFT_DENY6="$TMP_DIR/deny6.set"
NFT_DENY4_TOKENS="$TMP_DIR/deny4.tokens"
NFT_DENY6_TOKENS="$TMP_DIR/deny6.tokens"

nft list table inet agent_fetch >"$NFT_TABLE" || fail 'nftables table inet agent_fetch is not loaded or cannot be read'
nft list set inet agent_fetch deny4 >"$NFT_DENY4" || fail 'nftables set inet agent_fetch deny4 is not loaded'
nft list set inet agent_fetch deny6 >"$NFT_DENY6" || fail 'nftables set inet agent_fetch deny6 is not loaded'

grep -F -- 'flags interval' "$NFT_DENY4" >/dev/null 2>&1 || fail 'nftables deny4 must be an interval set'
grep -F -- 'flags interval' "$NFT_DENY6" >/dev/null 2>&1 || fail 'nftables deny6 must be an interval set'

chain_has_exact_rule() {
  chain=$1
  expected=$2
  awk -v chain="$chain" -v expected="$expected" '
    {
      line = $0
      sub(/^[[:space:]]*/, "", line)
      sub(/[[:space:]]*$/, "", line)
    }
    line == "chain " chain " {" { in_chain = 1; next }
    in_chain && line == "}" { in_chain = 0 }
    in_chain && line == expected { found = 1 }
    END { exit(found ? 0 : 1) }
  ' "$NFT_TABLE"
}

chain_has_required_reject() {
  chain=$1
  prefix=$2
  awk -v chain="$chain" -v prefix="$prefix" -f "$NFT_REJECT_MATCHER" "$NFT_TABLE"
}

chain_has_exact_rule input 'type filter hook input priority filter - 5; policy accept;' ||
  chain_has_exact_rule input 'type filter hook input priority -5; policy accept;' ||
  fail 'nftables input chain lacks the required hook, priority, or accept policy'
chain_has_required_reject input 'iifname "br-agent-fetch"' ||
  fail 'nftables input chain lacks the unconditional br-agent-fetch host reject'
chain_has_exact_rule forward 'type filter hook forward priority filter - 5; policy accept;' ||
  chain_has_exact_rule forward 'type filter hook forward priority -5; policy accept;' ||
  fail 'nftables forward chain lacks the required hook, priority, or accept policy'
chain_has_required_reject forward 'iifname "br-agent-fetch" ip daddr @deny4' ||
  fail 'nftables forward chain lacks the br-agent-fetch IPv4 destination reject'
chain_has_required_reject forward 'iifname "br-agent-fetch" ip6 daddr @deny6' ||
  fail 'nftables forward chain lacks the br-agent-fetch IPv6 destination reject'

tr ',{}' '\n\n\n' <"$NFT_DENY4" | sed 's/^[[:space:]]*//;s/[[:space:];]*$//' >"$NFT_DENY4_TOKENS"
tr ',{}' '\n\n\n' <"$NFT_DENY6" | sed 's/^[[:space:]]*//;s/[[:space:];]*$//' >"$NFT_DENY6_TOKENS"

nft_set_contains() {
  family=$1
  cidr=$2
  if [ "$family" = 4 ]; then
    tokens=$NFT_DENY4_TOKENS
  else
    tokens=$NFT_DENY6_TOKENS
  fi
  grep -F -x -- "$cidr" "$tokens" >/dev/null 2>&1
}

for cidr in \
  0.0.0.0/8 10.0.0.0/8 100.64.0.0/10 127.0.0.0/8 \
  169.254.0.0/16 172.16.0.0/12 192.0.0.0/24 192.0.2.0/24 \
  192.168.0.0/16 198.18.0.0/15 198.51.100.0/24 \
  203.0.113.0/24 224.0.0.0/4 240.0.0.0/4; do
  nft_set_contains 4 "$cidr" || fail "loaded nftables deny4 is missing required CIDR: $cidr"
done
for cidr in ::/128 ::1/128 ::ffff:0:0/96 100::/64 2001:db8::/32 fc00::/7 fe80::/10 ff00::/8; do
  nft_set_contains 6 "$cidr" || fail "loaded nftables deny6 is missing required CIDR: $cidr"
done

EXTRA_CIDR_LIST="$TMP_DIR/extra-cidrs"
printf '%s\n' "$AGENT_FETCH_EXTRA_DENY_CIDRS" | tr ',' '\n' >"$EXTRA_CIDR_LIST"
while IFS= read -r raw_cidr; do
  cidr=$(printf '%s' "$raw_cidr" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
  [ -n "$cidr" ] || fail 'AGENT_FETCH_EXTRA_DENY_CIDRS contains an empty entry'
  case "$cidr" in
    *[!0-9A-Fa-f:./]*) fail "AGENT_FETCH_EXTRA_DENY_CIDRS contains an unsafe CIDR token: $cidr" ;;
  esac
  case "$cidr" in
    */*) ;;
    *) fail "AGENT_FETCH_EXTRA_DENY_CIDRS entry is not a CIDR: $cidr" ;;
  esac
  case "$cidr" in
    *:*) family=6 ;;
    *.*) family=4 ;;
    *) fail "AGENT_FETCH_EXTRA_DENY_CIDRS entry has no IP family: $cidr" ;;
  esac
  nft_set_contains "$family" "$cidr" ||
    fail "loaded nftables deny$family is missing required site CIDR: $cidr"
done <"$EXTRA_CIDR_LIST"

printf 'PASS: Linux host satisfies agent Runtime cgroup, storage, secret, DNS, and nftables prerequisites\n'
