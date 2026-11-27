#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)

for tool in awk docker jq mktemp grep rm rmdir sed; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    printf 'error: required tool not found: %s\n' "$tool" >&2
    exit 1
  fi
done

if ! sh "$SCRIPT_DIR/test-agent-runtime-nft-reject.sh"; then
  printf 'error: nft reject matcher fixtures failed\n' >&2
  exit 1
fi

TMP_DIR=$(mktemp -d)
cleanup() {
  [ -n "$TMP_DIR" ] || return
  rm -f -- "$TMP_DIR/agent-fetch-hmac-key"
  rm -f -- "$TMP_DIR/base-compose.json"
  rm -f -- "$TMP_DIR/compose.json"
  rm -f -- "$TMP_DIR/security-compose.json"
  rm -f -- "$TMP_DIR/security-missing-cgroup-compose.json"
  rm -f -- "$TMP_DIR/fetch-services"
  rm -f -- "$TMP_DIR/no-profile-services"
  rm -f -- "$TMP_DIR/agent-fetch-egress.normalized"
  rm -f -- "$TMP_DIR/missing-env.err"
  rmdir -- "$TMP_DIR"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

export AGENT_RUNTIME_TOKEN=compose-static-runtime-token
export AGENT_RUNTIME_CGROUP_PARENT=agent-runtime.slice
export AGENT_RUNTIME_CGROUP_HOST_ROOT=/sys/fs/cgroup/agent-runtime.slice/commands
export AGENT_RUNTIME_WORKSPACE_HOST_ROOT=/mnt/agent-runtime-workspace
export AGENT_RUNTIME_LOG_HOST_ROOT=/mnt/agent-runtime-log
export AGENT_RUNTIME_WORKSPACE_MAX_BYTES=1073741824
export AGENT_RUNTIME_WORKSPACE_FS_MAX_BYTES=2147483648
export AGENT_RUNTIME_LOG_FS_MAX_BYTES=536870912

unset COMPOSE_PROFILES AGENT_FETCH_ENABLE AGENT_FETCH_AUDIT_HOST_ROOT
unset AGENT_RUNTIME_TEST_CGROUP_ROOT
unset AGENT_FETCH_AUDIT_FS_MAX_BYTES AGENT_FETCH_HMAC_SECRET_FILE
unset AGENT_FETCH_DNS_SERVERS AGENT_FETCH_EXTRA_DENY_CIDRS
unset AGENT_FETCH_POLICY_VERSION

BASE_RENDERED="$TMP_DIR/base-compose.json"
if ! (cd "$REPO_ROOT" && docker compose -f docker-compose.yml config --format json >"$BASE_RENDERED"); then
  printf 'not ok - base-compose-is-fetch-disabled-without-secret-or-socket\n' >&2
  exit 1
fi
if ! (cd "$REPO_ROOT" && docker compose -f docker-compose.yml config --quiet); then
  printf 'error: base docker compose config --quiet failed\n' >&2
  exit 1
fi
printf 'ok - base Compose renders quietly without Fetch inputs\n'

SECRET_FILE="$TMP_DIR/agent-fetch-hmac-key"
umask 077
printf 'compose-static-test-key\n' >"$SECRET_FILE"

export COMPOSE_PROFILES=agent-fetch
export AGENT_FETCH_ENABLE=true
export AGENT_FETCH_AUDIT_HOST_ROOT=/mnt/agent-fetch-audit
export AGENT_FETCH_AUDIT_FS_MAX_BYTES=536870912
export AGENT_FETCH_HMAC_SECRET_FILE="$SECRET_FILE"
export AGENT_FETCH_DNS_SERVERS=9.9.9.9:53,149.112.112.112:53
export AGENT_FETCH_EXTRA_DENY_CIDRS=8.8.8.0/24,2001:4860::/32
export AGENT_FETCH_POLICY_VERSION=2026-08-25
export AGENT_RUNTIME_SECURITY_TEST_GUARD=task9-acceptance-only

RENDERED="$TMP_DIR/compose.json"
if ! (cd "$REPO_ROOT" && docker compose -f docker-compose.yml -f docker-compose.fetch.yml config --format json >"$RENDERED"); then
  printf 'error: enabled Fetch Compose config failed\n' >&2
  exit 1
fi
if ! (cd "$REPO_ROOT" && docker compose -f docker-compose.yml -f docker-compose.fetch.yml config --quiet); then
  printf 'error: enabled Fetch Compose config --quiet failed\n' >&2
  exit 1
fi
if ! (cd "$REPO_ROOT" && docker compose -f docker-compose.yml -f docker-compose.fetch.yml config --services >"$TMP_DIR/fetch-services"); then
  printf 'error: enabled Fetch Compose service selection failed\n' >&2
  exit 1
fi
printf 'ok - enabled Fetch Compose renders with explicit overlay, profile, and inputs\n'

if ! (
  export COMPOSE_PROFILES=
  cd "$REPO_ROOT"
  docker compose -f docker-compose.yml -f docker-compose.fetch.yml config --services >"$TMP_DIR/no-profile-services"
); then
  printf 'error: no-profile Fetch Compose service selection failed\n' >&2
  exit 1
fi

SECURITY_RENDERED="$TMP_DIR/security-compose.json"
if ! (cd "$REPO_ROOT" && docker compose -f docker-compose.yml -f docker-compose.fetch.yml -f docker-compose.security-test.yml config --format json >"$SECURITY_RENDERED"); then
  printf 'error: security-test Compose override failed to render\n' >&2
  exit 1
fi
printf 'ok - security-test Compose override renders with base and Fetch overlay\n'

SECURITY_MISSING_CGROUP_RENDERED="$TMP_DIR/security-missing-cgroup-compose.json"
if ! (
  export AGENT_RUNTIME_TEST_CGROUP_ROOT=/sys/fs/cgroup/agent-runtime.slice/missing-static
  export MSYS_NO_PATHCONV=1
  cd "$REPO_ROOT"
  docker compose -f docker-compose.yml -f docker-compose.fetch.yml -f docker-compose.security-test.yml config --format json >"$SECURITY_MISSING_CGROUP_RENDERED"
); then
  printf 'error: security-test missing-cgroup override failed to render\n' >&2
  exit 1
fi

for required_name in \
  AGENT_RUNTIME_TOKEN \
  AGENT_RUNTIME_CGROUP_PARENT \
  AGENT_RUNTIME_CGROUP_HOST_ROOT \
  AGENT_RUNTIME_WORKSPACE_HOST_ROOT \
  AGENT_RUNTIME_LOG_HOST_ROOT \
  AGENT_RUNTIME_WORKSPACE_MAX_BYTES \
  AGENT_RUNTIME_WORKSPACE_FS_MAX_BYTES \
  AGENT_RUNTIME_LOG_FS_MAX_BYTES; do
  if (
    export "$required_name="
    cd "$REPO_ROOT"
    docker compose -f docker-compose.yml config --quiet >/dev/null 2>"$TMP_DIR/missing-env.err"
  ); then
    printf 'error: base Compose rendered without required variable %s\n' "$required_name" >&2
    exit 1
  fi
  if ! grep -F -- "$required_name" "$TMP_DIR/missing-env.err" >/dev/null 2>&1; then
    printf 'error: base missing-variable failure did not identify %s\n' "$required_name" >&2
    exit 1
  fi
done
printf 'ok - every base deployment interpolation fails closed with a named error\n'

for required_name in \
  AGENT_FETCH_ENABLE \
  AGENT_FETCH_AUDIT_HOST_ROOT \
  AGENT_FETCH_AUDIT_FS_MAX_BYTES \
  AGENT_FETCH_HMAC_SECRET_FILE \
  AGENT_FETCH_DNS_SERVERS \
  AGENT_FETCH_EXTRA_DENY_CIDRS \
  AGENT_FETCH_POLICY_VERSION; do
  if (
    export "$required_name="
    cd "$REPO_ROOT"
    docker compose -f docker-compose.yml -f docker-compose.fetch.yml config --quiet >/dev/null 2>"$TMP_DIR/missing-env.err"
  ); then
    printf 'error: Fetch overlay rendered without required variable %s\n' "$required_name" >&2
    exit 1
  fi
  if ! grep -F -- "$required_name" "$TMP_DIR/missing-env.err" >/dev/null 2>&1; then
    printf 'error: missing-variable failure did not identify %s\n' "$required_name" >&2
    exit 1
  fi
done
printf 'ok - every Fetch overlay interpolation fails closed with a named error\n'

failures=0

check_base_json() {
  description=$1
  filter=$2
  if jq -e \
    --arg cgroup_host "$AGENT_RUNTIME_CGROUP_HOST_ROOT" \
    --arg cgroup_parent "$AGENT_RUNTIME_CGROUP_PARENT" \
    "$filter" "$BASE_RENDERED" >/dev/null 2>&1; then
    printf 'ok - %s\n' "$description"
  else
    printf 'not ok - %s\n' "$description" >&2
    failures=$((failures + 1))
  fi
}

check_base_json 'base-compose-is-fetch-disabled-without-secret-or-socket' '
  (.services | has("agent-fetch-broker") | not) and
  (.networks | has("fetch-egress") | not) and
  ((.volumes // {}) | has("fetch-socket") | not) and
  ((.secrets // {}) | has("agent-fetch-hmac-key") | not) and
  ((.services["agent-runtime"].depends_on // {}) | has("agent-fetch-broker") | not) and
  (.services["agent-runtime"].environment as $e |
    $e.AGENT_RUNTIME_FETCH_ENABLED == "false" and
    $e.AGENT_RUNTIME_REQUIRE_FETCH_FOR_READINESS == "false" and
    ([ $e | keys[] | select(contains("FETCH")) ] | sort) ==
      ["AGENT_RUNTIME_FETCH_ENABLED", "AGENT_RUNTIME_REQUIRE_FETCH_FOR_READINESS"]) and
  (all(.services["agent-runtime"].volumes[]?;
    .source != "fetch-socket" and .target != "/run/agent-fetch")) and
  ((.services["agent-runtime"].secrets // []) | length) == 0'

check_base_json 'runtime-cgroup-paths-preserve-canonical-host-ancestry' '
  .services["agent-runtime"].environment as $e |
  $e.AGENT_RUNTIME_CGROUP_AGGREGATE_ROOT == ("/sys/fs/cgroup/" + $cgroup_parent) and
  $e.AGENT_RUNTIME_CGROUP_ROOT == ($e.AGENT_RUNTIME_CGROUP_AGGREGATE_ROOT + "/commands") and
  any(.services["agent-runtime"].volumes[];
    .type == "bind" and .source == $cgroup_host and .target == .source and
    (.read_only // false) == false)'
check_base_json 'Runtime host binds require existing paths' '
  all(.services["agent-runtime"].volumes[];
    .type != "bind" or (.bind.create_host_path // false) == false)'
check_base_json 'base Compose has no SearXNG-specific environment variables' '
  all(.services[];
    [(.environment // {}) | keys[] | select(test("SEARXNG"; "i"))] | length == 0)'

check_json() {
  description=$1
  filter=$2
  if jq -e \
    --arg cgroup_host "$AGENT_RUNTIME_CGROUP_HOST_ROOT" \
    --arg cgroup_parent "$AGENT_RUNTIME_CGROUP_PARENT" \
    --arg workspace_host "$AGENT_RUNTIME_WORKSPACE_HOST_ROOT" \
    --arg runtime_log_host "$AGENT_RUNTIME_LOG_HOST_ROOT" \
    --arg audit_host "$AGENT_FETCH_AUDIT_HOST_ROOT" \
    --arg workspace_max "$AGENT_RUNTIME_WORKSPACE_MAX_BYTES" \
    --arg workspace_fs_max "$AGENT_RUNTIME_WORKSPACE_FS_MAX_BYTES" \
    --arg runtime_log_fs_max "$AGENT_RUNTIME_LOG_FS_MAX_BYTES" \
    --arg audit_fs_max "$AGENT_FETCH_AUDIT_FS_MAX_BYTES" \
    --arg dns "$AGENT_FETCH_DNS_SERVERS" \
    --arg extra_deny "$AGENT_FETCH_EXTRA_DENY_CIDRS" \
    --arg policy_version "$AGENT_FETCH_POLICY_VERSION" \
    "$filter" "$RENDERED" >/dev/null 2>&1; then
    printf 'ok - %s\n' "$description"
  else
    printf 'not ok - %s\n' "$description" >&2
    failures=$((failures + 1))
  fi
}

check_json 'exact contractual networks are rendered with stable names' '
  (.networks | keys | sort) == ["bot-data", "bot-egress", "bot-runtime-control", "fetch-egress"] and
  all(.networks | to_entries[]; .value.name == .key)'
check_json 'control and data networks are internal' '
  .networks["bot-runtime-control"].internal == true and
  .networks["bot-data"].internal == true'
check_json 'service network membership is least privilege' '
  (.services.bot.networks | keys | sort) == ["bot-data", "bot-egress", "bot-runtime-control"] and
  (.services["agent-runtime"].networks | keys) == ["bot-runtime-control"] and
  (.services["agent-fetch-broker"].networks | keys) == ["fetch-egress"] and
  (.services.redis.networks | keys) == ["bot-data"]'
check_json 'fetch egress uses the deterministic bridge' '
  .networks["fetch-egress"].driver == "bridge" and
  .networks["fetch-egress"].driver_opts["com.docker.network.bridge.name"] == "br-agent-fetch"'
check_json 'runtime and broker use production build targets' '
  .services["agent-runtime"].build.target == "runtime" and
  .services["agent-fetch-broker"].build.target == "broker"'
check_json 'fetch-compose-requires-overlay-profile-and-enable' '
  .services["agent-fetch-broker"].profiles == ["agent-fetch"] and
  .services["agent-fetch-broker"].environment.AGENT_FETCH_ENABLED == "true" and
  .services["agent-runtime"].environment.AGENT_RUNTIME_FETCH_ENABLED == "true" and
  .services["agent-runtime"].environment.AGENT_RUNTIME_REQUIRE_FETCH_FOR_READINESS == "true" and
  ((.services["agent-runtime"].depends_on // {}) | has("agent-fetch-broker") | not) and
  (all(.services[]; ([((.environment // {}) | keys[]) | select(contains("RECEIPT"))] | length) == 0))'
if grep -F -x -- 'agent-fetch-broker' "$TMP_DIR/fetch-services" >/dev/null 2>&1 &&
  ! grep -F -x -- 'agent-fetch-broker' "$TMP_DIR/no-profile-services" >/dev/null 2>&1; then
  printf 'ok - Fetch Broker activation requires the agent-fetch profile\n'
else
  printf 'not ok - Fetch Broker activation requires the agent-fetch profile\n' >&2
  failures=$((failures + 1))
fi
check_json 'broker-has-exact-service-limits' '
  .services["agent-fetch-broker"].pids_limit == 128 and
  (.services["agent-fetch-broker"].mem_limit | tonumber) == 268435456 and
  (.services["agent-fetch-broker"].memswap_limit | tonumber) == 268435456 and
  .services["agent-fetch-broker"].cpus == 1 and
  .services["agent-fetch-broker"].ulimits.nofile.soft == 256 and
  .services["agent-fetch-broker"].ulimits.nofile.hard == 1024 and
  (.services["agent-fetch-broker"].cap_drop | map(ascii_upcase)) == ["ALL"] and
  ((.services["agent-fetch-broker"].cap_add // []) | length) == 0 and
  (.services["agent-fetch-broker"].security_opt | index("no-new-privileges:true")) != null and
  (.services["agent-fetch-broker"].privileged // false) == false and
  (.services["agent-fetch-broker"].pid // "") != "host"'
check_json 'broker mounts only its socket and bounded audit root' '
  (.services["agent-fetch-broker"].volumes | length) == 2 and
  any(.services["agent-fetch-broker"].volumes[]; .type == "volume" and .source == "fetch-socket" and .target == "/run/agent-fetch") and
  any(.services["agent-fetch-broker"].volumes[]; .type == "bind" and .source == $audit_host and .target == "/var/log/agent-fetch")'
check_json 'shared UDS volume is exclusive to runtime and broker' '
  ([.services | to_entries[] | select(any(.value.volumes[]?; .source == "fetch-socket")) | .key] | sort) ==
  ["agent-fetch-broker", "agent-runtime"]'
check_json 'shared UDS storage is a bounded non-executable tmpfs volume' '
  .volumes["fetch-socket"].name == "fetch-socket" and
  .volumes["fetch-socket"].driver == "local" and
  .volumes["fetch-socket"].driver_opts.type == "tmpfs" and
  .volumes["fetch-socket"].driver_opts.device == "tmpfs" and
  (.volumes["fetch-socket"].driver_opts.o | contains("size=1m")) and
  (.volumes["fetch-socket"].driver_opts.o | contains("uid=10002")) and
  (.volumes["fetch-socket"].driver_opts.o | contains("gid=10001")) and
  (.volumes["fetch-socket"].driver_opts.o | contains("mode=0770")) and
  (.volumes["fetch-socket"].driver_opts.o | contains("noexec"))'
check_json 'HMAC secret is exclusive to runtime and broker' '
  ([.services | to_entries[] | select(any(.value.secrets[]?; .source == "agent-fetch-hmac-key" and .target == "/run/secrets/agent_fetch_hmac_key")) | .key] | sort) ==
  ["agent-fetch-broker", "agent-runtime"] and
  (.secrets["agent-fetch-hmac-key"].file | length) > 0 and
  .services["agent-runtime"].secrets[0].uid == "10001" and
  .services["agent-runtime"].secrets[0].gid == "10001" and
  .services["agent-runtime"].secrets[0].mode == "0440" and
  .services["agent-fetch-broker"].secrets[0].uid == "10002" and
  .services["agent-fetch-broker"].secrets[0].gid == "10001" and
  .services["agent-fetch-broker"].secrets[0].mode == "0440"'
check_json 'dangerous container privileges, tracing, unconfined profiles, and Docker socket are absent' '
  all(.services | to_entries[];
    (.value.privileged // false) != true and
    (all(.value.cap_add[]?; (ascii_upcase != "SYS_ADMIN" and ascii_upcase != "SYS_PTRACE"))) and
    (all(.value.security_opt[]?; (ascii_downcase != "seccomp=unconfined" and ascii_downcase != "apparmor=unconfined"))) and
    (all(.value.volumes[]?;
      (((.source // "") | ascii_downcase) | contains("docker.sock") | not) and
      (((.target // "") | ascii_downcase) | contains("docker.sock") | not) and
      (((.source // "") | ascii_downcase) | contains("docker_engine") | not) and
      (((.target // "") | ascii_downcase) | contains("docker_engine") | not))))'
check_json 'runtime and broker drop every capability with no-new-privileges' '
  all([.services["agent-runtime"], .services["agent-fetch-broker"]][];
    (.cap_drop | map(ascii_upcase)) == ["ALL"] and
    (.security_opt | index("no-new-privileges:true")) != null)'
check_json 'runtime has aggregate Docker resource limits' '
  .services["agent-runtime"].cgroup == "host" and
  .services["agent-runtime"].cgroup_parent == "agent-runtime.slice" and
  .services["agent-runtime"].pids_limit == 512 and
  (.services["agent-runtime"].mem_limit | tonumber) == 1073741824 and
  (.services["agent-runtime"].memswap_limit | tonumber) == 1073741824 and
  .services["agent-runtime"].cpus == 2 and
  .services["agent-runtime"].init == true'
check_json 'runtime has finite nofile and executable hardened tmpfs for PRoot' '
  .services["agent-runtime"].ulimits.nofile.soft == 256 and
  .services["agent-runtime"].ulimits.nofile.hard == 4096 and
  any(.services["agent-runtime"].tmpfs[];
    . == "/tmp:rw,exec,nosuid,nodev,size=64m,mode=1777" or
    (.path == "/tmp" and .size == 67108864 and .mode == 1023 and .exec == true and .nosuid == true and .nodev == true)) and
  all(.services["agent-runtime"].tmpfs[];
    (type != "string" or (contains("noexec") | not)) and
    (type != "object" or (.noexec // false) != true))'
check_json 'delegated commands sibling uses the canonical host path in Runtime' '
  .services["agent-runtime"].environment as $e |
  $e.AGENT_RUNTIME_CGROUP_AGGREGATE_ROOT == ("/sys/fs/cgroup/" + $cgroup_parent) and
  $e.AGENT_RUNTIME_CGROUP_ROOT == ($e.AGENT_RUNTIME_CGROUP_AGGREGATE_ROOT + "/commands") and
  any(.services["agent-runtime"].volumes[];
    .type == "bind" and .source == $cgroup_host and
    .target == .source and (.read_only // false) == false)'
check_json 'Runtime and Broker host binds require existing paths' '
  all([.services["agent-runtime"], .services["agent-fetch-broker"]][].volumes[];
    .type != "bind" or (.bind.create_host_path // false) == false)'
check_json 'workspace, runtime log, and broker audit bind roots stay separate' '
  ($workspace_host != $runtime_log_host and $workspace_host != $audit_host and $runtime_log_host != $audit_host) and
  any(.services["agent-runtime"].volumes[]; .type == "bind" and .source == $workspace_host and .target == "/runtime/workspaces") and
  any(.services["agent-runtime"].volumes[]; .type == "bind" and .source == $runtime_log_host and .target == "/runtime/logs") and
  any(.services["agent-fetch-broker"].volumes[]; .type == "bind" and .source == $audit_host and .target == "/var/log/agent-fetch")'
check_json 'Runtime has exactly one read-only repository skills mount' '
  [.services["agent-runtime"].volumes[] | select(.target == "/runtime/skills")] as $skills |
  ($skills | length) == 1 and $skills[0].read_only == true'
check_json 'Bot neither mounts nor shares the Runtime skills source' '
  ([.services["agent-runtime"].volumes[] | select(.target == "/runtime/skills")] | .[0].source) as $runtime_skills_source |
  all(.services.bot.volumes[]?;
    .target != "/runtime/skills" and .source != $runtime_skills_source)'
check_json 'rendered services do not define SearXNG' '
  (.services | has("searxng") | not)'
check_json 'logical and filesystem capacity ceilings are required in services' '
  .services["agent-runtime"].environment.AGENT_RUNTIME_WORKSPACE_MAX_BYTES == $workspace_max and
  .services["agent-runtime"].environment.AGENT_RUNTIME_WORKSPACE_FS_MAX_BYTES == $workspace_fs_max and
  .services["agent-runtime"].environment.AGENT_RUNTIME_LOG_FS_MAX_BYTES == $runtime_log_fs_max and
  .services["agent-fetch-broker"].environment.AGENT_FETCH_AUDIT_FS_MAX_BYTES == $audit_fs_max'
check_json 'runtime command and token caps equal approved defaults' '
  .services["agent-runtime"].environment as $e |
  $e.AGENT_RUNTIME_COMMAND_TIMEOUT_SECS == "120" and
  $e.AGENT_RUNTIME_COMMAND_PIDS_MAX == "64" and
  $e.AGENT_RUNTIME_COMMAND_MEMORY_MAX_BYTES == "268435456" and
  $e.AGENT_RUNTIME_COMMAND_MEMORY_SWAP_MAX_BYTES == "0" and
  $e.AGENT_RUNTIME_COMMAND_CPU_QUOTA_US == "100000" and
  $e.AGENT_RUNTIME_COMMAND_CPU_PERIOD_US == "100000" and
  $e.AGENT_RUNTIME_COMMAND_CPU_BUDGET_SECS == "120" and
  $e.AGENT_RUNTIME_COMMAND_NPROC == "480" and
  $e.AGENT_RUNTIME_COMMAND_NOFILE == "256" and
  $e.AGENT_RUNTIME_COMMAND_FSIZE_BYTES == "67108864" and
  $e.AGENT_RUNTIME_FETCH_MAX_CONCURRENCY == "2" and
  $e.AGENT_RUNTIME_FETCH_MAX_REQUESTS == "20" and
  $e.AGENT_RUNTIME_FETCH_MAX_REQUEST_BYTES == "8388608" and
  $e.AGENT_RUNTIME_FETCH_MAX_RESPONSE_BYTES == "33554432"'
check_json 'broker budgets, timeouts, and request caps equal approved defaults' '
  .services["agent-fetch-broker"].environment as $e |
  $e.AGENT_FETCH_REQUEST_HEADER_MAX_BYTES == "32768" and
  $e.AGENT_FETCH_REQUEST_BODY_MAX_BYTES == "8388608" and
  $e.AGENT_FETCH_RESPONSE_HEADER_MAX_BYTES == "32768" and
  $e.AGENT_FETCH_RESPONSE_NETWORK_MAX_BYTES == "16777216" and
  $e.AGENT_FETCH_RESPONSE_DECODED_MAX_BYTES == "33554432" and
  $e.AGENT_FETCH_MAX_DECOMPRESSION_RATIO == "20" and
  $e.AGENT_FETCH_DNS_TIMEOUT_MS == "2000" and
  $e.AGENT_FETCH_CONNECT_TIMEOUT_MS == "3000" and
  $e.AGENT_FETCH_FIRST_BYTE_TIMEOUT_MS == "5000" and
  $e.AGENT_FETCH_TOTAL_TIMEOUT_MS == "30000" and
  $e.AGENT_FETCH_MAX_CONCURRENCY == "2" and
  $e.AGENT_FETCH_MAX_REQUESTS == "20" and
  $e.AGENT_FETCH_MAX_REDIRECTS == "5"'
check_json 'runtime claims do not exceed broker caps' '
  (.services["agent-runtime"].environment.AGENT_RUNTIME_FETCH_MAX_CONCURRENCY | tonumber) <=
    (.services["agent-fetch-broker"].environment.AGENT_FETCH_MAX_CONCURRENCY | tonumber) and
  (.services["agent-runtime"].environment.AGENT_RUNTIME_FETCH_MAX_REQUESTS | tonumber) <=
    (.services["agent-fetch-broker"].environment.AGENT_FETCH_MAX_REQUESTS | tonumber) and
  (.services["agent-runtime"].environment.AGENT_RUNTIME_FETCH_MAX_REQUEST_BYTES | tonumber) <=
    (.services["agent-fetch-broker"].environment.AGENT_FETCH_REQUEST_BODY_MAX_BYTES | tonumber) and
  (.services["agent-runtime"].environment.AGENT_RUNTIME_FETCH_MAX_RESPONSE_BYTES | tonumber) <=
    (.services["agent-fetch-broker"].environment.AGENT_FETCH_RESPONSE_DECODED_MAX_BYTES | tonumber)'
check_json 'socket, identity, DNS, deny policy, and policy version are explicit' '
  .services["agent-runtime"].environment.AGENT_FETCH_SOCKET == "/run/agent-fetch/fetch.sock" and
  .services["agent-fetch-broker"].environment.AGENT_FETCH_SOCKET == "/run/agent-fetch/fetch.sock" and
  .services["agent-fetch-broker"].environment.AGENT_FETCH_PEER_UID == "10001" and
  .services["agent-fetch-broker"].environment.AGENT_FETCH_PEER_GID == "10001" and
  .services["agent-fetch-broker"].environment.AGENT_FETCH_DNS_SERVERS == $dns and
  (.services["agent-fetch-broker"].environment.AGENT_FETCH_DENY_CIDRS | contains($extra_deny)) and
  .services["agent-runtime"].environment.AGENT_FETCH_POLICY_VERSION == $policy_version and
  .services["agent-fetch-broker"].environment.AGENT_FETCH_POLICY_VERSION == $policy_version'
check_json 'HMAC value is never an environment variable' '
  all(.services[]; (.environment.AGENT_FETCH_HMAC_KEY? // null) == null) and
  .services["agent-runtime"].environment.AGENT_FETCH_HMAC_KEY_FILE == "/run/secrets/agent_fetch_hmac_key" and
  .services["agent-fetch-broker"].environment.AGENT_FETCH_HMAC_KEY_FILE == "/run/secrets/agent_fetch_hmac_key"'
check_json 'legacy links and host networking are absent' '
  all(.services[]; (.links? // null) == null and (.network_mode? // "") != "host")'

check_security_json() {
  description=$1
  filter=$2
  if jq -e \
    --arg guard "$AGENT_RUNTIME_SECURITY_TEST_GUARD" \
    --arg cgroup_host "$AGENT_RUNTIME_CGROUP_HOST_ROOT" \
    --arg cgroup_parent "$AGENT_RUNTIME_CGROUP_PARENT" \
    --arg workspace_host "$AGENT_RUNTIME_WORKSPACE_HOST_ROOT" \
    --arg runtime_log_host "$AGENT_RUNTIME_LOG_HOST_ROOT" \
    --arg audit_host "$AGENT_FETCH_AUDIT_HOST_ROOT" \
    --arg policy_version "$AGENT_FETCH_POLICY_VERSION" \
    "$filter" "$SECURITY_RENDERED" >/dev/null 2>&1; then
    printf 'ok - %s\n' "$description"
  else
    printf 'not ok - %s\n' "$description" >&2
    failures=$((failures + 1))
  fi
}

check_security_json 'test override alone selects the forbidden-in-production Runtime target' '
  .services["agent-runtime"].build.target == "runtime-security-test" and
  .services["agent-runtime"].labels["org.csusters.agent-runtime.security-test-only"] == "true" and
  .services["agent-runtime"].labels["org.csusters.agent-runtime.security-test-guard"] == $guard and
  .services["agent-runtime"].environment.AGENT_RUNTIME_SECURITY_TEST_ONLY == "1"'
check_security_json 'test Runtime keeps cgroup enforcement and adds a readiness health probe' '
  .services["agent-runtime"].environment as $e |
  $e.AGENT_RUNTIME_CGROUP_AGGREGATE_ROOT == ("/sys/fs/cgroup/" + $cgroup_parent) and
  $e.AGENT_RUNTIME_CGROUP_ROOT == ($e.AGENT_RUNTIME_CGROUP_AGGREGATE_ROOT + "/commands") and
  (.services["agent-runtime"].healthcheck.test | length) >= 4 and
  .services["agent-runtime"].healthcheck.interval == "2s" and
  .services["agent-runtime"].healthcheck.retries == 15'
if jq -e '
  .services["agent-runtime"].environment.AGENT_RUNTIME_CGROUP_ROOT ==
    "/sys/fs/cgroup/agent-runtime.slice/missing-static"
' "$SECURITY_MISSING_CGROUP_RENDERED" >/dev/null 2>&1; then
  printf 'ok - security override consumes the test-only command cgroup root override\n'
else
  actual_cgroup_override=$(jq -r '.services["agent-runtime"].environment.AGENT_RUNTIME_CGROUP_ROOT // "<missing>"' "$SECURITY_MISSING_CGROUP_RENDERED")
  printf 'not ok - security override consumes the test-only command cgroup root override (rendered %s)\n' "$actual_cgroup_override" >&2
  failures=$((failures + 1))
fi
check_security_json 'fixture is test-only and isolated to deterministic Fetch egress IP' '
  .services["agent-fetch-fixture"].labels["org.csusters.agent-runtime.security-test-only"] == "true" and
  .services["agent-fetch-fixture"].labels["org.csusters.agent-runtime.security-test-guard"] == $guard and
  (.services["agent-fetch-fixture"].networks | keys) == ["fetch-egress"] and
  .services["agent-fetch-fixture"].networks["fetch-egress"].ipv4_address == "11.0.0.10" and
  (.services["agent-fetch-fixture"].volumes // [] | length) == 0 and
  (.services["agent-fetch-fixture"].secrets // [] | length) == 0 and
  (.services["agent-fetch-fixture"].ports // [] | length) == 0'
check_security_json 'test Broker retains Fetch-only membership at a deterministic denied address' '
  (.services["agent-fetch-broker"].networks | keys) == ["fetch-egress"] and
  .services["agent-fetch-broker"].networks["fetch-egress"].ipv4_address == "11.0.0.2"'
check_security_json 'security override retains exact production Broker resources' '
  .services["agent-fetch-broker"].profiles == ["agent-fetch"] and
  .services["agent-fetch-broker"].restart == "always" and
  .services["agent-fetch-broker"].environment.AGENT_FETCH_POLICY_VERSION == $policy_version and
  .services["agent-runtime"].environment.AGENT_FETCH_POLICY_VERSION == $policy_version and
  .services["agent-fetch-broker"].pids_limit == 128 and
  (.services["agent-fetch-broker"].mem_limit | tonumber) == 268435456 and
  (.services["agent-fetch-broker"].memswap_limit | tonumber) == 268435456 and
  .services["agent-fetch-broker"].cpus == 1 and
  .services["agent-fetch-broker"].ulimits.nofile.soft == 256 and
  .services["agent-fetch-broker"].ulimits.nofile.hard == 1024'
check_security_json 'fixture has bounded resources and no elevated container authority' '
  .services["agent-fetch-fixture"].read_only == true and
  .services["agent-fetch-fixture"].pids_limit == 32 and
  (.services["agent-fetch-fixture"].mem_limit | tonumber) == 134217728 and
  (.services["agent-fetch-fixture"].memswap_limit | tonumber) == 134217728 and
  .services["agent-fetch-fixture"].cpus == 0.5 and
  (.services["agent-fetch-fixture"].cap_drop | map(ascii_upcase)) == ["ALL"] and
  (.services["agent-fetch-fixture"].security_opt | index("no-new-privileges:true")) != null'
check_security_json 'fixture request, event, and stream safety caps are explicit' '
  .services["agent-fetch-fixture"].environment.FIXTURE_MAX_REQUEST_BODY_BYTES == "8388608" and
  .services["agent-fetch-fixture"].environment.FIXTURE_MAX_EVENTS == "256" and
  .services["agent-fetch-fixture"].environment.FIXTURE_STREAM_MAX_BYTES == "67108864" and
  .services["agent-fetch-fixture"].environment.FIXTURE_BYTES_MAX_BYTES == "8388608"'
check_security_json 'security-test Fetch network has the deterministic public-looking subnet' '
  .networks["fetch-egress"].ipam.config == [{"subnet":"11.0.0.0/24","gateway":"11.0.0.1"}] and
  .services["agent-fetch-broker"].environment.AGENT_FETCH_DNS_SERVERS == "11.0.0.10:5353"'
check_security_json 'security override retains caller-prepared bounded roots and delegated cgroup only' '
  .services["agent-runtime"].restart == "always" and
  (.services["agent-runtime"].networks | keys) == ["bot-runtime-control"] and
  any(.services["agent-runtime"].volumes[]; .source == $cgroup_host and .target == $cgroup_host) and
  any(.services["agent-runtime"].volumes[]; .source == $workspace_host and .target == "/runtime/workspaces") and
  any(.services["agent-runtime"].volumes[]; .source == $runtime_log_host and .target == "/runtime/logs") and
  any(.services["agent-fetch-broker"].volumes[]; .source == $audit_host and .target == "/var/log/agent-fetch")'
check_security_json 'security override does not add privileged, SYS_ADMIN, SYS_PTRACE, unconfined profiles, Docker socket, or host network' '
  all(.services | to_entries[];
    (.value.privileged // false) != true and
    (.value.network_mode // "") != "host" and
    all(.value.cap_add[]?; (ascii_upcase != "SYS_ADMIN" and ascii_upcase != "SYS_PTRACE")) and
    all(.value.security_opt[]?; (ascii_downcase != "seccomp=unconfined" and ascii_downcase != "apparmor=unconfined")) and
    all(.value.volumes[]?;
      (((.source // "") | ascii_downcase) | contains("docker.sock") | not) and
      (((.target // "") | ascii_downcase) | contains("docker.sock") | not)))'

if jq -e '
  .services["agent-runtime"].build.target == "runtime" and
  (.services | has("agent-fetch-broker") | not) and
  (.services | has("agent-fetch-fixture") | not) and
  (.services["agent-runtime"].labels["org.csusters.agent-runtime.security-test-only"]? // null) == null and
  (.services["agent-runtime"].environment.AGENT_RUNTIME_SECURITY_TEST_ONLY? // null) == null
' "$BASE_RENDERED" >/dev/null 2>&1; then
  printf 'ok - default production Compose has neither Fetch nor security-test-only artifacts\n'
else
  printf 'not ok - default production Compose has neither Fetch nor security-test-only artifacts\n' >&2
  failures=$((failures + 1))
fi

NFT_FILE="$REPO_ROOT/deploy/agent-fetch-egress.nft"
if [ ! -f "$NFT_FILE" ]; then
  printf 'not ok - nftables source exists\n' >&2
  failures=$((failures + 1))
else
  NORMALIZED_NFT="$TMP_DIR/agent-fetch-egress.normalized"
  sed 's/^[[:space:]]*//;s/[[:space:]]*$//' "$NFT_FILE" >"$NORMALIZED_NFT"

  check_nft_line() {
    description=$1
    expected=$2
    if grep -F -x -- "$expected" "$NORMALIZED_NFT" >/dev/null 2>&1; then
      printf 'ok - %s\n' "$description"
    else
      printf 'not ok - %s\n' "$description" >&2
      failures=$((failures + 1))
    fi
  }

  check_nft_contains() {
    description=$1
    expected=$2
    if grep -F -- "$expected" "$NFT_FILE" >/dev/null 2>&1; then
      printf 'ok - %s\n' "$description"
    else
      printf 'not ok - %s\n' "$description" >&2
      failures=$((failures + 1))
    fi
  }

  check_nft_block_line() {
    block_type=$1
    block_name=$2
    description=$3
    expected=$4
    if awk -v block_type="$block_type" -v block_name="$block_name" -v expected="$expected" '
      $0 == block_type " " block_name " {" { in_block = 1; next }
      in_block && $0 == expected { found = 1 }
      in_block && $0 == "}" { in_block = 0 }
      END { exit(found ? 0 : 1) }
    ' "$NORMALIZED_NFT"; then
      printf 'ok - %s\n' "$description"
    else
      printf 'not ok - %s\n' "$description" >&2
      failures=$((failures + 1))
    fi
  }

  check_nft_line 'nftables inet agent_fetch table is declared' 'table inet agent_fetch {'
  check_nft_block_line set deny4 'nftables deny4 is an interval set' 'flags interval'
  check_nft_block_line set deny6 'nftables deny6 is an interval set' 'flags interval'
  check_nft_block_line chain input 'input chain uses the required hook, priority, and policy' 'type filter hook input priority filter - 5; policy accept;'
  check_nft_block_line chain input 'input chain rejects bridge traffic to the host' 'iifname "br-agent-fetch" reject'
  check_nft_block_line chain forward 'forward chain uses the required hook, priority, and policy' 'type filter hook forward priority filter - 5; policy accept;'
  check_nft_block_line chain forward 'forward chain rejects IPv4 deny destinations' 'iifname "br-agent-fetch" ip daddr @deny4 reject'
  check_nft_block_line chain forward 'forward chain rejects IPv6 deny destinations' 'iifname "br-agent-fetch" ip6 daddr @deny6 reject'

  for cidr in \
    0.0.0.0/8 10.0.0.0/8 100.64.0.0/10 127.0.0.0/8 \
    169.254.0.0/16 172.16.0.0/12 192.0.0.0/24 192.0.2.0/24 \
    192.168.0.0/16 198.18.0.0/15 198.51.100.0/24 \
    203.0.113.0/24 224.0.0.0/4 240.0.0.0/4 \
    ::/128 ::1/128 ::ffff:0:0/96 100::/64 2001:db8::/32 \
    fc00::/7 fe80::/10 ff00::/8; do
    check_nft_contains "nftables base deny set contains $cidr" "$cidr"
  done
fi

if [ "$failures" -ne 0 ]; then
  printf 'FAILED: %s static deployment assertion(s) failed\n' "$failures" >&2
  exit 1
fi

printf 'PASS: agent Runtime Compose and nftables topology is fail-closed\n'
