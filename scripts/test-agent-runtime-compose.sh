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

check_file_contains() {
  description=$1
  path=$2
  expected=$3
  if grep -F -- "$expected" "$path" >/dev/null 2>&1; then
    printf 'ok - %s\n' "$description"
  else
    printf 'not ok - %s\n' "$description" >&2
    failures=$((failures + 1))
  fi
}

check_file_excludes() {
  description=$1
  path=$2
  forbidden=$3
  if grep -F -- "$forbidden" "$path" >/dev/null 2>&1; then
    printf 'not ok - %s\n' "$description" >&2
    failures=$((failures + 1))
  else
    printf 'ok - %s\n' "$description"
  fi
}

check_file_contains_all() {
  description=$1
  path=$2
  shift 2
  matched=true
  for expected in "$@"; do
    if ! grep -F -- "$expected" "$path" >/dev/null 2>&1; then
      matched=false
    fi
  done
  if [ "$matched" = true ]; then
    printf 'ok - %s\n' "$description"
  else
    printf 'not ok - %s\n' "$description" >&2
    failures=$((failures + 1))
  fi
}

check_file_contains_adjacent() {
  description=$1
  path=$2
  first=$3
  second=$4
  if awk -v first="$first" -v second="$second" '
      previous && index($0, second) { found = 1; exit }
      { previous = index($0, first) > 0 }
      END { exit found ? 0 : 1 }
    ' "$path"; then
    printf 'ok - %s\n' "$description"
  else
    printf 'not ok - %s\n' "$description" >&2
    failures=$((failures + 1))
  fi
}

base_create_host_path_count=$(grep -F -c -- 'create_host_path: false' "$REPO_ROOT/docker-compose.yml" || true)
fetch_create_host_path_count=$(grep -F -c -- 'create_host_path: false' "$REPO_ROOT/docker-compose.fetch.yml" || true)
if [ "$base_create_host_path_count" -eq 4 ] && [ "$fetch_create_host_path_count" -eq 1 ]; then
  printf 'ok - all required base and Fetch host binds disable path auto-creation\n'
else
  printf 'not ok - all required base and Fetch host binds disable path auto-creation\n' >&2
  failures=$((failures + 1))
fi

check_file_excludes 'production Compose does not reference the security-test Runtime target' \
  "$REPO_ROOT/docker-compose.yml" 'runtime-security-test'
check_file_excludes 'production Compose does not reference the test fixture service' \
  "$REPO_ROOT/docker-compose.yml" 'agent-fetch-fixture'
check_file_excludes 'production Compose does not carry security-test-only labels' \
  "$REPO_ROOT/docker-compose.yml" 'security-test-only'
check_file_excludes 'base Compose does not define the Fetch Broker' \
  "$REPO_ROOT/docker-compose.yml" 'agent-fetch-broker'
check_file_excludes 'base Compose does not define Fetch socket or HMAC material' \
  "$REPO_ROOT/docker-compose.yml" 'fetch-socket'
check_file_excludes 'base Compose does not define Fetch egress' \
  "$REPO_ROOT/docker-compose.yml" 'fetch-egress'
check_file_contains 'Runtime Dockerfile defines the approved security-test-only target' \
  "$REPO_ROOT/agent-runtime/Dockerfile" 'FROM runtime-base AS runtime-security-test'
check_file_contains 'Runtime security-test target has the approved image label' \
  "$REPO_ROOT/agent-runtime/Dockerfile" 'LABEL org.csusters.agent-runtime.security-test-only="true"'
if awk '
  /^FROM runtime-base AS runtime-security-test$/ { security = NR }
  /^FROM runtime-base AS runtime$/ { production = NR }
  END { exit(security > 0 && production > security ? 0 : 1) }
' "$REPO_ROOT/agent-runtime/Dockerfile"; then
  printf 'ok - production Runtime is a clean runtime-base stage after the test-only target\n'
else
  printf 'not ok - production Runtime is a clean runtime-base stage after the test-only target\n' >&2
  failures=$((failures + 1))
fi
check_file_contains 'fixture image is labeled security-test-only' \
  "$REPO_ROOT/agent-runtime/tests/fixtures/Dockerfile" \
  'LABEL org.csusters.agent-runtime.security-test-only="true"'
check_file_contains_all 'attack matrix observes exact command RLIMIT_NPROC=480' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'nproc_limit_observation()' 'ulimit -u' 'nproc=480'
check_file_contains_all 'attack matrix records cgroup populated, PID, and removal lifecycle evidence' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'observe_command_cgroup_cleanup()' 'start_cgroup_cleanup_observer()' \
  'cgroup.events' 'cgroup.procs' 'completion_evidence='
check_file_contains_all 'attack matrix requires and increments a live nft input-hook counter' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'AGENT_FETCH_NFT_TEST_INPUT_TARGET' 'AGENT_FETCH_NFT_TEST_INPUT_RULE_HANDLE' 'nft_counter input'
check_file_contains_all 'attack matrix activates the test stack only through base, Fetch overlay, security override, profile, and explicit enable' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'FETCH_OVERLAY="$REPO_ROOT/docker-compose.fetch.yml"' \
  '-f "$REPO_ROOT/docker-compose.yml" -f "$FETCH_OVERLAY" -f "$COMPOSE_OVERRIDE"' \
  'export COMPOSE_PROFILES=agent-fetch' 'export AGENT_FETCH_ENABLE=true'

for required_case in \
  deployed-aggregate-direct-children \
  actual-supervisor-nondumpable-attach-denied \
  command-control-env-copy-binding \
  command-control-post-exit-revocation \
  command-control-packet-session-bounds \
  broker-preauth-semaphore-deadline \
  broker-server-connect-denial \
  header-separator-real-surface \
  workspace-root-independent-of-cwd \
  fetch-output-shared-quota \
  nonreading-peer-bounded-cancel \
  deployed-graceful-sigterm-lifecycle \
  production-activation-default-off; do
  check_file_contains "attack matrix locks named case $required_case" \
    "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" "case_run $required_case "
done
check_file_excludes 'audit Start never claims request body bytes before Broker Continue' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" 'request_body_byte_len > 0'
check_file_contains_all 'audit acceptance locks empty Start and actual Completion body semantics' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855' \
  '$starts[0].request_body_byte_len == 0' '$completions[0].request_body_bytes == $sentinel_bytes'
check_file_excludes 'attack matrix has no Shell token capture helper' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" 'capture_command_token()'
check_file_excludes 'attack matrix has no Shell direct-Broker helper' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" 'direct_uds_command()'
check_file_excludes 'attack matrix never exposes the Broker token variable to a command Shell' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" 'AGENT_FETCH_TOKEN'
check_file_excludes 'attack matrix never exposes the Broker socket variable to a command Shell' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" 'AGENT_FETCH_SOCKET'
check_file_excludes 'rollback gate no longer references the deleted permissive Fetch default test' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" 'TestAgentV3FetchEnabledDefaultsTrueAndAllowsExplicitDisable'
check_file_contains_all 'rollback gate runs every exact current test selector with observable run/pass evidence' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'run_exact_go_test()' '=== RUN   ' '--- PASS: ' 'runs == 1 && passes == 1' \
  'run_exact_go_test ./config TestAgentV3RuntimeFetchDefaultsDisabled rollback-fetch-default-disabled' \
  'run_exact_go_test ./config TestAgentV3RuntimeFetchRequiresExplicitTrue rollback-fetch-explicit-true' \
  'run_exact_go_test ./chatv2 TestAgentV3FetchGuidanceIsOmittedWhenDisabled rollback-fetch-guidance-disabled' \
  'run_exact_go_test ./chatv2 TestBuildAgentV3StablePrefixHashIncludesRuntimeRules rollback-stable-prefix-runtime-rules'
check_file_excludes 'output cleanup assertions no longer use the obsolete non-dotfile Fetch temp glob' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" '*.fetch-tmp-*'
check_file_contains_all 'output success, check-status, cancel, error, and quota paths use exact adjacent dotfile temp names recursively' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  '.atomic.json.agent-runtime-????????????????.tmp' \
  '.atomic-failure.txt.agent-runtime-????????????????.tmp' \
  '.atomic-timeout.txt.agent-runtime-????????????????.tmp' \
  '.out.agent-runtime-????????????????.tmp' \
  '.fetch-quota.bin.agent-runtime-????????????????.tmp' \
  'find "$AGENT_RUNTIME_WORKSPACE_HOST_ROOT" -type f' \
  'fetch --timeout 50ms --output /workspace/atomic-timeout.txt' \
  'fetch --output /workspace/locked/out'
check_file_excludes 'temp cleanup does not use a broad pattern that could match unrelated destination names' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" "-name '.*.agent-runtime-????????????????.tmp'"
check_file_contains_all 'check-status with output remains exit 22, preserves the old destination, and removes the exact adjacent temp' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'fetch --check-status --output /workspace/atomic-failure.txt' \
  'test "$?" -eq 22' 'test "$(cat /workspace/atomic-failure.txt)" = preserved' \
  '.atomic-failure.txt.agent-runtime-????????????????.tmp'
check_file_contains_all 'no-delegation case drives the test-only Compose cgroup override and restores it' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'export AGENT_RUNTIME_TEST_CGROUP_ROOT=/sys/fs/cgroup/$AGENT_RUNTIME_CGROUP_PARENT/missing-task9' \
  'unset AGENT_RUNTIME_TEST_CGROUP_ROOT'
check_file_contains_all 'policy authentication mismatch is unavailable exit 69 with zero downstream work' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  "assert_bash_exit 69 'fetch GET http://11.0.0.10:8080/items' task9-cross-a policy-mismatch" \
  'unavailable/auth' '.requests_total == 0' '.request_bytes == 0' \
  '(.dns_counts | length) == 0' 'policy-mismatch-broker.pcap' \
  '[ ! -s "$policy_capture_text" ]' '"$audit_before" = "$audit_after"'
check_file_contains_all 'deployed graceful SIGTERM locks health-before-cancel, drain, exit, identity, and recreation evidence' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'deployed_graceful_sigterm_lifecycle()' \
  'docker stop --signal SIGTERM --time 15' \
  'com.docker.compose.project' 'resolve_runtime_supervisor_host_pid' \
  'assert active_at_latch' \
  'shutdown_health_latched=1' 'shutdown_acceptance_closed=1' \
  'request_terminated=1' 'binding_session_drained=1' \
  'command_cgroup_removed=1' 'captured_pid_identities_gone=1' \
  'runtime_exited=1' 'runtime_recreated_healthy=1' \
  'broker_identity_unchanged=1'
check_file_excludes 'lifecycle evidence never orders Broker Completion before cgroup removal' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" 'revocation_completion_before_removal'
check_file_excludes 'cgroup observer never polls Broker audit as a cleanup-order oracle' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" 'cleanup_audit_namespace_hash'
check_file_contains_all 'every observed lifecycle requires exact same-stream Runtime drain and cleanup markers in order' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'capture_runtime_lifecycle_markers()' 'command_binding_owned_drain_complete' \
  'command_cgroup_cleanup_complete' 'runtime_drain_marker_count=1' \
  'runtime_cleanup_marker_count=1' 'runtime_marker_order=drain-before-cleanup' \
  'if len(matching) != 1:' 'if observed[other]:' \
  'fields.get("command_cgroup") != command_cgroup' \
  'capture_runtime_lifecycle_markers "$evidence_name" "$observed_command_cgroup"'
check_file_contains_all 'normal, timeout, cancel, syscall, SIGTERM, and Broker-disconnect paths all consume marker receipts' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  "run_observed_lifecycle normal-cleanup 'sleep 2; true'" \
  "run_observed_lifecycle timeout-cleanup 'sleep 10'" \
  'for syscall in setsid setpgid unshare setns; do' \
  "run_observed_lifecycle api-cancel 'sleep 30'" \
  'capture_runtime_lifecycle_markers graceful-sigterm "$observed_command_cgroup" "$runtime_container_id"' \
  'capture_runtime_lifecycle_markers broker-disconnect "$observed_command_cgroup"'
check_file_contains_all 'Broker Completion is checked independently with one bounded five-second eventual wait' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'wait_for_audit_completions()' 'deadline=$((SECONDS + 5))' \
  'audit_eventual_completion=1' 'cancellation_reason' \
  'wait_for_audit_completions command-control-bounds' \
  'wait_for_audit_completions graceful-sigterm' \
  'wait_for_audit_completions broker-disconnect'
check_file_contains_all 'C7 authority creates one runner-owned 0700 test root with exact cleanup ownership' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'c7_test_root="$tmp_dir/c7-tests"' 'mkdir -m 0700 -- "$c7_test_root"' \
  'rmdir -- "$resolved_tmp/c7-tests"'
check_file_contains_all 'C7 compiler independently selects lib or one exact integration target with feature-gated Cargo JSON' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'compile_c7_test_target()' 'case "$selector" in' \
  'lib) selector_args=(--lib)' 'test:*) selector_args=(--test "${selector#test:}")' \
  'cargo test --locked --release --no-run --message-format=json' \
  '--features c7-test-support' '.target.name == $target' \
  '.profile.test == true' '(.executable | type) == "string"' \
  '[ "${#executable_paths[@]}" -eq 1 ]' 'chmod 0555 "$copied"'
check_file_contains_all 'C7 authority target table is exactly lib plus four named integration tests including linux_cgroup' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'compile_required_c7_target lib lib agent_runtime' \
  'compile_required_c7_target linux_exec_helper test:linux_exec_helper linux_exec_helper' \
  'compile_required_c7_target linux_cgroup test:linux_cgroup linux_cgroup' \
  'compile_required_c7_target runtime_fetch_proxy test:runtime_fetch_proxy runtime_fetch_proxy' \
  'compile_required_c7_target fetch_cli test:fetch_cli fetch_cli'
c7_compile_call_count=$(grep -E -c '^compile_required_c7_target ' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" || true)
if [ "$c7_compile_call_count" -eq 5 ]; then
  printf 'ok - C7 authority invokes exactly five independent target compiles\n'
else
  printf 'not ok - C7 authority invokes exactly five independent target compiles\n' >&2
  failures=$((failures + 1))
fi
check_file_excludes 'C7 authority removes the drifting broad artifact resolver' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" 'resolve_required_test_binary()'
check_file_contains_all 'C7 exact runner uses the approved binary-only hardened command and nonzero receipt' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'run_c7_test_exact()' '--network none' '--read-only' '--user 10001:10001' \
  '--cap-drop ALL' '--security-opt no-new-privileges=true' \
  '--pids-limit 64' '--memory 256m' '--memory-swap 256m' '--cpus 1' \
  '--tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m' '--workdir /tmp' \
  '--mount "type=bind,src=$binary,dst=/c7-test,readonly"' \
  '--entrypoint /c7-test "$runtime_test_image_id"' \
  '"$exact_name" --exact --nocapture --test-threads=1' \
  'running 1 test' 'test_run_count=1' 'test_pass_count=1' 'test_result_ok=1'
c7_runner_source=$(awk '/^run_c7_test_exact\(\)/,/^}/' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh")
c7_runner_mount_count=$(printf '%s\n' "$c7_runner_source" | grep -F -c -- '--mount ' || true)
if [ "$c7_runner_mount_count" -eq 1 ] \
  && ! printf '%s\n' "$c7_runner_source" | grep -E \
    'release_root|CARGO_(HOME|TARGET|BIN_EXE)|/src|Docker\.sock|/sys/fs/cgroup|cp /usr/local/bin|/bin/bash|--volume|(^|[[:space:]])-v([[:space:]]|$)' >/dev/null; then
  printf 'ok - C7 exact runner has one test bind and no companion, source, cache, target, socket, cgroup, or shell seam\n'
else
  printf 'not ok - C7 exact runner has one test bind and no companion, source, cache, target, socket, cgroup, or shell seam\n' >&2
  failures=$((failures + 1))
fi
for obsolete_c7_runner_fragment in \
  '--pids-limit 128' '--memory 1g' '--memory-swap 1g' \
  '--tmpfs /tmp:rw,nosuid,nodev,size=64m' \
  '--tmpfs "$release_root:rw,nosuid,nodev,size=64m,mode=1777"' \
  'run_built_test_filter()'; do
  check_file_excludes "C7 matrix rejects obsolete runner fragment $obsolete_c7_runner_fragment" \
    "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" "$obsolete_c7_runner_fragment"
done
check_file_contains_all 'retained C2 and C3 exact tests remain outside the C7 authority runner' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'run_legacy_test_exact()' \
  'run_legacy_test_exact c2-image-preauth-task-metrics fetch_broker preauth_connection_limit_rejects_before_spawning_more_tasks' \
  'run_legacy_test_exact c2-image-handshake-deadline fetch_broker silent_peer_is_closed_at_one_absolute_handshake_deadline' \
  'run_legacy_test_exact c2-image-connect-zero-counters fetch_broker broker_rejects_connect_before_audit_body_dns_or_connector' \
  'run_legacy_test_exact c3-image-drop-before-cancel fetch_cli linux_cli::timeout_drops_inflight_future_before_cancel 1' \
  'run_legacy_test_exact c3-image-nonreader-deadline fetch_cli linux_cli::cancel_is_bounded_when_peer_never_reads 1' \
  'run_legacy_test_exact c3-image-broken-pipe-diagnostic fetch_cli linux_cli::broken_pipe_does_not_emit_a_second_diagnostic 1'

required_c7_exact_count=0
for required_c7_exact_selector in \
  'exec::tests::c7_helper_init_stage_failures_emit_one_exact_record_and_latch' \
  'exec::tests::c7_spawn_preexec_and_config_writer_failures_latch' \
  'exec::tests::c7_helper_status_clean_eof_accepts_target_exit_one_without_latch' \
  'exec::tests::c7_helper_status_timeout_malformed_and_read_failure_latch' \
  'c7_exec_fd_layouts_preserve_config_control_status' \
  'c7_each_three_fd_mapping_stage_failure_aborts_and_latches' \
  'c7_config_writer_thread_creation_failure_latches_and_cleans' \
  'c7_cgroup_create_failure_latches_and_cancels_active' \
  'c7_each_limit_control_write_failure_latches' \
  'c7_cpu_usage_read_and_parse_failure_latch' \
  'c7_enforcement_latch_is_irreversible_but_local_apis_remain' \
  'c7_control_reader_panic_still_drains_guardian' \
  'c7_revoke_phase_blocks_admission_before_guardian_drain' \
  'c7_control_reader_error_receipt_does_not_drop_guardian' \
  'c7_guardian_receipt_mismatch_blocks_cgroup_cleanup' \
  'c7_guardian_timeout_retains_entry_handles_and_joinset' \
  'c7_deferred_cgroup_cleanup_waits_for_shutdown_drain_receipt' \
  'c7_lifecycle_trace_orders_drain_before_cleanup_complete' \
  'c7_cgroup_cleanup_failure_omits_complete_trace_and_latches_health' \
  'c7_output_capacity_path_and_busy_send_one_policy_terminal_exit_65' \
  'c7_output_open_write_file_sync_and_rename_send_one_internal_terminal_exit_70' \
  'c7_runtime_error_is_not_silent_eof_and_terminal_is_never_duplicated' \
  'c7_pre_rename_failure_preserves_old_file_and_returns_70' \
  'c7_post_rename_directory_sync_failure_is_committed_and_latches_shared_health' \
  'c7_command_binding_uses_supervisor_bash_health' \
  'c7_output_policy_and_internal_terminal_errors_map_exact_exit_codes'; do
  required_c7_exact_count=$((required_c7_exact_count + 1))
  selector_count=$(grep -F -c -- "$required_c7_exact_selector" \
    "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" || true)
  if [ "$selector_count" -eq 1 ]; then
    printf 'ok - authoritative C7 selector appears exactly once: %s\n' "$required_c7_exact_selector"
  else
    printf 'not ok - authoritative C7 selector appears exactly once: %s\n' \
      "$required_c7_exact_selector" >&2
    failures=$((failures + 1))
  fi
done
c7_run_call_count=$(grep -E -c '^run_c7_case ' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" || true)
if [ "$required_c7_exact_count" -eq 26 ] && [ "$c7_run_call_count" -eq 26 ]; then
  printf 'ok - C7 authority executes exactly the current 26-selector table with no substitute\n'
else
  printf 'not ok - C7 authority executes exactly the current 26-selector table with no substitute\n' >&2
  failures=$((failures + 1))
fi
for obsolete_c7_selector in \
  'helper_status_accepts_only_clean_eof_or_one_exact_failure_record' \
  'helper_status_rejects_malformed_truncated_multiple_and_timeout' \
  'collision_safe_fd_mapping_covers_config_control_and_status_target_collisions' \
  'syscall_failure_closes_sources_temps_and_installed_targets' \
  'successful_target_exec_closes_status_fd_and_preserves_exit_one' \
  'helper_closes_inherited_fds_and_sets_no_new_privs' \
  'enforcement_failures_latch_one_health_fuse_but_target_exit_one_does_not' \
  'malformed_multiple_and_timed_out_helper_status_latch_health' \
  'guardian_timeout_returns_bounded_and_shutdown_retries_same_retained_handle' \
  'control_reader_panic_is_observed_but_exact_guardian_receipt_authorizes_cleanup' \
  'guardian_receipt_mismatch_retains_entry_and_blocks_cleanup_authorization' \
  'control_reader_error_still_yields_valid_receipt_after_exact_guardian_drain' \
  'guardian_timeout_returns_command_bounded_without_crossing_cleanup_gate' \
  'guardian_receipt_mismatch_blocks_process_cgroup_and_jail_cleanup' \
  'proxy_shutdown_failure_prevents_stale_recovery' \
  'session_admission_and_revoke_are_linearized_in_both_barrier_orders' \
  'cli_output_quota_failure_is_one_policy_error_and_exit_65' \
  'cli_precommit_actual_io_failure_is_one_internal_error_and_exit_70' \
  'postrename_directory_sync_failure_is_committed_visible_and_latches_health' \
  'trace_markers_are_ordered_and_cleanup_failure_omits_second_marker'; do
  check_file_excludes "C7 authority rejects obsolete or substitute selector $obsolete_c7_selector" \
    "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" "$obsolete_c7_selector"
done
check_file_contains_all 'C7 config-writer thread failure executes from the actual linux_exec_helper authority target' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'run_c7_case c7-config-writer-thread linux_exec_helper c7_config_writer_thread_creation_failure_latches_and_cleans'
check_file_contains_all 'C7 FD authority locks all 21 config/control/status install stages instead of one invalid FD' \
  "$REPO_ROOT/agent-runtime/src/exec/spawn/fd_map/c7_test_support.rs" \
  'pub const FD_INSTALL_FAULT_STAGES: [&str; 21]' \
  'pub(in crate::exec) const FD_INSTALL_FAULTS: [FdInstallStage; 21]' \
  'FdInstallStage::Duplicate(FdRole::Config)' 'FdInstallStage::Duplicate(FdRole::Control)' 'FdInstallStage::Duplicate(FdRole::Status)' \
  'FdInstallStage::OriginalClose(FdRole::Config)' 'FdInstallStage::OriginalClose(FdRole::Control)' 'FdInstallStage::OriginalClose(FdRole::Status)' \
  'FdInstallStage::Dup2(FdRole::Config)' 'FdInstallStage::Dup2(FdRole::Control)' 'FdInstallStage::Dup2(FdRole::Status)' \
  'FdInstallStage::GetFd(FdRole::Config)' 'FdInstallStage::GetFd(FdRole::Control)' 'FdInstallStage::GetFd(FdRole::Status)' \
  'FdInstallStage::SetFd(FdRole::Config)' 'FdInstallStage::SetFd(FdRole::Control)' 'FdInstallStage::SetFd(FdRole::Status)' \
  'FdInstallStage::VerifyGetFd(FdRole::Config)' 'FdInstallStage::VerifyGetFd(FdRole::Control)' 'FdInstallStage::VerifyGetFd(FdRole::Status)' \
  'FdInstallStage::TempClose(FdRole::Config)' 'FdInstallStage::TempClose(FdRole::Control)' 'FdInstallStage::TempClose(FdRole::Status)'
check_file_excludes 'C7 FD authority rejects the obsolete const FAULTS table name' \
  "$REPO_ROOT/agent-runtime/src/exec/spawn/fd_map/c7_test_support.rs" 'const FAULTS:'
check_file_contains_all 'C11 FD table builds a fresh production supervisor per stage and records actual lifecycle state' \
  "$REPO_ROOT/agent-runtime/src/exec/c7_test_support/fd_faults.rs" \
  'for (fault, stage) in FD_INSTALL_FAULTS.into_iter().zip(FD_INSTALL_FAULT_STAGES)' \
  'rows.push(run_fault_row(fault, stage).await);' \
  'let supervisor = production_supervisor(cgroups, Some(fault), descriptor_probe.clone());' \
  'let health = supervisor.health();' \
  'let current = supervisor.start_command_with_launch(' \
  'let binding_phase = lifecycle.phase().unwrap().unwrap();' \
  'let binding_registry_entries = proxy.active_binding_count().unwrap();' \
  'let cgroup_cleanup_count = cleanup.fixture_kill_log().len();' \
  'let deferred_cleanup_count = supervisor.deferred_cleanup_count_for_tests().await;' \
  'let reason = health.reason();' \
  'let subsequent = supervisor.start(marker_target(&marker), Vec::new(), Duration::from_secs(1));' \
  'current_command_failed: matches!(current, Err(SupervisorError::Unavailable(_)))' \
  'target_exec_marker: marker.exists()' \
  'health_ready: health.is_ready()' \
  'health_reason_stable: health.reason() == reason' \
  'cgroup_removed: !cgroup_path.exists()' \
  'local_descriptors_released: descriptor_probe.all_released()' \
  'subsequent_bash_rejected: matches!(subsequent, Err(SupervisorError::Unavailable(_)))' \
  'CommandSupervisor::test_production_with_spawn_controls(' \
  'fd_install_fault: fault'
check_file_contains_all 'C11 FD integration test asserts every actual marker, health, binding, cgroup, descriptor, and rejection receipt' \
  "$REPO_ROOT/agent-runtime/tests/linux_exec_helper.rs" \
  'let receipt = agent_runtime::c7_test_support::fd_mapping_fault_table().await;' \
  'assert!(receipt.success_control_marker);' \
  'assert_eq!(receipt.rows.len(), 21);' \
  'agent_runtime::c7_test_support::FD_INSTALL_FAULT_STAGES' \
  'assert!(row.current_command_failed, "{}", row.stage);' \
  'assert!(!row.target_exec_marker, "{}", row.stage);' \
  'assert!(!row.health_ready, "{}", row.stage);' \
  'assert!(row.health_reason_stable, "{}", row.stage);' \
  'agent_runtime::runtime_fetch_proxy::CommandBindingPhase::Drained' \
  'assert_eq!(row.binding_registry_entries, 0, "{}", row.stage);' \
  'assert!(row.cgroup_removed, "{}", row.stage);' \
  'assert_eq!(row.cgroup_cleanup_count, 1, "{}", row.stage);' \
  'assert_eq!(row.deferred_cleanup_count, 0, "{}", row.stage);' \
  'assert!(row.local_descriptors_released, "{}", row.stage);' \
  'assert!(row.subsequent_bash_rejected, "{}", row.stage);'
check_file_contains_all 'C11 FD fault flows from feature-gated SpawnControls through production launch into child pre_exec' \
  "$REPO_ROOT/agent-runtime/src/exec/spawn.rs" \
  '#[cfg(all(feature = "c7-test-support", target_os = "linux"))]' \
  'pub(super) struct SpawnControls {' \
  'pub(super) fd_install_fault: Option<fd_map::FdInstallStage>' \
  'let fd_install_fault = controls.fd_install_fault;' \
  'command.pre_exec(move || {' \
  'if let Some(fault) = fd_install_fault {' \
  'return fd_map::c7_test_support::install_exec_fds_with_fault('
check_file_contains_adjacent 'C11 SpawnControls remains feature-gated at its production fault-control definition' \
  "$REPO_ROOT/agent-runtime/src/exec/spawn.rs" \
  '#[cfg(all(feature = "c7-test-support", target_os = "linux"))]' '#[derive(Clone, Default)]'
check_file_contains_adjacent 'C11 fd_install_fault capture remains feature-gated immediately before pre_exec' \
  "$REPO_ROOT/agent-runtime/src/exec/spawn.rs" \
  '#[cfg(feature = "c7-test-support")]' 'let fd_install_fault = controls.fd_install_fault;'
check_file_contains_adjacent 'C11 injected FD fault branch remains feature-gated inside child pre_exec' \
  "$REPO_ROOT/agent-runtime/src/exec/spawn.rs" \
  '#[cfg(feature = "c7-test-support")]' 'if let Some(fault) = fd_install_fault {'
check_file_contains_all 'C11 FD fixture selects the production backend and carries controls through production prepare' \
  "$REPO_ROOT/agent-runtime/src/exec/supervisor/test_support.rs" \
  'fn test_production_with_spawn_controls(' 'backend: SupervisorBackend::Production {' 'spawn_controls,'
check_file_contains_all 'C11 FD production launch forwards feature-gated controls to the exec helper' \
  "$REPO_ROOT/agent-runtime/src/exec/launch/production.rs" \
  'spawn_controls: &SpawnControls' \
  'let spawn_result = spawn_exec_helper_with_control_and_controls(' \
  'spawn_controls.clone()'
check_file_contains_adjacent 'C11 production launch selects the controlled exec helper only under c7-test-support' \
  "$REPO_ROOT/agent-runtime/src/exec/launch/production.rs" \
  '#[cfg(feature = "c7-test-support")]' 'let spawn_result = spawn_exec_helper_with_control_and_controls('
c11_fd_synthetic_found=false
for c11_fd_synthetic_fragment in \
  'install_exec_fds_with' \
  'BashHealth::ready()' \
  'health.latch_' \
  'current_command_failed: true' \
  'target_exec_marker: false' \
  'health_ready: false' \
  'health_reason_stable: true' \
  'binding_phase: CommandBindingPhase::Drained' \
  'binding_registry_entries: 0' \
  'cgroup_removed: true' \
  'cgroup_cleanup_count: 1' \
  'deferred_cleanup_count: 0' \
  'local_descriptors_released: true' \
  'subsequent_bash_rejected: true'; do
  if grep -F -- "$c11_fd_synthetic_fragment" \
      "$REPO_ROOT/agent-runtime/src/exec/c7_test_support/fd_faults.rs" >/dev/null 2>&1; then
    c11_fd_synthetic_found=true
  fi
done
if [ "$c11_fd_synthetic_found" = false ]; then
  printf 'ok - C11 FD table fixture rejects direct mapper, manual health latch, and fixed receipt substitutes\n'
else
  printf 'not ok - C11 FD table fixture rejects direct mapper, manual health latch, and fixed receipt substitutes\n' >&2
  failures=$((failures + 1))
fi
check_file_contains_all 'C7 guardian and terminal authority rejects zero-session and empty-relay substitutes' \
  "$REPO_ROOT/agent-runtime/tests/runtime_fetch_proxy.rs" \
  'assert_eq!(receipt.live_sessions_before_panic, 2);' \
  'assert_eq!(receipt.drain.guardian.spawned_sessions, 2);' \
  'assert_eq!(receipt.drain.guardian.joined_sessions, 2);' \
  'assert_eq!(receipt.live_sessions_before_revoke, 2);' \
  'assert_eq!(receipt.live_sessions_after_timeout, 2);' \
  'assert_eq!(receipt.guardian_spawned_after_shutdown, 2);' \
  'assert_eq!(receipt.guardian_joined_after_shutdown, 2);' \
  'assert!(receipt.broker_error_then_internal_exactly_once);' \
  'assert!(receipt.writer_unavailable_no_frame);' \
  'relay_failure_after_response_start_is_one_terminal().await;' \
  'broker_error_then_late_bad_frame_is_one_terminal().await;'
check_file_contains_all 'native matrix verifies FD and guardian source authority before compiling exact C7 binaries' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'verify_c7_authority_source_contracts()' \
  'source_excludes_fragments()' 'source_fragments_in_order()' \
  'source_has_contiguous_fragments()' 'source_fragment_count()' \
  'FD_INSTALL_FAULT_STAGES: [&str; 21]' 'FD_INSTALL_FAULTS: [FdInstallStage; 21]' \
  'agent-runtime/src/exec/c7_test_support/fd_faults.rs' \
  'production_supervisor(cgroups, Some(fault), descriptor_probe.clone())' \
  'start_command_with_launch(' 'target_exec_marker: marker.exists()' \
  'health_reason_stable: health.reason() == reason' \
  'binding_registry_entries = proxy.active_binding_count().unwrap()' \
  'cgroup_cleanup_count = cleanup.fixture_kill_log().len()' \
  'deferred_cleanup_count = supervisor.deferred_cleanup_count_for_tests().await' \
  'local_descriptors_released: descriptor_probe.all_released()' \
  'subsequent_bash_rejected: matches!(subsequent, Err(SupervisorError::Unavailable(_)))' \
  'agent-runtime/src/exec/spawn.rs' 'controls.fd_install_fault' \
  'command.pre_exec(move || {' 'install_exec_fds_with_fault(' \
  'BashHealth::ready()' 'current_command_failed: true' 'cgroup_removed: true' \
  'live_sessions_before_panic, 2' 'live_sessions_after_timeout, 2' \
  'broker_error_then_internal_exactly_once' 'broker_error_then_late_bad_frame_is_one_terminal().await'

audit_redaction_source=$(awk '/^audit_redaction\(\)/,/^}/' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh")
if printf '%s\n' "$audit_redaction_source" | grep -F \
    'wait_for_audit_completions audit-redaction "$namespace_hash" "$run_hash" 1 completed' >/dev/null \
  && printf '%s\n' "$audit_redaction_source" | grep -F \
    '$completions[0].cancellation_reason == null' >/dev/null \
  && printf '%s\n' "$audit_redaction_source" | grep -F \
    '$completions[0].rejection_reason == null' >/dev/null \
  && ! printf '%s\n' "$audit_redaction_source" | grep -E \
    'cancellation_reason == "(broken_pipe|client_cancel|client_disconnect|broker_shutdown|timeout)"' >/dev/null; then
  printf 'ok - successful audit redaction completion has null cancellation and rejection without contradictory canceled evidence\n'
else
  printf 'not ok - successful audit redaction completion has null cancellation and rejection without contradictory canceled evidence\n' >&2
  failures=$((failures + 1))
fi

check_file_contains_all 'native enforcement health latch case proves active cancellation, permanent latch, local APIs, and healthy recreation' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'runtime_enforcement_health_latch()' \
  'case_run runtime-enforcement-health-latch ' \
  'enforcement_health_observer()' 'enforcement_health_observer_ready=1' \
  'enforcement_health_observed=1' \
  'enforcement_active_requests=2' 'enforcement_trigger_http_503=1' \
  'enforcement_active_requests_canceled=2' 'enforcement_bash_latched=1' \
  'enforcement_local_apis_ready=1' 'enforcement_runtime_recreated_healthy=1' \
  'wait_cgroup_root_empty'
check_file_contains_all 'native enforcement mutation journals exact identity and arms trap restoration before one scoped chmod' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'restore_enforcement_commands_permissions()' \
  'permission_restore_path=%s' 'permission_restore_device=%s' 'permission_restore_inode=%s' \
  'permission_restore_mode=%s' 'permission_restore_uid=%s' 'permission_restore_gid=%s' \
  'enforcement_restore_pending=1' \
  'chmod "$enforcement_restricted_mode" -- "$enforcement_commands_path"' \
  'chmod "$enforcement_original_mode" -- "$enforcement_commands_path"' \
  'restore_enforcement_commands_permissions || result=1' \
  'restore_enforcement_commands_permissions || cleanup_failed=1'
enforcement_source=$(awk '/^runtime_enforcement_health_latch\(\)/,/^}/' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh")
enforcement_restore_source=$(awk '/^restore_enforcement_commands_permissions\(\)/,/^}/' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh")
cleanup_source=$(awk '/^cleanup\(\)/,/^}/' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh")
arm_line=$(printf '%s\n' "$enforcement_source" | grep -n -F 'enforcement_restore_pending=1' | awk -F: 'NR == 1 {print $1}' || true)
restrict_line=$(printf '%s\n' "$enforcement_source" | grep -n -F 'chmod "$enforcement_restricted_mode" -- "$enforcement_commands_path"' | awk -F: 'NR == 1 {print $1}' || true)
restore_line=$(printf '%s\n' "$enforcement_source" | grep -n -F 'restore_enforcement_commands_permissions || result=1' | awk -F: 'NR == 1 {print $1}' || true)
active_wait_line=$(printf '%s\n' "$enforcement_source" | grep -n -F 'wait "$enforcement_active_pid_a" || return 1' | awk -F: 'NR == 1 {print $1}' || true)
latch_line=$(printf '%s\n' "$enforcement_source" | grep -n -F "grep -F -x 'enforcement_health_observed=1'" | awk -F: 'NR == 1 {print $1}' || true)
first_intact_line=$(printf '%s\n' "$enforcement_source" | grep -n -F 'enforcement_active_groups_intact "$commands" "$active_identity_file"' | awk -F: 'NR == 1 {print $1}' || true)
second_intact_line=$(printf '%s\n' "$enforcement_source" | grep -n -F 'enforcement_active_groups_intact "$commands" "$active_identity_file"' | awk -F: 'NR == 2 {print $1}' || true)
cleanup_restore_line=$(printf '%s\n' "$cleanup_source" | grep -n -F 'restore_enforcement_commands_permissions || cleanup_failed=1' | awk -F: 'NR == 1 {print $1}' || true)
cleanup_stop_line=$(printf '%s\n' "$cleanup_source" | grep -n -F 'stop_backgrounds || cleanup_failed=1' | awk -F: 'NR == 1 {print $1}' || true)
mutation_count=$(printf '%s\n%s\n' "$enforcement_source" "$enforcement_restore_source" | grep -E -c '^[[:space:]]*chmod ' || true)
if [ -n "$arm_line" ] && [ -n "$restrict_line" ] && [ -n "$restore_line" ] && [ -n "$active_wait_line" ] \
  && [ -n "$latch_line" ] && [ -n "$first_intact_line" ] && [ -n "$second_intact_line" ] \
  && [ -n "$cleanup_restore_line" ] && [ -n "$cleanup_stop_line" ] \
  && [ "$arm_line" -lt "$restrict_line" ] && [ "$latch_line" -lt "$first_intact_line" ] \
  && [ "$first_intact_line" -lt "$restore_line" ] && [ "$restore_line" -lt "$second_intact_line" ] \
  && [ "$second_intact_line" -lt "$active_wait_line" ] \
  && [ "$cleanup_restore_line" -lt "$cleanup_stop_line" ] && [ "$mutation_count" -eq 2 ] \
  && ! printf '%s\n%s\n' "$enforcement_source" "$enforcement_restore_source" | grep -E \
    'chmod[[:space:]]+-R|chown|mount|mkdir|rm[[:space:]]+-rf|/sys/fs/cgroup/[^"$]*\*' >/dev/null; then
  printf 'ok - enforcement permission mutation is exact, restoration-first, nonrecursive, and trap-safe\n'
else
  printf 'not ok - enforcement permission mutation is exact, restoration-first, nonrecursive, and trap-safe\n' >&2
  failures=$((failures + 1))
fi
validator_line=$(grep -n -F 'pass_case preflight-host-validator' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" | awk -F: 'NR == 1 {print $1}' || true)
readiness_line=$(grep -n -F "pass_case compose-start 'fixture, Broker, and security-test Runtime are healthy and ready'" \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" | awk -F: 'NR == 1 {print $1}' || true)
enforcement_case_line=$(grep -n -F 'case_run runtime-enforcement-health-latch ' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" | awk -F: 'NR == 1 {print $1}' || true)
exit_trap_line=$(grep -n -F 'trap on_exit EXIT' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" | awk -F: 'NR == 1 {print $1}' || true)
if [ -n "$validator_line" ] && [ -n "$readiness_line" ] && [ -n "$enforcement_case_line" ] \
  && [ -n "$exit_trap_line" ] \
  && [ "$exit_trap_line" -lt "$enforcement_case_line" ] \
  && [ "$validator_line" -lt "$enforcement_case_line" ] \
  && [ "$readiness_line" -lt "$enforcement_case_line" ]; then
  printf 'ok - enforcement mutation case is unreachable before native validator and Runtime readiness\n'
else
  printf 'not ok - enforcement mutation case is unreachable before native validator and Runtime readiness\n' >&2
  failures=$((failures + 1))
fi
enforcement_case_tail=$(awk '
  /case_run runtime-enforcement-health-latch / {capture=1; remaining=5}
  capture && remaining > 0 {print; remaining--}
' "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh")
if printf '%s\n' "$enforcement_case_tail" | grep -F \
    'case_run runtime-enforcement-health-latch ' >/dev/null \
  && printf '%s\n' "$enforcement_case_tail" | grep -F \
    'if [ "$fail_count" -ne 0 ]; then' >/dev/null \
  && printf '%s\n' "$enforcement_case_tail" | grep -F 'exit 1' >/dev/null; then
  printf 'ok - enforcement case failure exits directly into the armed restoration cleanup trap\n'
else
  printf 'not ok - enforcement case failure exits directly into the armed restoration cleanup trap\n' >&2
  failures=$((failures + 1))
fi
check_file_contains_all 'Runtime marker receipt validates the exact typed drain authorization before same-stream cleanup' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'allowed_outcomes = {"completed", "error", "panicked", "cancelled"}' \
  'control_reader_outcome not in allowed_outcomes' \
  'isinstance(spawned_sessions, int)' 'not isinstance(spawned_sessions, bool)' \
  'spawned_sessions == joined_sessions' 'spawned_sessions >= 0' \
  'joinset_empty is True' 'job_channel_closed is True' \
  'runtime_drain_index=' 'runtime_cleanup_index=' \
  'runtime_marker_order=drain-before-cleanup' ': >"$marker_stdout"' ': >"$marker_stderr"'
check_file_contains_all 'audit eventual receipt pairs exact Start and Completion identities with typed terminal and quota fields' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'def nonnegative_integer:' '($starts | length) == $expected' \
  '($completions | length) == $expected' 'request_identity' \
  '.command_id_sha256 | test("^[0-9a-f]{64}$")' \
  '.method == $start.method' '.normalized_origin == $start.normalized_origin' \
  '.policy_version == $start.policy_version' \
  '.request_body_bytes | nonnegative_integer' '.network_bytes | nonnegative_integer' \
  '.decoded_bytes | nonnegative_integer' '.quota.requests_used >= 1' \
  '.quota.concurrent_requests | nonnegative_integer' \
  '.quota.request_bytes_used | nonnegative_integer' \
  '.quota.response_bytes_used | nonnegative_integer' \
  '["broker_shutdown", "broken_pipe", "client_cancel", "client_disconnect", "timeout"]' \
  '$mode == "completed"' '$mode == "canceled"'
check_file_contains_all 'every audit eventual call selects one explicit completed or canceled contract' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'wait_for_audit_completions command-control-bounds "$namespace_hash" "$run_hash" 2 canceled' \
  'wait_for_audit_completions graceful-sigterm "$graceful_namespace_hash" "$graceful_run_hash" 1 canceled' \
  'wait_for_audit_completions broker-disconnect "$broker_disconnect_namespace_hash" "$broker_disconnect_run_hash" 1 canceled' \
  'wait_for_audit_completions control-copy-b "$namespace_hash" "$run_b_hash" 1 completed' \
  'wait_for_audit_completions audit-redaction "$namespace_hash" "$run_hash" 1 completed'
check_file_contains_all 'C7 cleanup-failure exact test locks drain-present cleanup-absent and health false' \
  "$REPO_ROOT/agent-runtime/tests/runtime_fetch_proxy.rs" \
  'async fn c7_cgroup_cleanup_failure_omits_complete_trace_and_latches_health()' \
  'assert_eq!(receipt.events, ["command_binding_owned_drain_complete"]);' \
  'assert!(!receipt.health_ready);'
check_file_contains_all 'real shared quota overlap locks Fetch loser to policy exit 65 and preserves destination/temp invariants' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'if [ "$fetch_success" -eq 0 ]; then' \
  "jq -e '.http_status == 200 and .body.exit_code == 65' \"\$fetch_output\"" \
  '.body.content == "old-fetch-quota.bin"' \
  '.fetch-quota.bin.agent-runtime-????????????????.tmp'
check_file_contains_all 'real precommit output filesystem failure remains internal exit 70 with exact temp cleanup' \
  "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" \
  'fetch --output /workspace/locked/out' 'test "$rc" -eq 70' \
  '.out.agent-runtime-????????????????.tmp'
check_file_contains_all 'fixture exposes only a bounded deterministic byte stream for quota overlap' \
  "$REPO_ROOT/agent-runtime/tests/fixtures/fixture_server.py" \
  'BYTES_MAX_BYTES = bounded_env_int(' 'if path.startswith("/bytes/"):' \
  'elif parsed.path.startswith("/bytes/"):' 'size > BYTES_MAX_BYTES'
check_file_contains_all 'Linux probe and tests lock actual PTRACE_ATTACH argument safety' \
  "$REPO_ROOT/agent-runtime/src/bin/agent-runtime-net-probe.rs" \
  'Some("ptrace-attach")' 'libc::PTRACE_ATTACH' 'libc::PTRACE_DETACH' \
  'ptrace attach unexpectedly succeeded'
check_file_contains_all 'Linux seccomp suite rejects ptrace probe arguments without attaching the test runner' \
  "$REPO_ROOT/agent-runtime/tests/linux_seccomp.rs" \
  'fn ptrace_attach_rejects_invalid_arguments_without_attaching()' \
  'vec!["1", "trailing"]'

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
check_json 'dangerous container privileges and Docker socket are absent' '
  all(.services | to_entries[];
    (.value.privileged // false) != true and
    (all(.value.cap_add[]?; ascii_upcase != "SYS_ADMIN")) and
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
check_json 'runtime has finite nofile and bounded tmpfs' '
  .services["agent-runtime"].ulimits.nofile.soft == 256 and
  .services["agent-runtime"].ulimits.nofile.hard == 4096 and
  any(.services["agent-runtime"].tmpfs[];
    . == "/tmp:size=64m,mode=1777" or
    (.path == "/tmp" and .size == 67108864 and .mode == 1023))'
check_json 'delegated commands sibling uses the canonical host path in Runtime' '
  .services["agent-runtime"].environment as $e |
  $e.AGENT_RUNTIME_CGROUP_AGGREGATE_ROOT == ("/sys/fs/cgroup/" + $cgroup_parent) and
  $e.AGENT_RUNTIME_CGROUP_ROOT == ($e.AGENT_RUNTIME_CGROUP_AGGREGATE_ROOT + "/commands") and
  any(.services["agent-runtime"].volumes[];
    .type == "bind" and .source == $cgroup_host and
    .target == .source and (.read_only // false) == false)'
check_json 'workspace, runtime log, and broker audit bind roots stay separate' '
  ($workspace_host != $runtime_log_host and $workspace_host != $audit_host and $runtime_log_host != $audit_host) and
  any(.services["agent-runtime"].volumes[]; .type == "bind" and .source == $workspace_host and .target == "/runtime/workspaces") and
  any(.services["agent-runtime"].volumes[]; .type == "bind" and .source == $runtime_log_host and .target == "/runtime/logs") and
  any(.services["agent-fetch-broker"].volumes[]; .type == "bind" and .source == $audit_host and .target == "/var/log/agent-fetch")'
check_json 'repository skills are mounted read-only' '
  any(.services["agent-runtime"].volumes[];
    .target == "/runtime/skills" and .read_only == true)'
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
check_security_json 'security override does not add privileged, SYS_ADMIN, Docker socket, or host network' '
  all(.services | to_entries[];
    (.value.privileged // false) != true and
    (.value.network_mode // "") != "host" and
    all(.value.cap_add[]?; ascii_upcase != "SYS_ADMIN") and
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
