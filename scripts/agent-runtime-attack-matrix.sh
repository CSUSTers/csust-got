#!/usr/bin/env bash
set -euo pipefail

umask 077
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
COMPOSE_OVERRIDE="$REPO_ROOT/docker-compose.security-test.yml"
FETCH_OVERLAY="$REPO_ROOT/docker-compose.fetch.yml"
PROJECT_NAME="agent-runtime-task9-$$"
RUST_TEST_CONTAINER="$PROJECT_NAME-rust-tests"
COMPOSE_IMAGE_NAMES=(
  "$PROJECT_NAME-agent-runtime"
  "$PROJECT_NAME-agent-fetch-broker"
  "$PROJECT_NAME-agent-fetch-fixture"
)

pass_count=0
fail_count=0
cleanup_done=0
compose_attempted=0
base_compose_attempted=0
base_runtime_active=0
rust_test_attempted=0
tmp_dir=""
tmp_parent=""
c7_test_root=""
enforcement_restore_pending=0
enforcement_commands_path=""
enforcement_commands_device=""
enforcement_commands_inode=""
enforcement_original_mode=""
enforcement_original_uid=""
enforcement_original_gid=""
enforcement_permission_receipt=""
observed_group=""
observed_group_identity=""
observed_command_cgroup=""
observed_pid_evidence=""
observed_lifecycle_evidence=""
cgroup_observer_pid=""
declare -A background_pid_start=()
declare -A c7_test_binaries=()
declare -A legacy_test_binaries=()
declare -a namespaces=(task9-main task9-cross-a task9-cross-b task9-resource task9-quota task9-rollback task9-enforcement)

pass_case() {
  pass_count=$((pass_count + 1))
  printf 'PASS %-40s %s\n' "$1" "$2"
}

fail_case() {
  fail_count=$((fail_count + 1))
  printf 'FAIL %-40s %s\n' "$1" "$2" >&2
}

dc() {
  docker compose --project-name "$PROJECT_NAME" \
    -f "$REPO_ROOT/docker-compose.yml" -f "$FETCH_OVERLAY" -f "$COMPOSE_OVERRIDE" "$@"
}

dc_base() {
  docker compose --project-name "$PROJECT_NAME" -f "$REPO_ROOT/docker-compose.yml" "$@"
}

runtime_exec() {
  if [ "$base_runtime_active" -eq 1 ]; then
    dc_base exec -T agent-runtime "$@"
  else
    dc exec -T agent-runtime "$@"
  fi
}

runtime_request() {
  method=$1
  endpoint=$2
  payload=$3
  printf '%s' "$payload" | runtime_exec python3 -c '
import json, os, sys, urllib.error, urllib.request
payload = sys.stdin.buffer.read()
method = sys.argv[2]
request = urllib.request.Request(
    "http://127.0.0.1:8080" + sys.argv[1],
    data=payload if method != "GET" else None,
    method=method,
    headers={"Authorization": "Bearer " + os.environ["AGENT_RUNTIME_TOKEN"], "Content-Type": "application/json"},
)
try:
    response = urllib.request.urlopen(request, timeout=40)
    status = response.status
    raw = response.read()
except urllib.error.HTTPError as error:
    status = error.code
    raw = error.read()
try:
    body = json.loads(raw)
except Exception:
    body = {"raw_sha256": __import__("hashlib").sha256(raw).hexdigest(), "raw_bytes": len(raw)}
print(json.dumps({"http_status": status, "body": body}, separators=(",", ":")))
' "$endpoint" "$method"
}

runtime_post() {
  runtime_request POST "$1" "$2"
}

runtime_get() {
  runtime_request GET "$1" ''
}

bash_response() {
  command=$1
  namespace=${2:-task9-main}
  run_id=${3:-matrix-run}
  timeout_value=${4:-30s}
  cwd=${5:-/workspace}
  payload=$(jq -nc \
    --arg namespace "$namespace" \
    --arg run_id "$run_id" \
    --arg command "$command" \
    --arg timeout "$timeout_value" \
    --arg cwd "$cwd" \
    '{namespace:$namespace,run_id:$run_id,cwd:$cwd,command:$command,timeout:$timeout}')
  runtime_post /v1/bash "$payload"
}

reset_namespace() {
  namespace=$1
  payload=$(jq -nc --arg namespace "$namespace" '{namespace:$namespace,run_id:"matrix-cleanup",cwd:"/workspace"}')
  runtime_post /v1/reset "$payload"
}

new_artifact() {
  name=$1
  case "$name" in
    ''|*/*|.|..|.*/*) return 1 ;;
  esac
  path="$tmp_dir/$name"
  printf 'file\t%s\n' "$path" >>"$tmp_dir/cleanup.journal"
  printf '%s\n' "$path"
}

new_c7_artifact() {
  name=$1
  case "$name" in
    ''|*/*|.|..|.*/*) return 1 ;;
  esac
  [ -n "$c7_test_root" ] && [ -d "$c7_test_root" ] || return 1
  path="$c7_test_root/$name"
  printf 'file\t%s\n' "$path" >>"$tmp_dir/cleanup.journal"
  printf '%s\n' "$path"
}

track_pid() {
  pid=$1
  start=$(awk '{print $22}' "/proc/$pid/stat" 2>/dev/null || true)
  [ -n "$start" ] || return 1
  background_pid_start["$pid"]=$start
  printf 'pid\t%s\t%s\n' "$pid" "$start" >>"$tmp_dir/cleanup.journal"
}

untrack_pid() {
  pid=$1
  unset 'background_pid_start[$pid]'
}

pid_is_tracked_process() {
  pid=$1
  expected=${background_pid_start[$pid]-}
  [ -n "$expected" ] || return 1
  actual=$(awk '{print $22}' "/proc/$pid/stat" 2>/dev/null || true)
  [ "$actual" = "$expected" ]
}

run_logged() {
  name=$1
  shift
  log_file=$(new_artifact "$name.log")
  if "$@" >"$log_file" 2>&1; then
    pass_case "$name" 'completed with exit 0'
    return 0
  else
    status=$?
  fi
  fail_case "$name" "command exited $status (transient log journaled for cleanup)"
  return 1
}

case_run() {
  name=$1
  description=$2
  shift 2
  if "$@"; then
    pass_case "$name" "$description"
    return 0
  fi
  fail_case "$name" "$description"
  return 1
}

runtime_health_json() {
  docker inspect agent-runtime | jq -cer '
    .[0].State.Health as $health |
    ($health.Log | map(.Output) | map(select(startswith("{"))) | last | fromjson) +
    {docker_health:$health.Status}'
}

wait_container_running() {
  container=$1
  attempts=${2:-30}
  while [ "$attempts" -gt 0 ]; do
    if [ "$(docker inspect -f '{{.State.Running}}' "$container" 2>/dev/null || true)" = true ]; then
      return 0
    fi
    attempts=$((attempts - 1))
    sleep 1
  done
  return 1
}

wait_container_health() {
  container=$1
  expected=$2
  attempts=${3:-40}
  while [ "$attempts" -gt 0 ]; do
    actual=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$container" 2>/dev/null || true)
    if [ "$actual" = "$expected" ]; then
      return 0
    fi
    attempts=$((attempts - 1))
    sleep 1
  done
  return 1
}

fixture_state() {
  dc exec -T agent-fetch-fixture python3 -c \
    'import urllib.request; print(urllib.request.urlopen("http://127.0.0.1:8080/__state", timeout=2).read().decode())'
}

fixture_reset() {
  dc exec -T agent-fetch-fixture python3 -c \
    'import urllib.request; r=urllib.request.Request("http://127.0.0.1:8080/__reset", data=b"", method="POST"); urllib.request.urlopen(r, timeout=2).read()'
}

cgroup_root_empty() {
  [ -z "$(find "$AGENT_RUNTIME_CGROUP_HOST_ROOT" -mindepth 1 -maxdepth 1 -type d -print -quit)" ]
}

wait_cgroup_root_empty() {
  attempts=100
  while [ "$attempts" -gt 0 ]; do
    if cgroup_root_empty; then
      return 0
    fi
    attempts=$((attempts - 1))
    sleep 0.1
  done
  return 1
}

wait_new_cgroup() {
  attempts=100
  while [ "$attempts" -gt 0 ]; do
    group=$(find "$AGENT_RUNTIME_CGROUP_HOST_ROOT" -mindepth 1 -maxdepth 1 -type d -print -quit)
    if [ -n "$group" ]; then
      printf '%s\n' "$group"
      return 0
    fi
    attempts=$((attempts - 1))
    sleep 0.05
  done
  return 1
}

capture_active_command_cgroup() {
  evidence_name=$1
  cgroup_root=$(realpath -e -- "$AGENT_RUNTIME_CGROUP_HOST_ROOT" 2>/dev/null || true)
  candidate=$(wait_new_cgroup) || return 1
  observed_group=$(realpath -e -- "$candidate" 2>/dev/null || true)
  [ -n "$cgroup_root" ] && [ -n "$observed_group" ] || return 1
  [ "$(dirname -- "$observed_group")" = "$cgroup_root" ] || return 1
  observed_group_identity=$(stat -Lc '%d:%i' "$observed_group" 2>/dev/null || true)
  [ -n "$observed_group_identity" ] || return 1
  observed_command_cgroup=$(basename -- "$observed_group") || return 1
  [ -n "$observed_command_cgroup" ] && [ "$observed_command_cgroup" != . ] \
    && [ "$observed_command_cgroup" != .. ] || return 1
  active_populated=$(awk '$1 == "populated" {print $2}' "$observed_group/cgroup.events" 2>/dev/null || true)
  [ "$active_populated" = 1 ] || return 1

  observed_pid_evidence=$(new_artifact "$evidence_name-cgroup-pids.tsv")
  observed_lifecycle_evidence=$(new_artifact "$evidence_name-cgroup-lifecycle.txt")
  pid_count=0
  while IFS= read -r child_pid; do
    case "$child_pid" in ''|*[!0-9]*) return 1 ;; esac
    child_start=$(awk '{print $22}' "/proc/$child_pid/stat" 2>/dev/null || true)
    [ -n "$child_start" ] || return 1
    printf '%s\t%s\n' "$child_pid" "$child_start" >>"$observed_pid_evidence"
    pid_count=$((pid_count + 1))
  done <"$observed_group/cgroup.procs"
  [ "$pid_count" -gt 0 ] || return 1
  printf 'group=%s\nidentity=%s\ncommand_cgroup=%s\nactive_populated=1\nactive_pid_count=%s\n' \
    "$observed_group" "$observed_group_identity" "$observed_command_cgroup" "$pid_count" >"$observed_lifecycle_evidence"
}

observe_command_cgroup_cleanup() {
  observed_populated_zero=0
  observed_empty_procs=0
  observed_removed=0
  attempts=200
  while [ "$attempts" -gt 0 ]; do
    if [ -d "$observed_group" ]; then
      current_identity=$(stat -Lc '%d:%i' "$observed_group" 2>/dev/null || true)
      [ "$current_identity" = "$observed_group_identity" ] || return 1
      populated=$(awk '$1 == "populated" {print $2}' "$observed_group/cgroup.events" 2>/dev/null || true)
      processes=$(cat "$observed_group/cgroup.procs" 2>/dev/null || true)
      if [ "$populated" = 0 ]; then
        observed_populated_zero=1
      fi
      if [ -z "$processes" ]; then
        observed_empty_procs=1
      fi
    else
      observed_removed=1
      break
    fi
    attempts=$((attempts - 1))
    sleep 0.05
  done
  [ "$observed_removed" -eq 1 ] || return 1

  while IFS=$'\t' read -r child_pid child_start; do
    current_start=$(awk '{print $22}' "/proc/$child_pid/stat" 2>/dev/null || true)
    [ "$current_start" != "$child_start" ] || return 1
  done <"$observed_pid_evidence"

  if [ "$observed_populated_zero" -eq 1 ] && [ "$observed_empty_procs" -eq 1 ]; then
    completion_evidence=observed-populated-zero-and-empty-procs
  else
    completion_evidence=kernel-removal-of-captured-active-cgroup-with-no-tracked-pid
  fi
  printf 'observed_populated_zero=%s\nobserved_empty_procs=%s\ndirectory_removed=1\ncompletion_evidence=%s\n' \
    "$observed_populated_zero" "$observed_empty_procs" "$completion_evidence" >>"$observed_lifecycle_evidence"
}

start_cgroup_cleanup_observer() {
  observe_command_cgroup_cleanup &
  cgroup_observer_pid=$!
  if track_pid "$cgroup_observer_pid"; then
    return 0
  fi
  kill -TERM "$cgroup_observer_pid" 2>/dev/null || true
  wait "$cgroup_observer_pid" 2>/dev/null || true
  cgroup_observer_pid=""
  return 1
}

finish_cgroup_cleanup_observer() {
  [ -n "$cgroup_observer_pid" ] || return 1
  observer_result=0
  wait "$cgroup_observer_pid" || observer_result=1
  untrack_pid "$cgroup_observer_pid"
  cgroup_observer_pid=""
  [ "$observer_result" -eq 0 ]
}

capture_runtime_lifecycle_markers() {
  evidence_name=$1
  command_cgroup=$2
  runtime_container=${3:-}
  if [ -z "$runtime_container" ]; then
    runtime_container=$(dc ps -q agent-runtime 2>/dev/null || true)
    runtime_container=$(docker inspect -f '{{.Id}}' "$runtime_container" 2>/dev/null || true)
  fi
  [[ "$runtime_container" =~ ^[0-9a-f]{64}$ ]] || return 1
  marker_stdout=$(new_artifact "$evidence_name-runtime-stdout.jsonl") || return 1
  marker_stderr=$(new_artifact "$evidence_name-runtime-stderr.jsonl") || return 1
  marker_receipt=$(new_artifact "$evidence_name-runtime-markers.txt") || return 1
  deadline=$((SECONDS + 5))
  while [ "$SECONDS" -lt "$deadline" ]; do
    timeout 2s docker logs "$runtime_container" >"$marker_stdout" 2>"$marker_stderr" || return 1
    if "$python_for_syntax" - "$command_cgroup" "$marker_stdout" "$marker_stderr" "$marker_receipt" <<'PY'
import json
import pathlib
import sys

command_cgroup = sys.argv[1]
streams = (("stdout", pathlib.Path(sys.argv[2])), ("stderr", pathlib.Path(sys.argv[3])))
receipt = pathlib.Path(sys.argv[4])
markers = {
    "command_binding_owned_drain_complete": "drain",
    "command_cgroup_cleanup_complete": "cleanup",
}
allowed_outcomes = {"completed", "error", "panicked", "cancelled"}
observed = {}
for stream_name, path in streams:
    records = []
    for index, raw in enumerate(path.read_text(encoding="utf-8", errors="strict").splitlines()):
        try:
            record = json.loads(raw)
        except json.JSONDecodeError:
            continue
        fields = record.get("fields", record)
        if not isinstance(fields, dict) or fields.get("command_cgroup") != command_cgroup:
            continue
        marker = markers.get(fields.get("message"))
        if marker is not None:
            records.append((index, marker, fields))
    observed[stream_name] = records

matching = []
for stream_name, records in observed.items():
    drains = [(index, fields) for index, marker, fields in records if marker == "drain"]
    cleanups = [(index, fields) for index, marker, fields in records if marker == "cleanup"]
    if len(drains) != 1 or len(cleanups) != 1 or drains[0][0] >= cleanups[0][0]:
        continue
    drain_fields = drains[0][1]
    control_reader_outcome = drain_fields.get("control_reader_outcome")
    spawned_sessions = drain_fields.get("spawned_sessions")
    joined_sessions = drain_fields.get("joined_sessions")
    joinset_empty = drain_fields.get("joinset_empty")
    job_channel_closed = drain_fields.get("job_channel_closed")
    if control_reader_outcome not in allowed_outcomes:
        continue
    if not (
        isinstance(spawned_sessions, int)
        and not isinstance(spawned_sessions, bool)
        and isinstance(joined_sessions, int)
        and not isinstance(joined_sessions, bool)
        and spawned_sessions == joined_sessions
        and spawned_sessions >= 0
        and joinset_empty is True
        and job_channel_closed is True
    ):
        continue
    matching.append(
        (
            stream_name,
            drains[0][0],
            cleanups[0][0],
            control_reader_outcome,
            spawned_sessions,
            joined_sessions,
        )
    )
if len(matching) != 1:
    raise SystemExit(1)
selected, drain_index, cleanup_index, outcome, spawned, joined = matching[0]
other = "stderr" if selected == "stdout" else "stdout"
if observed[other]:
    raise SystemExit(1)
receipt.write_text(
    f"runtime_marker_stream={selected}\n"
    f"runtime_control_reader_outcome={outcome}\n"
    f"runtime_spawned_sessions={spawned}\n"
    f"runtime_joined_sessions={joined}\n"
    "runtime_joinset_empty=true\n"
    "runtime_job_channel_closed=true\n"
    "runtime_drain_marker_count=1\n"
    "runtime_cleanup_marker_count=1\n"
    f"runtime_drain_index={drain_index}\n"
    f"runtime_cleanup_index={cleanup_index}\n"
    "runtime_marker_order=drain-before-cleanup\n",
    encoding="utf-8",
)
PY
    then
      cat "$marker_receipt" >>"$observed_lifecycle_evidence"
      : >"$marker_stdout"
      : >"$marker_stderr"
      return 0
    fi
    sleep 0.05
  done
  return 1
}

wait_for_audit_completions() {
  [ "$#" -eq 5 ] || return 1
  evidence_name=$1
  namespace_hash=$2
  run_hash=$3
  expected_count=$4
  completion_mode=$5
  case "$completion_mode" in completed|canceled) ;; *) return 1 ;; esac
  audit_receipt=$(new_artifact "$evidence_name-audit-eventual.txt") || return 1
  deadline=$((SECONDS + 5))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if timeout 1s jq -s -e --arg namespace "$namespace_hash" --arg run "$run_hash" \
      --argjson expected "$expected_count" --arg mode "$completion_mode" '
        def nonnegative_integer:
          type == "number" and floor == . and . >= 0;
        def digest:
          type == "string" and test("^[0-9a-f]{64}$");
        def request_identity:
          {
            command_id_sha256,
            method,
            normalized_origin,
            policy_version,
            query_byte_len,
            query_sha256,
            request_body_byte_len,
            sensitive_headers
          };
        [.[] | select(.namespace_sha256 == $namespace and .run_id_sha256 == $run)] as $records |
        [$records[] | select(.event == "start")] as $starts |
        [$records[] | select(.event == "completion")] as $completions |
        ($namespace | digest) and ($run | digest) and
        ($records | length) == ($expected * 2) and
        ($starts | length) == $expected and
        ($completions | length) == $expected and
        all($records[]; .event == "start" or .event == "completion") and
        ($starts | map(request_identity) | sort_by(tojson)) ==
          ($completions | map(request_identity) | sort_by(tojson)) and
        all($starts[];
          (.command_id_sha256 | test("^[0-9a-f]{64}$")) and
          (.query_sha256 | digest) and
          (.request_body_sha256 | digest) and
          (.query_byte_len | nonnegative_integer) and
          (.request_body_byte_len | nonnegative_integer) and
          all(.sensitive_headers[]?;
            (.byte_len | nonnegative_integer) and (.sha256 | digest)
          )
        ) and
        all($starts[];
          . as $start |
          any($completions[];
            .command_id_sha256 == $start.command_id_sha256 and
            .method == $start.method and
            .normalized_origin == $start.normalized_origin and
            .policy_version == $start.policy_version
          )
        ) and
        all($completions[];
          (.command_id_sha256 | digest) and
          (.query_sha256 | digest) and
          (.request_body_sha256 | digest) and
          (.request_body_bytes | nonnegative_integer) and
          (.network_bytes | nonnegative_integer) and
          (.decoded_bytes | nonnegative_integer) and
          (.duration_ms | nonnegative_integer) and
          (.quota | type) == "object" and
          (.quota.requests_used | nonnegative_integer) and
          .quota.requests_used >= 1 and
          (.quota.concurrent_requests | nonnegative_integer) and
          (.quota.request_bytes_used | nonnegative_integer) and
          (.quota.response_bytes_used | nonnegative_integer) and
          if $mode == "completed" then
            .cancellation_reason == null and .rejection_reason == null
          elif $mode == "canceled" then
            .rejection_reason == null and
            (.cancellation_reason as $reason |
              ["broker_shutdown", "broken_pipe", "client_cancel", "client_disconnect", "timeout"] |
              index($reason) != null)
          else
            false
          end
        )
      ' "$AGENT_FETCH_AUDIT_HOST_ROOT/audit.jsonl" >/dev/null 2>&1; then
      printf 'audit_eventual_completion=1\naudit_completion_count=%s\naudit_completion_mode=%s\n' \
        "$expected_count" "$completion_mode" >"$audit_receipt"
      return 0
    fi
    sleep 0.05
  done
  return 1
}

assert_bash_success() {
  command=$1
  namespace=${2:-task9-main}
  run_id=${3:-matrix-run}
  timeout_value=${4:-30s}
  cwd=${5:-/workspace}
  response=$(bash_response "$command" "$namespace" "$run_id" "$timeout_value" "$cwd") || return 1
  printf '%s' "$response" | jq -e '.http_status == 200 and .body.exit_code == 0' >/dev/null
}

assert_bash_exit() {
  expected=$1
  command=$2
  namespace=${3:-task9-main}
  run_id=${4:-matrix-run}
  timeout_value=${5:-30s}
  cwd=${6:-/workspace}
  response=$(bash_response "$command" "$namespace" "$run_id" "$timeout_value" "$cwd") || return 1
  printf '%s' "$response" | jq -e --argjson expected "$expected" \
    '.http_status == 200 and .body.exit_code == $expected' >/dev/null
}

start_capture() {
  container=$1
  output=$2
  filter=$3
  container_pid=$(docker inspect -f '{{.State.Pid}}' "$container")
  capture_log=$(new_artifact "$(basename "$output").capture.log")
  nsenter -t "$container_pid" -n tcpdump -U -i any -nn -w "$output" "$filter" >"$capture_log" 2>&1 &
  capture_pid=$!
  track_pid "$capture_pid"
  sleep 1
  kill -0 "$capture_pid" 2>/dev/null || return 1
}

stop_backgrounds() {
  stop_failed=0
  for pid in "${!background_pid_start[@]}"; do
    if pid_is_tracked_process "$pid"; then
      kill -INT "$pid" 2>/dev/null || true
    elif [ -e "/proc/$pid" ]; then
      stop_failed=1
    fi
  done
  for pid in "${!background_pid_start[@]}"; do
    if pid_is_tracked_process "$pid"; then
      wait "$pid" 2>/dev/null || true
    fi
    untrack_pid "$pid"
  done
  [ "$stop_failed" -eq 0 ]
}

all_backgrounds_alive() {
  [ "${#background_pid_start[@]}" -gt 0 ] || return 1
  for pid in "${!background_pid_start[@]}"; do
    pid_is_tracked_process "$pid" || return 1
  done
}

restore_enforcement_commands_permissions() {
  [ "$enforcement_restore_pending" -eq 1 ] || return 0
  [ -n "$enforcement_commands_path" ] && [ -n "$enforcement_original_mode" ] || return 1
  current_path=$(realpath -e -- "$enforcement_commands_path" 2>/dev/null || true)
  [ "$current_path" = "$enforcement_commands_path" ] || return 1
  [ "$(stat -Lc '%d' -- "$enforcement_commands_path" 2>/dev/null || true)" = "$enforcement_commands_device" ] || return 1
  [ "$(stat -Lc '%i' -- "$enforcement_commands_path" 2>/dev/null || true)" = "$enforcement_commands_inode" ] || return 1
  [ "$(stat -Lc '%u' -- "$enforcement_commands_path" 2>/dev/null || true)" = "$enforcement_original_uid" ] || return 1
  [ "$(stat -Lc '%g' -- "$enforcement_commands_path" 2>/dev/null || true)" = "$enforcement_original_gid" ] || return 1
  chmod "$enforcement_original_mode" -- "$enforcement_commands_path" || return 1
  [ "$(stat -Lc '%a' -- "$enforcement_commands_path" 2>/dev/null || true)" = "$enforcement_original_mode" ] || return 1
  setpriv --reuid=10001 --regid=10001 --clear-groups /usr/bin/test -w "$enforcement_commands_path" || return 1
  enforcement_restore_pending=0
  if [ -n "$enforcement_permission_receipt" ]; then
    printf 'permission_restored=1\n' >>"$enforcement_permission_receipt" || return 1
  fi
}

cleanup() {
  [ "$cleanup_done" -eq 0 ] || return 0
  cleanup_done=1
  cleanup_failed=0
  set +e

  restore_enforcement_commands_permissions || cleanup_failed=1
  stop_backgrounds || cleanup_failed=1
  if [ "$rust_test_attempted" -eq 1 ]; then
    if docker container inspect "$RUST_TEST_CONTAINER" >/dev/null 2>&1; then
      owned_test_container=$(docker container inspect -f '{{index .Config.Labels "org.csusters.agent-runtime.task9-test-project"}}' "$RUST_TEST_CONTAINER" 2>/dev/null || true)
      if [ "$owned_test_container" = "$PROJECT_NAME" ]; then
        docker container rm -f "$RUST_TEST_CONTAINER" >/dev/null 2>&1 || cleanup_failed=1
      else
        cleanup_failed=1
      fi
    fi
  fi
  if [ "$compose_attempted" -eq 1 ]; then
    dc down --volumes --remove-orphans --rmi local --timeout 10 >/dev/null 2>&1 || cleanup_failed=1
    for container in agent-runtime agent-fetch-broker agent-fetch-fixture; do
      docker container inspect "$container" >/dev/null 2>&1 && cleanup_failed=1
    done
    for network in bot-runtime-control fetch-egress; do
      docker network inspect "$network" >/dev/null 2>&1 && cleanup_failed=1
    done
    docker volume inspect fetch-socket >/dev/null 2>&1 && cleanup_failed=1
    for image in "${COMPOSE_IMAGE_NAMES[@]}"; do
      docker image inspect "$image" >/dev/null 2>&1 && cleanup_failed=1
    done
  fi
  if [ "$base_compose_attempted" -eq 1 ]; then
    dc_base down --volumes --remove-orphans --rmi local --timeout 10 >/dev/null 2>&1 || cleanup_failed=1
  fi

  if [ -n "$tmp_dir" ] && [ -d "$tmp_dir" ]; then
    resolved_tmp=$(realpath -e -- "$tmp_dir" 2>/dev/null || true)
    case "$resolved_tmp" in
      "$tmp_parent"/agent-runtime-task9.*) ;;
      *) cleanup_failed=1; resolved_tmp='' ;;
    esac
    if [ -n "$resolved_tmp" ]; then
      journal="$resolved_tmp/cleanup.journal"
      if [ -f "$journal" ]; then
        while IFS=$'\t' read -r kind path _rest; do
          [ "$kind" = file ] || continue
          case "$path" in
            "$resolved_tmp"/*) rm -f -- "$path" || cleanup_failed=1 ;;
            *) cleanup_failed=1 ;;
          esac
        done <"$journal"
        if [ -d "$resolved_tmp/c7-tests" ] && [ ! -L "$resolved_tmp/c7-tests" ]; then
          rmdir -- "$resolved_tmp/c7-tests" || cleanup_failed=1
        else
          cleanup_failed=1
        fi
        rm -f -- "$journal" || cleanup_failed=1
      else
        cleanup_failed=1
      fi
      rmdir -- "$resolved_tmp" || cleanup_failed=1
    fi
  fi

  set -e
  [ "$cleanup_failed" -eq 0 ]
}

on_exit() {
  original_status=$?
  trap - EXIT HUP INT TERM
  if cleanup; then
    pass_case matrix-cleanup 'all runner-owned PIDs, Compose resources, and exact temporary artifacts removed'
  else
    fail_case matrix-cleanup 'one or more journaled resources could not be removed exactly'
  fi
  printf 'SUMMARY pass=%s fail=%s skipped=0\n' "$pass_count" "$fail_count"
  if [ "$original_status" -ne 0 ] || [ "$fail_count" -ne 0 ]; then
    exit 1
  fi
  exit 0
}

trap on_exit EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

required_files=(
  "$COMPOSE_OVERRIDE"
  "$FETCH_OVERLAY"
  "$REPO_ROOT/agent-runtime/tests/fixtures/Dockerfile"
  "$REPO_ROOT/agent-runtime/tests/fixtures/fixture_server.py"
  "$REPO_ROOT/scripts/test-agent-runtime-compose.sh"
  "$REPO_ROOT/scripts/validate-agent-runtime-host.sh"
)
wiring_missing=()
for path in "${required_files[@]}"; do
  [ -f "$path" ] || wiring_missing+=("${path#"$REPO_ROOT/"}")
done
if [ "${#wiring_missing[@]}" -ne 0 ]; then
  fail_case security-override-wiring "missing: ${wiring_missing[*]}"
  exit 1
fi
if grep -F -- 'FIXTURE_MAX_REQUEST_BODY_BYTES: "8388608"' "$COMPOSE_OVERRIDE" >/dev/null 2>&1 \
  && grep -F -- 'FIXTURE_MAX_EVENTS: "256"' "$COMPOSE_OVERRIDE" >/dev/null 2>&1 \
  && grep -F -- 'FIXTURE_STREAM_MAX_BYTES: "67108864"' "$COMPOSE_OVERRIDE" >/dev/null 2>&1 \
  && grep -F -- 'FIXTURE_BYTES_MAX_BYTES: "8388608"' "$COMPOSE_OVERRIDE" >/dev/null 2>&1; then
  pass_case security-override-wiring 'all Task 9 files and explicit fixture safety-cap wiring exist'
else
  fail_case security-override-wiring 'security-test override lacks explicit fixture body, event, or stream safety-cap wiring'
  exit 1
fi

local_gate_start_failures=$fail_count
missing_gate_tools=()
for tool in awk bash cargo docker go grep jq mkdir mktemp realpath rm rmdir sed timeout; do
  command -v "$tool" >/dev/null 2>&1 || missing_gate_tools+=("$tool")
done
python_for_syntax=""
for candidate in python3 python; do
  if command -v "$candidate" >/dev/null 2>&1 \
    && "$candidate" -c 'import pathlib' >/dev/null 2>&1; then
    python_for_syntax=$candidate
    break
  fi
done
if [ -z "$python_for_syntax" ] && command -v uv >/dev/null 2>&1; then
  uv_python=$(uv python find 2>/dev/null || true)
  uv_python=${uv_python//\\//}
  if [ -n "$uv_python" ] && [ -x "$uv_python" ] \
    && "$uv_python" -c 'import pathlib' >/dev/null 2>&1; then
    python_for_syntax=$uv_python
  fi
fi
[ -n "$python_for_syntax" ] || missing_gate_tools+=("python3-or-python")
if [ "${#missing_gate_tools[@]}" -eq 0 ]; then
  pass_case preflight-local-gate-tools 'all preinstalled tools for Tasks 1-8 unit and static gates are present'
else
  fail_case preflight-local-gate-tools "missing preinstalled gate tools: ${missing_gate_tools[*]}"
  exit 1
fi

if docker compose version >/dev/null 2>&1; then
  pass_case preflight-compose-v2 'Docker Compose v2 is available'
else
  fail_case preflight-compose-v2 'Docker Compose v2 must already be installed and reachable'
  exit 1
fi

tmp_parent=$(realpath -e -- "${TMPDIR:-/tmp}" 2>/dev/null || true)
if [ -z "$tmp_parent" ] || [ ! -d "$tmp_parent" ] \
  || [[ "$tmp_parent" == *$'\n'* ]] || [[ "$tmp_parent" == *$'\t'* ]]; then
  fail_case preflight-temp-root 'the existing OS temporary directory could not be resolved safely'
  exit 1
fi
tmp_dir=$(mktemp -d "$tmp_parent/agent-runtime-task9.XXXXXXXX")
printf 'project\t%s\n' "$PROJECT_NAME" >"$tmp_dir/cleanup.journal"
printf 'compose\t%s\n' "$COMPOSE_OVERRIDE" >>"$tmp_dir/cleanup.journal"
c7_test_root="$tmp_dir/c7-tests"
mkdir -m 0700 -- "$c7_test_root"
pass_case preflight-temp-root 'runner-owned temporary journal root was created with an exact generated path'

run_logged runner-bash-syntax timeout 1m bash -n "$REPO_ROOT/scripts/agent-runtime-attack-matrix.sh" || true
run_logged static-shell-syntax timeout 1m sh -n "$REPO_ROOT/scripts/test-agent-runtime-compose.sh" || true
run_logged validator-shell-syntax timeout 1m sh -n "$REPO_ROOT/scripts/validate-agent-runtime-host.sh" || true
run_logged fixture-python-syntax timeout 1m "$python_for_syntax" -c \
  'import pathlib,sys; path=pathlib.Path(sys.argv[1]); compile(path.read_bytes(), str(path), "exec")' \
  "$REPO_ROOT/agent-runtime/tests/fixtures/fixture_server.py" || true
run_logged rust-format timeout 5m cargo fmt --manifest-path "$REPO_ROOT/agent-runtime/Cargo.toml" -- --check || true
run_logged task1-6-rust-security timeout 30m cargo test --manifest-path "$REPO_ROOT/agent-runtime/Cargo.toml" --release --all-targets || true
run_logged rust-release-binaries timeout 20m cargo build --manifest-path "$REPO_ROOT/agent-runtime/Cargo.toml" --release --bins || true
run_logged task8-go-fetch-gate timeout 5m go test ./config ./agent -run 'TestAgentV3Fetch|TestRemoteBashToolDocuments|TestBuildAgentV3StablePrefix' -count=1 || true
run_logged task8-go-race timeout 30m go test -race -covermode=atomic -short ./... || true
run_logged go-build timeout 10m go build ./... || true
run_logged task7-static-compose timeout 5m bash "$REPO_ROOT/scripts/test-agent-runtime-compose.sh" || true
if [ "$fail_count" -ne "$local_gate_start_failures" ]; then
  fail_case tasks1-8-local-gate 'one or more Tasks 1-8 unit, release, syntax, or static assertions failed; host acceptance is forbidden'
  exit 1
fi
pass_case tasks1-8-local-gate 'all Tasks 1-8 unit, release, syntax, Go, and static assertions passed before host validation'

preflight_start_failures=$fail_count
host_os=$(uname -s 2>/dev/null || printf unknown)
if [ "$host_os" = Linux ]; then
  pass_case preflight-native-linux 'native Linux kernel detected'
else
  fail_case preflight-native-linux "requires native Linux; detected $host_os (Docker Desktop, WSL, VMs, and remote substitutes are not receipts)"
fi

if [ "$(id -u 2>/dev/null || printf -1)" = 0 ]; then
  pass_case preflight-root-inspection 'root is available for read-only namespace/cgroup/nft inspection'
else
  fail_case preflight-root-inspection 'run as root; the runner does not elevate itself'
fi

missing_host_tools=()
for tool in basename cat chmod cp df find id mountpoint nft nsenter ps python3 setpriv sha256sum stat tcpdump tr uname wc; do
  command -v "$tool" >/dev/null 2>&1 || missing_host_tools+=("$tool")
done
if [ "${#missing_host_tools[@]}" -eq 0 ]; then
  pass_case preflight-host-tools 'jq, nft, nsenter, tcpdump, and all read-only host inspection tools are present'
else
  fail_case preflight-host-tools "missing preinstalled host tools: ${missing_host_tools[*]}"
fi

if docker info >/dev/null 2>&1; then
  pass_case preflight-docker-engine 'Docker engine is reachable without changing daemon configuration'
else
  fail_case preflight-docker-engine 'Docker engine must already be running and reachable'
fi

required_env=(
  AGENT_RUNTIME_TOKEN
  AGENT_RUNTIME_CGROUP_PARENT
  AGENT_RUNTIME_CGROUP_HOST_ROOT
  AGENT_RUNTIME_WORKSPACE_HOST_ROOT
  AGENT_RUNTIME_LOG_HOST_ROOT
  AGENT_FETCH_AUDIT_HOST_ROOT
  AGENT_RUNTIME_WORKSPACE_MAX_BYTES
  AGENT_RUNTIME_WORKSPACE_FS_MAX_BYTES
  AGENT_RUNTIME_LOG_FS_MAX_BYTES
  AGENT_FETCH_AUDIT_FS_MAX_BYTES
  AGENT_FETCH_HMAC_SECRET_FILE
  AGENT_FETCH_DNS_SERVERS
  AGENT_FETCH_EXTRA_DENY_CIDRS
  AGENT_FETCH_POLICY_VERSION
  AGENT_FETCH_NFT_TEST_PID
  AGENT_FETCH_NFT_TEST_IPV4_TARGET
  AGENT_FETCH_NFT_TEST_IPV6_TARGET
  AGENT_FETCH_NFT_TEST_INPUT_TARGET
  AGENT_FETCH_NFT_TEST_IPV4_RULE_HANDLE
  AGENT_FETCH_NFT_TEST_IPV6_RULE_HANDLE
  AGENT_FETCH_NFT_TEST_INPUT_RULE_HANDLE
  AGENT_FETCH_TEST_HOST_URL
  AGENT_FETCH_TEST_BOT_URL
  AGENT_FETCH_TEST_REDIS_URL
)
missing_env=()
for name in "${required_env[@]}"; do
  value=${!name-}
  [ -n "$value" ] || missing_env+=("$name")
done
if [ "${#missing_env[@]}" -eq 0 ]; then
  pass_case preflight-environment 'all deployment, bounded-mount, secret, site-route, and nft fixture inputs are explicit'
else
  fail_case preflight-environment "missing nonblank variables: ${missing_env[*]}"
fi

if [ "$host_os" = Linux ] && [ "$(stat -fc %T /sys/fs/cgroup 2>/dev/null || true)" = cgroup2fs ]; then
  pass_case preflight-cgroup-v2 'unified cgroup v2 is mounted'
else
  fail_case preflight-cgroup-v2 'requires /sys/fs/cgroup to be a native cgroup v2 filesystem'
fi

if [ "$fail_count" -ne "$preflight_start_failures" ]; then
  exit 1
fi

bounded_ok=1
for numeric in AGENT_RUNTIME_WORKSPACE_MAX_BYTES AGENT_RUNTIME_WORKSPACE_FS_MAX_BYTES AGENT_RUNTIME_LOG_FS_MAX_BYTES AGENT_FETCH_AUDIT_FS_MAX_BYTES; do
  value=${!numeric}
  case "$value" in *[!0-9]*|'') bounded_ok=0 ;; esac
done
if [ "$bounded_ok" -eq 1 ] \
  && [ "$AGENT_RUNTIME_WORKSPACE_MAX_BYTES" -ge 73400320 ] \
  && [ "$AGENT_RUNTIME_WORKSPACE_FS_MAX_BYTES" -le 100663296 ] \
  && [ "$AGENT_RUNTIME_LOG_FS_MAX_BYTES" -le 67108864 ] \
  && [ "$AGENT_FETCH_AUDIT_FS_MAX_BYTES" -le 67108864 ]; then
  pass_case preflight-test-capacities 'caller mounts are small enough for disk attacks and large enough for the 65 MiB FSIZE probe'
else
  fail_case preflight-test-capacities 'require workspace logical >=73400320, workspace filesystem <=100663296, and log/audit filesystems <=67108864 bytes'
fi

case ",$AGENT_FETCH_EXTRA_DENY_CIDRS," in
  *,11.0.0.1/32,* ) gateway_denied=1 ;;
  * ) gateway_denied=0 ;;
esac
case ",$AGENT_FETCH_EXTRA_DENY_CIDRS," in
  *,11.0.0.2/32,* ) broker_denied=1 ;;
  * ) broker_denied=0 ;;
esac
if [ "$gateway_denied" -eq 1 ] && [ "$broker_denied" -eq 1 ]; then
  pass_case preflight-test-deny-cidrs 'test Docker gateway and deterministic Broker TCP address are site-denied'
else
  fail_case preflight-test-deny-cidrs 'AGENT_FETCH_EXTRA_DENY_CIDRS must include 11.0.0.1/32 and 11.0.0.2/32 without denying 11.0.0.10'
fi

if python3 - \
  "$AGENT_FETCH_EXTRA_DENY_CIDRS" \
  "$AGENT_FETCH_TEST_HOST_URL" \
  "$AGENT_FETCH_TEST_BOT_URL" \
  "$AGENT_FETCH_TEST_REDIS_URL" \
  "$AGENT_FETCH_NFT_TEST_IPV4_TARGET" \
  "$AGENT_FETCH_NFT_TEST_IPV6_TARGET" \
  "$AGENT_FETCH_NFT_TEST_INPUT_TARGET" <<'PY'
import ipaddress
import sys
import urllib.parse

deny = [ipaddress.ip_network(item.strip(), strict=False) for item in sys.argv[1].split(",")]
fixture = ipaddress.ip_address("11.0.0.10")
test_network = ipaddress.ip_network("11.0.0.0/24")
if any(fixture in network for network in deny):
    raise SystemExit("fixture IP must not be denied")

def allowed(address):
    return (not address.is_global or address in test_network) and any(
        address in network for network in deny
    )

for raw_url in sys.argv[2:5]:
    parsed = urllib.parse.urlsplit(raw_url)
    if (
        parsed.scheme not in {"http", "https"}
        or parsed.username is not None
        or parsed.password is not None
        or parsed.port is None
        or parsed.query
        or parsed.fragment
    ):
        raise SystemExit("control URLs must be credential-free literal HTTP(S) endpoints with explicit ports")
    try:
        address = ipaddress.ip_address(parsed.hostname)
    except ValueError as error:
        raise SystemExit("control URLs must use literal IP addresses") from error
    if address == fixture or not allowed(address):
        raise SystemExit("control URL target must be a denied private/control or Task 9 test-network address")

ipv4 = ipaddress.ip_address(sys.argv[5])
ipv6 = ipaddress.ip_address(sys.argv[6])
if ipv4.version != 4 or ipv6.version != 6 or not allowed(ipv4) or not allowed(ipv6):
    raise SystemExit("nft counter targets must be denied IPv4/IPv6 private or control addresses")
input_target = ipaddress.ip_address(sys.argv[7])
if input_target != ipaddress.ip_address("11.0.0.1") or not allowed(input_target):
    raise SystemExit("nft input target must be the denied deterministic bridge gateway 11.0.0.1")
PY
then
  pass_case preflight-local-targets 'all live targets are literal denied control/test addresses and the fixture remains allowed'
else
  fail_case preflight-local-targets 'control URLs or nft counter targets could reach an unapproved/public destination'
fi

roots_empty=1
for root in "$AGENT_RUNTIME_WORKSPACE_HOST_ROOT" "$AGENT_RUNTIME_LOG_HOST_ROOT" "$AGENT_FETCH_AUDIT_HOST_ROOT"; do
  if [ ! -d "$root" ] || [ -n "$(find "$root" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)" ]; then
    roots_empty=0
  fi
done
if [ "$roots_empty" -eq 1 ]; then
  pass_case preflight-empty-test-roots 'bounded workspace, Runtime log, and Broker audit roots are empty and disposable'
else
  fail_case preflight-empty-test-roots 'all three caller-prepared bounded roots must exist and be empty; the runner will not delete preexisting data'
fi

if [ -d "$AGENT_RUNTIME_CGROUP_HOST_ROOT" ] \
  && [ -z "$(find "$AGENT_RUNTIME_CGROUP_HOST_ROOT" -mindepth 1 -maxdepth 1 -type d -print -quit 2>/dev/null)" ]; then
  pass_case preflight-empty-command-cgroup 'delegated command root has no preexisting child cgroup'
else
  fail_case preflight-empty-command-cgroup 'delegated command root must exist and contain no child cgroup before acceptance'
fi

collision=0
for container in agent-runtime agent-fetch-broker agent-fetch-fixture; do
  docker container inspect "$container" >/dev/null 2>&1 && collision=1
done
for network in bot-runtime-control fetch-egress; do
  docker network inspect "$network" >/dev/null 2>&1 && collision=1
done
docker volume inspect fetch-socket >/dev/null 2>&1 && collision=1
docker container inspect "$RUST_TEST_CONTAINER" >/dev/null 2>&1 && collision=1
for image in "${COMPOSE_IMAGE_NAMES[@]}"; do
  docker image inspect "$image" >/dev/null 2>&1 && collision=1
done
if [ "$collision" -eq 0 ]; then
  pass_case preflight-resource-collision 'fixed Compose resources and the generated in-image test container name are absent'
else
  fail_case preflight-resource-collision 'Task 9 Compose or generated in-image test container resources already exist; refusing to touch them'
fi

if [[ "$AGENT_FETCH_NFT_TEST_PID" =~ ^[1-9][0-9]*$ ]] \
  && [ -r "/proc/$AGENT_FETCH_NFT_TEST_PID/ns/net" ] \
  && [ "$(stat -Lc %i "/proc/$AGENT_FETCH_NFT_TEST_PID/ns/net")" != "$(stat -Lc %i /proc/self/ns/net)" ]; then
  pass_case preflight-nft-test-namespace 'caller-prepared dedicated network namespace process exists and differs from the host namespace'
else
  fail_case preflight-nft-test-namespace 'AGENT_FETCH_NFT_TEST_PID must identify a live preprovisioned process in the dedicated br-agent-fetch test namespace'
fi

if [[ "$AGENT_FETCH_NFT_TEST_IPV4_RULE_HANDLE" =~ ^[1-9][0-9]*$ ]] \
  && [[ "$AGENT_FETCH_NFT_TEST_IPV6_RULE_HANDLE" =~ ^[1-9][0-9]*$ ]] \
  && [[ "$AGENT_FETCH_NFT_TEST_INPUT_RULE_HANDLE" =~ ^[1-9][0-9]*$ ]]; then
  pass_case preflight-nft-counter-handles 'preloaded input and forward IPv4/IPv6 reject rules have explicit counter handles'
else
  fail_case preflight-nft-counter-handles 'input and forward IPv4/IPv6 reject rule handles must be positive decimal integers'
fi

if [ "$fail_count" -ne "$preflight_start_failures" ]; then
  exit 1
fi

export AGENT_RUNTIME_SECURITY_TEST_GUARD=task9-acceptance-only
export COMPOSE_PROFILES=agent-fetch
export AGENT_FETCH_ENABLE=true
export AGENT_FETCH_TEST_BROKER_POLICY_VERSION="$AGENT_FETCH_POLICY_VERSION"
unset AGENT_FETCH_TEST_AUDIT_PATH || true
unset AGENT_RUNTIME_TEST_CGROUP_ROOT || true

validator_log=$(new_artifact host-validator.log)
if bash "$REPO_ROOT/scripts/validate-agent-runtime-host.sh" >"$validator_log" 2>&1; then
  pass_case preflight-host-validator 'delegation, aggregate limits, bounded mounts, secret, DNS, and loaded nft table validate read-only'
else
  fail_case preflight-host-validator 'read-only host validator rejected delegation, mounts, secret, DNS, or preloaded nftables'
  sed 's/^/HOST-VALIDATOR: /' "$validator_log" >&2
  exit 1
fi

delegated_gate_failures=$fail_count
export AGENT_RUNTIME_TEST_CGROUP_ROOT="$AGENT_RUNTIME_CGROUP_HOST_ROOT"
run_logged task1-delegated-cgroup timeout 10m cargo test --manifest-path "$REPO_ROOT/agent-runtime/Cargo.toml" --release --test linux_cgroup -- --ignored --nocapture || true
unset AGENT_RUNTIME_TEST_CGROUP_ROOT
if [ "$fail_count" -ne "$delegated_gate_failures" ]; then
  fail_case delegated-cgroup-gate 'the real delegated-cgroup first-instruction assertion failed; Compose launch is forbidden'
  exit 1
fi
pass_case delegated-cgroup-gate 'real delegated-cgroup first-instruction and cleanup assertions passed'

production_activation_default_off() {
  result=0
  base_rendered=$(new_artifact base-production-compose.json)
  if ! (
    unset COMPOSE_PROFILES AGENT_FETCH_ENABLE AGENT_FETCH_AUDIT_HOST_ROOT
    unset AGENT_FETCH_AUDIT_FS_MAX_BYTES AGENT_FETCH_HMAC_SECRET_FILE
    unset AGENT_FETCH_DNS_SERVERS AGENT_FETCH_EXTRA_DENY_CIDRS AGENT_FETCH_POLICY_VERSION
    dc_base config --format json >"$base_rendered"
  ); then
    return 1
  fi
  jq -e '
    (.services | has("agent-fetch-broker") | not) and
    (.services | has("agent-fetch-fixture") | not) and
    (.networks | has("fetch-egress") | not) and
    ((.volumes // {}) | has("fetch-socket") | not) and
    ((.secrets // {}) | has("agent-fetch-hmac-key") | not) and
    (.services["agent-runtime"].environment.AGENT_RUNTIME_FETCH_ENABLED == "false") and
    (.services["agent-runtime"].environment.AGENT_RUNTIME_REQUIRE_FETCH_FOR_READINESS == "false") and
    ([.services["agent-runtime"].environment | keys[] | select(contains("FETCH"))] | sort) ==
      ["AGENT_RUNTIME_FETCH_ENABLED","AGENT_RUNTIME_REQUIRE_FETCH_FOR_READINESS"] and
    ((.services["agent-runtime"].secrets // []) | length) == 0 and
    all(.services["agent-runtime"].volumes[]?; .source != "fetch-socket" and .target != "/run/agent-fetch")
  ' "$base_rendered" >/dev/null || result=1
  grep -E '^[[:space:]]+fetch_enabled:[[:space:]]+false([[:space:]]*#.*)?$' "$REPO_ROOT/config.yaml" >/dev/null 2>&1 || result=1

  base_compose_attempted=1
  base_runtime_active=1
  dc_base up -d --build agent-runtime >"$(new_artifact base-compose-up.log)" 2>&1 || result=1
  wait_container_running agent-runtime 40 || result=1
  status=''
  attempts=40
  while [ "$attempts" -gt 0 ]; do
    status=$(runtime_get /v1/status 2>/dev/null || true)
    if printf '%s' "$status" | jq -e '
      .http_status == 200 and .body.ok and .body.bash_ready and
      (.body.fetch_enabled | not) and (.body.fetch_ready | not) and
      (.body.supervisor_dumpable | not)
    ' >/dev/null 2>&1; then
      break
    fi
    attempts=$((attempts - 1))
    sleep 0.5
  done
  [ "$attempts" -gt 0 ] || result=1
  assert_bash_exit 69 'fetch GET http://11.0.0.10:8080/items' task9-rollback base-fetch-disabled 5s || result=1
  assert_bash_success '
    tr "\000" "\n" </proc/$$/environ | sort >/tmp/initial-env;
    test "$(cut -d= -f1 /tmp/initial-env | paste -sd, -)" = "AGENT_FETCH_CONTROL_FD,HOME,PATH";
    test "$(sed -n "s/^AGENT_FETCH_CONTROL_FD=//p" /tmp/initial-env)" = 4;
    test ! -e /run/agent-fetch;
    test ! -e /run/secrets/agent-fetch-hmac-key;
    test "$(agent-runtime-net-probe socket inet)" = errno=1;
    test "$(agent-runtime-net-probe socket inet6)" = errno=1
  ' task9-rollback base-local-only 5s || result=1

  dc_base down --volumes --remove-orphans --rmi local --timeout 10 >/dev/null 2>&1 || result=1
  base_runtime_active=0
  docker container inspect agent-runtime >/dev/null 2>&1 && result=1
  docker network inspect bot-runtime-control >/dev/null 2>&1 && result=1
  aggregate=$(realpath -e -- "/sys/fs/cgroup/$AGENT_RUNTIME_CGROUP_PARENT" 2>/dev/null || true)
  [ -n "$aggregate" ] || result=1
  attempts=100
  while [ "$attempts" -gt 0 ] \
    && [ -n "$(find "$aggregate" -mindepth 1 -maxdepth 1 -type d ! -name commands -print -quit 2>/dev/null)" ]; do
    attempts=$((attempts - 1))
    sleep 0.05
  done
  [ "$attempts" -gt 0 ] || result=1
  [ "$result" -eq 0 ]
}
case_run production-activation-default-off 'base-only Compose has no Fetch material; disabled Runtime keeps local Bash ready, Fetch=69, config false, and AF_INET EPERM before explicit activation' production_activation_default_off || true
if [ "$fail_count" -ne 0 ]; then
  exit 1
fi

image_failures=$fail_count
compose_attempted=1
printf 'compose-owned\t%s\n' "$PROJECT_NAME" >>"$tmp_dir/cleanup.journal"
run_logged security-test-images timeout 30m dc build agent-runtime agent-fetch-broker agent-fetch-fixture || true
if [ "$fail_count" -ne "$image_failures" ]; then
  exit 1
fi

image_test_failures=$fail_count
release_root=$(realpath -e "$REPO_ROOT/agent-runtime/target/release" 2>/dev/null || true)
cargo_manifest=$(realpath -e "$REPO_ROOT/agent-runtime/Cargo.toml" 2>/dev/null || true)
runtime_test_image=${COMPOSE_IMAGE_NAMES[0]}
runtime_test_image_id=$(docker image inspect -f '{{.Id}}' "$runtime_test_image" 2>/dev/null || true)
if [[ "$runtime_test_image_id" =~ ^sha256:[0-9a-f]{64}$ ]] \
  && docker image inspect "$runtime_test_image_id" | jq -e '
    length == 1 and
    .[0].Config.Labels["org.csusters.agent-runtime.security-test-only"] == "true"
  ' >/dev/null; then
  pass_case task4-5-test-image 'the built Runtime image is explicitly security-test-only'
else
  fail_case task4-5-test-image 'Compose did not produce exactly one labeled security-test Runtime image'
fi

source_has_all_fragments() {
  local source_path=$1
  shift
  [ -f "$source_path" ] || return 1
  for source_fragment in "$@"; do
    grep -F -- "$source_fragment" "$source_path" >/dev/null 2>&1 || return 1
  done
}

source_excludes_fragments() {
  local source_path=$1
  shift
  [ -f "$source_path" ] || return 1
  for source_fragment in "$@"; do
    if grep -F -- "$source_fragment" "$source_path" >/dev/null 2>&1; then
      return 1
    fi
  done
}

source_fragments_in_order() {
  local source_path=$1 source_line=0 matched_line
  shift
  [ -f "$source_path" ] || return 1
  for source_fragment in "$@"; do
    matched_line=$(awk -v after="$source_line" -v fragment="$source_fragment" '
      NR > after && index($0, fragment) { print NR; exit }
    ' "$source_path")
    [ -n "$matched_line" ] || return 1
    source_line=$matched_line
  done
}

source_has_contiguous_fragments() {
  local source_path=$1 source_line matched=0
  shift
  local -a source_fragments=("$@")
  [ -f "$source_path" ] && [ "${#source_fragments[@]}" -gt 0 ] || return 1
  while IFS= read -r source_line; do
    if [[ "$source_line" == *"${source_fragments[$matched]}"* ]]; then
      matched=$((matched + 1))
      [ "$matched" -eq "${#source_fragments[@]}" ] && return 0
    elif [[ "$source_line" == *"${source_fragments[0]}"* ]]; then
      matched=1
    else
      matched=0
    fi
  done <"$source_path"
  return 1
}

source_fragment_count() {
  local source_path=$1 source_fragment=$2 expected_count=$3 actual_count
  [ -f "$source_path" ] || return 1
  actual_count=$(grep -F -c -- "$source_fragment" "$source_path" || true)
  [ "$actual_count" -eq "$expected_count" ]
}

verify_c7_authority_source_contracts() {
  source_has_all_fragments \
    "$REPO_ROOT/agent-runtime/src/exec/spawn/fd_map/c7_test_support.rs" \
    'FD_INSTALL_FAULT_STAGES: [&str; 21]' \
    'FD_INSTALL_FAULTS: [FdInstallStage; 21]' \
    'FdInstallStage::Duplicate(FdRole::Config)' \
    'FdInstallStage::Duplicate(FdRole::Control)' \
    'FdInstallStage::Duplicate(FdRole::Status)' \
    'FdInstallStage::OriginalClose(FdRole::Config)' \
    'FdInstallStage::OriginalClose(FdRole::Control)' \
    'FdInstallStage::OriginalClose(FdRole::Status)' \
    'FdInstallStage::Dup2(FdRole::Config)' \
    'FdInstallStage::Dup2(FdRole::Control)' \
    'FdInstallStage::Dup2(FdRole::Status)' \
    'FdInstallStage::GetFd(FdRole::Config)' \
    'FdInstallStage::GetFd(FdRole::Control)' \
    'FdInstallStage::GetFd(FdRole::Status)' \
    'FdInstallStage::SetFd(FdRole::Config)' \
    'FdInstallStage::SetFd(FdRole::Control)' \
    'FdInstallStage::SetFd(FdRole::Status)' \
    'FdInstallStage::VerifyGetFd(FdRole::Config)' \
    'FdInstallStage::VerifyGetFd(FdRole::Control)' \
    'FdInstallStage::VerifyGetFd(FdRole::Status)' \
    'FdInstallStage::TempClose(FdRole::Config)' \
    'FdInstallStage::TempClose(FdRole::Control)' \
    'FdInstallStage::TempClose(FdRole::Status)' \
    'pub(in crate::exec) unsafe fn install_exec_fds_with_fault(' \
    'install_exec_fds_with(' \
    'real: RealFdSyscalls' || return 1
  source_excludes_fragments \
    "$REPO_ROOT/agent-runtime/src/exec/spawn/fd_map/c7_test_support.rs" \
    'const FAULTS:' || return 1
  source_has_all_fragments \
    "$REPO_ROOT/agent-runtime/src/exec/c7_test_support/fd_faults.rs" \
    'use crate::exec::spawn::fd_map::c7_test_support::FD_INSTALL_FAULTS;' \
    'let mut rows = Vec::with_capacity(FD_INSTALL_FAULTS.len());' \
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
    'binding_phase,' \
    'binding_registry_entries,' \
    'cgroup_removed: !cgroup_path.exists()' \
    'cgroup_cleanup_count,' \
    'deferred_cleanup_count,' \
    'local_descriptors_released: descriptor_probe.all_released()' \
    'subsequent_bash_rejected: matches!(subsequent, Err(SupervisorError::Unavailable(_)))' \
    'CommandSupervisor::test_production_with_spawn_controls(' \
    'fd_install_fault: fault' || return 1
  source_fragments_in_order \
    "$REPO_ROOT/agent-runtime/src/exec/c7_test_support/fd_faults.rs" \
    'for (fault, stage) in FD_INSTALL_FAULTS.into_iter().zip(FD_INSTALL_FAULT_STAGES)' \
    'rows.push(run_fault_row(fault, stage).await);' \
    'async fn run_fault_row(' \
    'let descriptor_probe = DescriptorReleaseProbe::default();' \
    'let supervisor = production_supervisor(cgroups, Some(fault), descriptor_probe.clone());' \
    'let health = supervisor.health();' \
    'let marker = root.path().join("target-exec-marker");' \
    'let launch = command_launch(&proxy, &health, &command_id);' \
    'let lifecycle = launch.lifecycle.clone();' \
    'let cgroup_path = cgroup_root.join(identity.cgroup_name());' \
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
    'subsequent_bash_rejected: matches!(subsequent, Err(SupervisorError::Unavailable(_)))' || return 1
  source_fragment_count \
    "$REPO_ROOT/agent-runtime/src/exec/c7_test_support/fd_faults.rs" \
    'let supervisor = production_supervisor(cgroups, Some(fault), descriptor_probe.clone());' 1 || return 1
  source_excludes_fragments \
    "$REPO_ROOT/agent-runtime/src/exec/c7_test_support/fd_faults.rs" \
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
    'subsequent_bash_rejected: true' || return 1
  source_has_all_fragments \
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
    'assert!(row.subsequent_bash_rejected, "{}", row.stage);' \
    'agent_runtime::c7_test_support::config_writer_thread_failure().await' \
    'assert!(receipt.binding_drained);' \
    'assert!(receipt.cgroup_removed);' \
    'assert!(receipt.local_descriptors_released);' || return 1
  source_has_all_fragments \
    "$REPO_ROOT/agent-runtime/src/exec/supervisor/test_support.rs" \
    'fn test_production_with_spawn_controls(' \
    'backend: SupervisorBackend::Production {' \
    'spawn_controls,' || return 1
  source_has_all_fragments \
    "$REPO_ROOT/agent-runtime/src/exec/launch.rs" \
    'SupervisorBackend::Production {' \
    'spawn_controls,' \
    'let prepared = production::prepare(' \
    'spawn_controls,' || return 1
  source_has_all_fragments \
    "$REPO_ROOT/agent-runtime/src/exec/launch/production.rs" \
    'spawn_controls: &SpawnControls' \
    'let spawn_result = spawn_exec_helper_with_control_and_controls(' \
    'spawn_controls.clone()' || return 1
  source_has_contiguous_fragments \
    "$REPO_ROOT/agent-runtime/src/exec/launch/production.rs" \
    '#[cfg(feature = "c7-test-support")]' \
    'let spawn_result = spawn_exec_helper_with_control_and_controls(' || return 1
  source_has_all_fragments \
    "$REPO_ROOT/agent-runtime/src/exec/spawn.rs" \
    '#[cfg(all(feature = "c7-test-support", target_os = "linux"))]' \
    'pub(super) struct SpawnControls {' \
    'pub(super) fd_install_fault: Option<fd_map::FdInstallStage>' \
    'let fd_install_fault = controls.fd_install_fault;' \
    'command.pre_exec(move || {' \
    'if let Some(fault) = fd_install_fault {' \
    'return fd_map::c7_test_support::install_exec_fds_with_fault(' || return 1
  source_has_contiguous_fragments \
    "$REPO_ROOT/agent-runtime/src/exec/spawn.rs" \
    '#[cfg(all(feature = "c7-test-support", target_os = "linux"))]' \
    '#[derive(Clone, Default)]' \
    'pub(super) struct SpawnControls {' || return 1
  source_has_contiguous_fragments \
    "$REPO_ROOT/agent-runtime/src/exec/spawn.rs" \
    '#[cfg(feature = "c7-test-support")]' \
    'let fd_install_fault = controls.fd_install_fault;' || return 1
  source_has_contiguous_fragments \
    "$REPO_ROOT/agent-runtime/src/exec/spawn.rs" \
    '#[cfg(feature = "c7-test-support")]' \
    'if let Some(fault) = fd_install_fault {' || return 1
  source_fragments_in_order \
    "$REPO_ROOT/agent-runtime/src/exec/spawn.rs" \
    'let fd_install_fault = controls.fd_install_fault;' \
    'command.pre_exec(move || {' \
    'if let Some(fault) = fd_install_fault {' \
    'return fd_map::c7_test_support::install_exec_fds_with_fault(' || return 1
  source_has_all_fragments \
    "$REPO_ROOT/agent-runtime/tests/runtime_fetch_proxy.rs" \
    'live_sessions_before_panic, 2' \
    'drain.guardian.spawned_sessions, 2' \
    'drain.guardian.joined_sessions, 2' \
    'live_sessions_after_timeout, 2' \
    'guardian_spawned_after_shutdown, 2' \
    'guardian_joined_after_shutdown, 2' \
    'broker_error_then_internal_exactly_once' \
    'writer_unavailable_no_frame' \
    'relay_failure_after_response_start_is_one_terminal().await' \
    'broker_error_then_late_bad_frame_is_one_terminal().await'
}

case_run c7-source-authority 'FD 21-stage and live guardian/terminal source contracts match the exact binaries about to be compiled' \
  verify_c7_authority_source_contracts || true
if [ "$fail_count" -ne "$image_test_failures" ]; then
  exit 1
fi

compile_c7_test_target() {
  local selector=$1 cargo_target=$2
  local json stderr_log candidates copied path kind
  local -a selector_args executable_paths
  case "$cargo_target" in ''|*[!a-zA-Z0-9_-]*) return 2 ;; esac
  case "$selector" in
    lib) selector_args=(--lib); kind=lib ;;
    test:*) selector_args=(--test "${selector#test:}"); kind=test ;;
    *) return 2 ;;
  esac
  json=$(new_c7_artifact "$cargo_target.cargo.jsonl") || return 2
  stderr_log=$(new_c7_artifact "$cargo_target.cargo.stderr") || return 2
  candidates=$(new_c7_artifact "$cargo_target.candidates") || return 2
  copied=$(new_c7_artifact "$cargo_target.test") || return 2
  [ ! -e "$json" ] && [ ! -e "$stderr_log" ] && [ ! -e "$candidates" ] && [ ! -e "$copied" ] || return 2
  cargo test --locked --release --no-run --message-format=json \
    --features c7-test-support \
    --manifest-path "$REPO_ROOT/agent-runtime/Cargo.toml" \
    "${selector_args[@]}" >"$json" 2>"$stderr_log" || return 3
  jq -s -r --arg manifest "$cargo_manifest" --arg target "$cargo_target" --arg kind "$kind" '
    [.[] | select(
      .reason == "compiler-artifact" and
      .manifest_path == $manifest and
      .target.name == $target and
      .target.kind == [$kind] and
      .profile.test == true and
      (.executable | type) == "string"
    ) | .executable] | unique[]
  ' "$json" >"$candidates" || return 4
  mapfile -t executable_paths <"$candidates"
  [ "${#executable_paths[@]}" -eq 1 ] || return 4
  path=$(realpath -e -- "${executable_paths[0]}" 2>/dev/null || true)
  case "$path" in "$release_root"/*) ;; *) return 5 ;; esac
  [ -f "$path" ] && [ -x "$path" ] || return 5
  [[ "$path" != *','* ]] && [[ "$path" != *$'\n'* ]] && [[ "$path" != *$'\t'* ]] || return 5
  cp -- "$path" "$copied" || return 6
  chmod 0555 "$copied" || return 7
  [ "$(stat -c '%a' "$copied")" = 555 ] || return 7
  compiled_c7_test_binary=$copied
}

compile_required_c7_target() {
  local key=$1 selector=$2 target=$3
  compiled_c7_test_binary=""
  if compile_c7_test_target "$selector" "$target"; then
    c7_test_binaries["$key"]=$compiled_c7_test_binary
    pass_case "c7-artifact-$key" 'one independently selected C7 Cargo target yielded exactly one copied executable'
  else
    fail_case "c7-artifact-$key" 'C7 Cargo selector did not yield exactly one safe executable'
  fi
}

compile_required_c7_target lib lib agent_runtime
compile_required_c7_target linux_exec_helper test:linux_exec_helper linux_exec_helper
compile_required_c7_target linux_cgroup test:linux_cgroup linux_cgroup
compile_required_c7_target runtime_fetch_proxy test:runtime_fetch_proxy runtime_fetch_proxy
compile_required_c7_target fetch_cli test:fetch_cli fetch_cli

if [ "$fail_count" -ne "$image_test_failures" ]; then
  exit 1
fi

run_c7_test_exact() {
  local binary=$1 exact_name=$2 receipt=$3
  [ -f "$binary" ] && [ -x "$binary" ] || return 1
  case "$(realpath -e -- "$binary" 2>/dev/null || true)" in "$c7_test_root"/*) ;; *) return 1 ;; esac
  rust_test_attempted=1
  timeout 10m docker run --rm \
    --name "$RUST_TEST_CONTAINER" \
    --network none \
    --read-only \
    --user 10001:10001 \
    --cap-drop ALL \
    --security-opt no-new-privileges=true \
    --pids-limit 64 \
    --memory 256m \
    --memory-swap 256m \
    --cpus 1 \
    --tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m \
    --workdir /tmp \
    --label "org.csusters.agent-runtime.task9-test-project=$PROJECT_NAME" \
    --mount "type=bind,src=$binary,dst=/c7-test,readonly" \
    --entrypoint /c7-test "$runtime_test_image_id" \
    "$exact_name" --exact --nocapture --test-threads=1 >"$receipt" 2>&1 || return 8
  local run_count pass_lines result_lines
  run_count=$(grep -Fxc 'running 1 test' "$receipt" || true)
  pass_lines=$(grep -Fxc "test $exact_name ... ok" "$receipt" || true)
  result_lines=$(grep -Ec '^test result: ok\. 1 passed; 0 failed; 0 ignored; 0 measured; [0-9]+ filtered out; finished in .+$' "$receipt" || true)
  if [ "$run_count" -ne 1 ] || [ "$pass_lines" -ne 1 ] || [ "$result_lines" -ne 1 ]; then
    return 9
  fi
  printf 'test_target=%s\ntest_filter=%s\ntest_run_count=1\ntest_pass_count=1\ntest_result_ok=1\n' \
    "$(basename "$binary")" "$exact_name" >>"$receipt"
}

run_c7_case() {
  local name=$1 target_key=$2 exact_name=$3 binary receipt
  binary=${c7_test_binaries[$target_key]-}
  if ! receipt=$(new_c7_artifact "$name.receipt"); then
    fail_case "$name" 'authoritative C7 exact test receipt path could not be allocated safely'
    return 1
  fi
  if [ -n "$binary" ] && run_c7_test_exact "$binary" "$exact_name" "$receipt"; then
    pass_case "$name" 'one authoritative C7 exact test emitted one RUN, one PASS, and one successful result'
  else
    fail_case "$name" 'authoritative C7 exact test failed binary-only execution or nonzero receipt validation'
  fi
}

printf 'test-container\t%s\n' "$RUST_TEST_CONTAINER" >>"$tmp_dir/cleanup.journal"
run_c7_case c7-helper-init-stage lib exec::tests::c7_helper_init_stage_failures_emit_one_exact_record_and_latch || true
run_c7_case c7-spawn-preexec-config lib exec::tests::c7_spawn_preexec_and_config_writer_failures_latch || true
run_c7_case c7-helper-clean-eof lib exec::tests::c7_helper_status_clean_eof_accepts_target_exit_one_without_latch || true
run_c7_case c7-helper-status-failures lib exec::tests::c7_helper_status_timeout_malformed_and_read_failure_latch || true
run_c7_case c7-helper-fd-layout linux_exec_helper c7_exec_fd_layouts_preserve_config_control_status || true
run_c7_case c7-helper-mapping-failures linux_exec_helper c7_each_three_fd_mapping_stage_failure_aborts_and_latches || true
run_c7_case c7-config-writer-thread linux_exec_helper c7_config_writer_thread_creation_failure_latches_and_cleans || true
run_c7_case c7-cgroup-create-failure linux_cgroup c7_cgroup_create_failure_latches_and_cancels_active || true
run_c7_case c7-cgroup-control-failures linux_cgroup c7_each_limit_control_write_failure_latches || true
run_c7_case c7-cgroup-cpu-failures linux_cgroup c7_cpu_usage_read_and_parse_failure_latch || true
run_c7_case c7-cgroup-latch-local-apis linux_cgroup c7_enforcement_latch_is_irreversible_but_local_apis_remain || true
run_c7_case c7-control-panic runtime_fetch_proxy c7_control_reader_panic_still_drains_guardian || true
run_c7_case c7-revoke-admission runtime_fetch_proxy c7_revoke_phase_blocks_admission_before_guardian_drain || true
run_c7_case c7-control-error runtime_fetch_proxy c7_control_reader_error_receipt_does_not_drop_guardian || true
run_c7_case c7-guardian-mismatch runtime_fetch_proxy c7_guardian_receipt_mismatch_blocks_cgroup_cleanup || true
run_c7_case c7-guardian-timeout runtime_fetch_proxy c7_guardian_timeout_retains_entry_handles_and_joinset || true
run_c7_case c7-deferred-cleanup runtime_fetch_proxy c7_deferred_cgroup_cleanup_waits_for_shutdown_drain_receipt || true
run_c7_case c7-lifecycle-trace runtime_fetch_proxy c7_lifecycle_trace_orders_drain_before_cleanup_complete || true
run_c7_case c7-cleanup-failure-trace runtime_fetch_proxy c7_cgroup_cleanup_failure_omits_complete_trace_and_latches_health || true
run_c7_case c7-output-policy65 runtime_fetch_proxy c7_output_capacity_path_and_busy_send_one_policy_terminal_exit_65 || true
run_c7_case c7-output-internal70 runtime_fetch_proxy c7_output_open_write_file_sync_and_rename_send_one_internal_terminal_exit_70 || true
run_c7_case c7-runtime-terminal runtime_fetch_proxy c7_runtime_error_is_not_silent_eof_and_terminal_is_never_duplicated || true
run_c7_case c7-prerename-preserve runtime_fetch_proxy c7_pre_rename_failure_preserves_old_file_and_returns_70 || true
run_c7_case c7-postrename-commit runtime_fetch_proxy c7_post_rename_directory_sync_failure_is_committed_and_latches_shared_health || true
run_c7_case c7-supervisor-health runtime_fetch_proxy c7_command_binding_uses_supervisor_bash_health || true
run_c7_case c7-fetch-exit-map fetch_cli c7_output_policy_and_internal_terminal_errors_map_exact_exit_codes || true
if [ "$fail_count" -ne "$image_test_failures" ]; then
  exit 1
fi
pass_case c7-binary-only-executable-gate 'all 26 authoritative C7 tests ran from one-bind copied binaries with no companion, source, cache, target, network, or cgroup seam'

compile_legacy_test_target() {
  local key=$1 target=$2 json candidates copied path
  local -a executable_paths
  json=$(new_artifact "legacy-$key.cargo.jsonl") || return 1
  candidates=$(new_artifact "legacy-$key.candidates") || return 1
  copied=$(new_artifact "legacy-$key.test") || return 1
  cargo test --locked --release --no-run --message-format=json \
    --manifest-path "$REPO_ROOT/agent-runtime/Cargo.toml" --test "$target" >"$json" 2>/dev/null || return 1
  jq -s -r --arg manifest "$cargo_manifest" --arg target "$target" '
    [.[] | select(
      .reason == "compiler-artifact" and .manifest_path == $manifest and
      .target.name == $target and .target.kind == ["test"] and
      .profile.test == true and (.executable | type) == "string"
    ) | .executable] | unique[]
  ' "$json" >"$candidates" || return 1
  mapfile -t executable_paths <"$candidates"
  [ "${#executable_paths[@]}" -eq 1 ] || return 1
  path=$(realpath -e -- "${executable_paths[0]}" 2>/dev/null || true)
  case "$path" in "$release_root"/*) ;; *) return 1 ;; esac
  [ -f "$path" ] && [ -x "$path" ] || return 1
  cp -- "$path" "$copied" || return 1
  chmod 0555 "$copied" || return 1
  legacy_test_binaries["$key"]=$copied
}

for legacy_target in fetch_broker fetch_cli; do
  if compile_legacy_test_target "$legacy_target" "$legacy_target"; then
    pass_case "legacy-artifact-$legacy_target" 'retained C2/C3 built-image target yielded one separate copied executable'
  else
    fail_case "legacy-artifact-$legacy_target" 'retained C2/C3 built-image target did not yield one safe executable'
  fi
done
legacy_fetch_binary=$(realpath -e -- "$release_root/fetch" 2>/dev/null || true)
[ -f "$legacy_fetch_binary" ] && [ -x "$legacy_fetch_binary" ] || fail_case legacy-fetch-companion 'retained C3 tests require the already-built exact Fetch executable'

run_legacy_test_exact() {
  local name=$1 target_key=$2 exact_name=$3 needs_fetch=${4:-0}
  local binary receipt run_count pass_lines result_lines
  local -a mounts
  binary=${legacy_test_binaries[$target_key]-}
  [ -n "$binary" ] && [ -f "$binary" ] && [ -x "$binary" ] || return 1
  receipt=$(new_artifact "$name.receipt") || return 1
  mounts=(--mount "type=bind,src=$binary,dst=/legacy-test,readonly")
  if [ "$needs_fetch" -eq 1 ]; then
    [ -f "$legacy_fetch_binary" ] && [ -x "$legacy_fetch_binary" ] || return 1
    mounts+=(--mount "type=bind,src=$legacy_fetch_binary,dst=$legacy_fetch_binary,readonly")
  fi
  rust_test_attempted=1
  if ! timeout 10m docker run --rm --name "$RUST_TEST_CONTAINER" \
    --network none --read-only --user 10001:10001 --cap-drop ALL \
    --security-opt no-new-privileges=true --pids-limit 64 \
    --memory 256m --memory-swap 256m --cpus 1 \
    --tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m \
    --tmpfs /workspace:rw,noexec,nosuid,nodev,size=64m,mode=0700,uid=10001,gid=10001 \
    --workdir /tmp --label "org.csusters.agent-runtime.task9-test-project=$PROJECT_NAME" \
    "${mounts[@]}" --entrypoint /legacy-test "$runtime_test_image_id" \
    "$exact_name" --exact --nocapture --test-threads=1 >"$receipt" 2>&1; then
    fail_case "$name" 'retained C2/C3 exact built-image test failed'
    return 1
  fi
  run_count=$(grep -Fxc 'running 1 test' "$receipt" || true)
  pass_lines=$(grep -Fxc "test $exact_name ... ok" "$receipt" || true)
  result_lines=$(grep -Ec '^test result: ok\. 1 passed; 0 failed; 0 ignored; 0 measured; [0-9]+ filtered out; finished in .+$' "$receipt" || true)
  if [ "$run_count" -ne 1 ] || [ "$pass_lines" -ne 1 ] || [ "$result_lines" -ne 1 ]; then
    fail_case "$name" 'retained C2/C3 binary exited without one exact nonzero test receipt'
    return 1
  fi
  pass_case "$name" 'retained C2/C3 exact built-image test emitted one RUN, PASS, and successful result'
}

if [ "$fail_count" -ne "$image_test_failures" ]; then
  exit 1
fi

if dc up -d agent-fetch-fixture agent-fetch-broker agent-runtime >"$(new_artifact compose-up.log)" 2>&1; then
  :
else
  fail_case compose-start 'approved Task 9 stack failed to start'
  exit 1
fi
if wait_container_health agent-fetch-fixture healthy 40 \
  && wait_container_running agent-fetch-broker 30 \
  && wait_container_health agent-runtime healthy 40; then
  health=$(runtime_health_json)
  if printf '%s' "$health" | jq -e '.docker_health == "healthy" and .ok and .bash_ready and .fetch_ready' >/dev/null; then
    pass_case compose-start 'fixture, Broker, and security-test Runtime are healthy and ready'
  else
    fail_case compose-start 'Runtime health output did not prove Bash and Fetch ready'
    exit 1
  fi
else
  fail_case compose-start 'fixture, Broker, or Runtime failed its bounded readiness wait'
  exit 1
fi

matrix_file_has_word() {
  path=$1
  expected=$2
  awk -v expected="$expected" '
    { for (i = 1; i <= NF; i++) { value = $i; sub(/^\+/, "", value); if (value == expected) found = 1 } }
    END { exit(found ? 0 : 1) }
  ' "$path"
}

read_exact_unified_cgroup() {
  pid=$1
  mapfile -t cgroup_lines <"/proc/$pid/cgroup" || return 1
  [ "${#cgroup_lines[@]}" -eq 1 ] || return 1
  [[ "${cgroup_lines[0]}" =~ ^0::(/.*)$ ]] || return 1
  printf '%s\n' "${BASH_REMATCH[1]}"
}

resolve_runtime_supervisor_host_pid() {
  container_init=$(docker inspect -f '{{.State.Pid}}' agent-runtime) || return 1
  [[ "$container_init" =~ ^[1-9][0-9]*$ ]] || return 1
  service_path=$(read_exact_unified_cgroup "$container_init") || return 1
  service_cgroup=$(realpath -e -- "/sys/fs/cgroup$service_path") || return 1
  runtime_binary_identity=$(stat -Lc '%d:%i' "/proc/$container_init/root/usr/local/bin/agent-runtime") || return 1
  runtime_pids=()
  for proc_dir in /proc/[1-9]*; do
    pid=${proc_dir##*/}
    [ -r "$proc_dir/cgroup" ] && [ -e "$proc_dir/exe" ] || continue
    candidate_path=$(read_exact_unified_cgroup "$pid" 2>/dev/null || true)
    [ "$candidate_path" = "$service_path" ] || continue
    candidate_identity=$(stat -Lc '%d:%i' "$proc_dir/exe" 2>/dev/null || true)
    [ "$candidate_identity" = "$runtime_binary_identity" ] || continue
    kill -0 "$pid" 2>/dev/null || continue
    runtime_pids+=("$pid")
  done
  [ "${#runtime_pids[@]}" -eq 1 ] || return 1
  runtime_supervisor_host_pid=${runtime_pids[0]}
  [ "$(stat -Lc '%d:%i' "/proc/$runtime_supervisor_host_pid/exe")" = "$runtime_binary_identity" ]
}

deployed_aggregate_direct_children() {
  aggregate=$(realpath -e -- "/sys/fs/cgroup/$AGENT_RUNTIME_CGROUP_PARENT") || return 1
  commands=$(realpath -e -- "$AGENT_RUNTIME_CGROUP_HOST_ROOT") || return 1
  [ "$(dirname -- "$commands")" = "$aggregate" ] || return 1
  [ "${commands##*/}" = commands ] || return 1

  resolve_runtime_supervisor_host_pid || return 1
  [ "$(dirname -- "$service_cgroup")" = "$aggregate" ] || return 1
  [ "$service_cgroup" != "$commands" ] || return 1

  for controller in pids memory cpu; do
    matrix_file_has_word "$aggregate/cgroup.controllers" "$controller" || return 1
    matrix_file_has_word "$aggregate/cgroup.subtree_control" "$controller" || return 1
    matrix_file_has_word "$commands/cgroup.controllers" "$controller" || return 1
    matrix_file_has_word "$commands/cgroup.subtree_control" "$controller" || return 1
  done
  [ "$(cat "$aggregate/pids.max")" = 512 ] || return 1
  [ "$(cat "$aggregate/memory.max")" = 1073741824 ] || return 1
  [ "$(cat "$aggregate/memory.swap.max")" = 0 ] || return 1
  [ "$(cat "$aggregate/cpu.max")" = '200000 100000' ] || return 1

  output=$(new_artifact deployed-cgroup-api.json)
  payload=$(jq -nc '{namespace:"task9-resource",run_id:"deployed-ancestry",cwd:"/workspace",command:"sleep 2",timeout:"5s"}')
  runtime_post /v1/bash "$payload" >"$output" 2>/dev/null &
  api_pid=$!
  track_pid "$api_pid" || return 1
  if ! capture_active_command_cgroup deployed-ancestry; then
    kill -TERM "$api_pid" 2>/dev/null || true
    wait "$api_pid" 2>/dev/null || true
    untrack_pid "$api_pid"
    return 1
  fi
  [ "$(dirname -- "$observed_group")" = "$commands" ] || return 1
  if ! wait "$api_pid"; then
    untrack_pid "$api_pid"
    return 1
  fi
  untrack_pid "$api_pid"
  jq -e '.http_status == 200 and .body.exit_code == 0' "$output" >/dev/null || return 1
  wait_cgroup_root_empty
}
case_run deployed-aggregate-direct-children 'the unique real agent-runtime PID and every active command are distinct direct aggregate children with exact controllers and limits' deployed_aggregate_direct_children || true

actual_supervisor_nondumpable_attach_denied() {
  [[ "${runtime_supervisor_host_pid-}" =~ ^[1-9][0-9]*$ ]] || return 1
  status=$(runtime_get /v1/status) || return 1
  printf '%s' "$status" | jq -e '
    .http_status == 200 and .body.ok and .body.bash_ready and .body.fetch_ready and
    (.body.supervisor_dumpable | not)
  ' >/dev/null || return 1
  nspid_line=$(awk '/^NSpid:/ {print; found=1} END {exit(found ? 0 : 1)}' "/proc/$runtime_supervisor_host_pid/status") || return 1
  read -r -a nspids <<<"$nspid_line"
  [ "${#nspids[@]}" -ge 3 ] || return 1
  [ "${nspids[1]}" = "$runtime_supervisor_host_pid" ] || return 1
  runtime_supervisor_namespace_pid=${nspids[${#nspids[@]}-1]}
  [[ "$runtime_supervisor_namespace_pid" =~ ^[1-9][0-9]*$ ]] || return 1
  assert_bash_success "test \"\$(agent-runtime-net-probe ptrace-attach $runtime_supervisor_namespace_pid)\" = errno=1" \
    task9-resource supervisor-ptrace 5s || return 1
  wait_container_health agent-runtime healthy 10 || return 1
  kill -0 "$runtime_supervisor_host_pid" 2>/dev/null
}
case_run actual-supervisor-nondumpable-attach-denied 'deployed status reports non-dumpable and UID 10001 PTRACE_ATTACH to the actual supervisor namespace PID returns EPERM without harming readiness' actual_supervisor_nondumpable_attach_denied || true
if [ "$fail_count" -ne 0 ]; then
  exit 1
fi

enforcement_health_observer() {
  runtime_exec python3 - <<'PY'
import json
import os
import time
import urllib.request

headers = {"Authorization": "Bearer " + os.environ["AGENT_RUNTIME_TOKEN"]}
deadline = time.monotonic() + 10
print("enforcement_health_observer_ready=1", flush=True)
while time.monotonic() < deadline:
    try:
        request = urllib.request.Request(
            "http://127.0.0.1:8080/v1/status",
            method="GET",
            headers=headers,
        )
        with urllib.request.urlopen(request, timeout=0.25) as response:
            status = json.loads(response.read())
        if (
            status.get("bash_ready") is False
            and "bash unavailable: command enforcement failed"
            in status.get("readiness_error", "")
        ):
            print("enforcement_health_observed=1", flush=True)
            raise SystemExit(0)
    except Exception:
        pass
    time.sleep(0.001)
raise SystemExit(3)
PY
}

capture_enforcement_active_groups() {
  local commands_path=$1 identity_file=$2 group canonical populated
  local -a groups
  mapfile -t groups < <(find "$commands_path" -mindepth 1 -maxdepth 1 -type d -print)
  [ "${#groups[@]}" -eq 2 ] || return 1
  : >"$identity_file" || return 1
  for group in "${groups[@]}"; do
    canonical=$(realpath -e -- "$group" 2>/dev/null || true)
    [ -n "$canonical" ] && [ "$(dirname -- "$canonical")" = "$commands_path" ] || return 1
    [ "$(stat -fc '%T' -- "$canonical" 2>/dev/null || true)" = cgroup2fs ] || return 1
    populated=$(awk '$1 == "populated" {print $2}' "$canonical/cgroup.events" 2>/dev/null || true)
    [ "$populated" = 1 ] && [ -s "$canonical/cgroup.procs" ] || return 1
    printf '%s\t%s\t%s\n' "$canonical" \
      "$(stat -Lc '%d' -- "$canonical")" "$(stat -Lc '%i' -- "$canonical")" >>"$identity_file" || return 1
  done
}

enforcement_active_groups_intact() {
  local commands_path=$1 identity_file=$2 group expected_device expected_inode canonical
  local -a groups
  mapfile -t groups < <(find "$commands_path" -mindepth 1 -maxdepth 1 -type d -print)
  [ "${#groups[@]}" -eq 2 ] || return 1
  while IFS=$'\t' read -r group expected_device expected_inode; do
    canonical=$(realpath -e -- "$group" 2>/dev/null || true)
    [ "$canonical" = "$group" ] || return 1
    [ "$(dirname -- "$canonical")" = "$commands_path" ] || return 1
    [ "$(stat -Lc '%d' -- "$canonical" 2>/dev/null || true)" = "$expected_device" ] || return 1
    [ "$(stat -Lc '%i' -- "$canonical" 2>/dev/null || true)" = "$expected_inode" ] || return 1
  done <"$identity_file"
}

runtime_enforcement_health_latch() {
  local result=0 aggregate_expected aggregate commands workspace_root runtime_log_root audit_root repo_root
  local original_mode_value latched status trigger_output trigger_payload trigger_pid trigger_gate
  local active_output_a active_output_b active_payload_a active_payload_b active_identity_file
  local health_observer_evidence health_observer_log health_observer_pid
  local old_runtime_container new_runtime_container response write_payload write_response
  local grep_payload grep_response edit_payload edit_response read_payload read_response
  local attempts protected_path

  wait_cgroup_root_empty || return 1
  status=$(runtime_get /v1/status) || return 1
  printf '%s' "$status" | jq -e '.http_status == 200 and .body.ok and .body.bash_ready and .body.fetch_ready' >/dev/null || return 1

  aggregate_expected="/sys/fs/cgroup/$AGENT_RUNTIME_CGROUP_PARENT"
  aggregate=$(realpath -e -- "$aggregate_expected" 2>/dev/null || true)
  commands=$(realpath -e -- "$AGENT_RUNTIME_CGROUP_HOST_ROOT" 2>/dev/null || true)
  [ "$aggregate" = "$aggregate_expected" ] || return 1
  [ -n "$commands" ] && [ "$commands" = "$aggregate/commands" ] || return 1
  [ "$(dirname -- "$commands")" = "$aggregate" ] && [ "${commands##*/}" = commands ] || return 1
  [ "$commands" != / ] && [ "$commands" != /sys/fs/cgroup ] && [ "$commands" != "$aggregate" ] || return 1
  [[ "$commands" != *$'\n'* ]] && [[ "$commands" != *$'\t'* ]] || return 1
  [ "$(stat -fc '%T' -- "$commands" 2>/dev/null || true)" = cgroup2fs ] || return 1
  [ "$(stat -Lc '%d' -- "$commands")" = "$(stat -Lc '%d' -- "$aggregate")" ] || return 1

  workspace_root=$(realpath -e -- "$AGENT_RUNTIME_WORKSPACE_HOST_ROOT") || return 1
  runtime_log_root=$(realpath -e -- "$AGENT_RUNTIME_LOG_HOST_ROOT") || return 1
  audit_root=$(realpath -e -- "$AGENT_FETCH_AUDIT_HOST_ROOT") || return 1
  repo_root=$(realpath -e -- "$REPO_ROOT") || return 1
  for protected_path in "$workspace_root" "$runtime_log_root" "$audit_root" "$repo_root"; do
    case "$protected_path" in "$commands"|"$commands"/*) return 1 ;; esac
    case "$commands" in "$protected_path"|"$protected_path"/*) return 1 ;; esac
  done

  enforcement_commands_path=$commands
  enforcement_commands_device=$(stat -Lc '%d' -- "$commands") || return 1
  enforcement_commands_inode=$(stat -Lc '%i' -- "$commands") || return 1
  enforcement_original_mode=$(stat -Lc '%a' -- "$commands") || return 1
  enforcement_original_uid=$(stat -Lc '%u' -- "$commands") || return 1
  enforcement_original_gid=$(stat -Lc '%g' -- "$commands") || return 1
  [[ "$enforcement_original_mode" =~ ^[0-7]{3,4}$ ]] || return 1
  [ "$enforcement_original_uid" = 10001 ] || return 1
  setpriv --reuid=10001 --regid=10001 --clear-groups /usr/bin/test -x "$commands" || return 1
  setpriv --reuid=10001 --regid=10001 --clear-groups /usr/bin/test -w "$commands" || return 1
  original_mode_value=$((8#$enforcement_original_mode))
  [ "$((original_mode_value & 0200))" -ne 0 ] || return 1
  enforcement_restricted_mode=$(printf '%o' "$((original_mode_value & 07555))")
  [ "$enforcement_restricted_mode" != "$enforcement_original_mode" ] || return 1

  enforcement_permission_receipt=$(new_artifact enforcement-permission-receipt.txt) || return 1
  active_identity_file=$(new_artifact enforcement-active-cgroups.txt) || return 1
  active_output_a=$(new_artifact enforcement-active-a.json) || return 1
  active_output_b=$(new_artifact enforcement-active-b.json) || return 1
  active_payload_a=$(jq -nc '{namespace:"task9-enforcement",run_id:"active-a",cwd:"/workspace",command:"sleep 60",timeout:"90s"}') || return 1
  active_payload_b=$(jq -nc '{namespace:"task9-enforcement",run_id:"active-b",cwd:"/workspace",command:"sleep 60",timeout:"90s"}') || return 1
  runtime_post /v1/bash "$active_payload_a" >"$active_output_a" 2>/dev/null &
  enforcement_active_pid_a=$!
  if ! track_pid "$enforcement_active_pid_a"; then
    kill -TERM "$enforcement_active_pid_a" 2>/dev/null || true
    wait "$enforcement_active_pid_a" 2>/dev/null || true
    return 1
  fi
  runtime_post /v1/bash "$active_payload_b" >"$active_output_b" 2>/dev/null &
  enforcement_active_pid_b=$!
  if ! track_pid "$enforcement_active_pid_b"; then
    kill -TERM "$enforcement_active_pid_b" 2>/dev/null || true
    wait "$enforcement_active_pid_b" 2>/dev/null || true
    return 1
  fi

  attempts=250
  while [ "$attempts" -gt 0 ]; do
    if pid_is_tracked_process "$enforcement_active_pid_a" \
      && pid_is_tracked_process "$enforcement_active_pid_b" \
      && capture_enforcement_active_groups "$commands" "$active_identity_file"; then
      break
    fi
    attempts=$((attempts - 1))
    sleep 0.02
  done
  [ "$attempts" -gt 0 ] || return 1
  printf 'enforcement_active_requests=2\n' >>"$enforcement_permission_receipt" || return 1

  health_observer_evidence=$(new_artifact enforcement-health-observer.txt) || return 1
  health_observer_log=$(new_artifact enforcement-health-observer.log) || return 1
  enforcement_health_observer >"$health_observer_evidence" 2>"$health_observer_log" &
  health_observer_pid=$!
  if ! track_pid "$health_observer_pid"; then
    kill -TERM "$health_observer_pid" 2>/dev/null || true
    wait "$health_observer_pid" 2>/dev/null || true
    return 1
  fi
  attempts=200
  while [ "$attempts" -gt 0 ]; do
    grep -F -x 'enforcement_health_observer_ready=1' "$health_observer_evidence" >/dev/null 2>&1 && break
    pid_is_tracked_process "$health_observer_pid" || return 1
    attempts=$((attempts - 1))
    sleep 0.01
  done
  [ "$attempts" -gt 0 ] || return 1

  printf 'permission-restore\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$enforcement_commands_path" "$enforcement_commands_device" "$enforcement_commands_inode" \
    "$enforcement_original_mode" "$enforcement_original_uid" "$enforcement_original_gid" \
    >>"$tmp_dir/cleanup.journal" || return 1
  printf 'permission_restore_path=%s\npermission_restore_device=%s\npermission_restore_inode=%s\npermission_restore_mode=%s\npermission_restore_uid=%s\npermission_restore_gid=%s\n' \
    "$enforcement_commands_path" "$enforcement_commands_device" "$enforcement_commands_inode" \
    "$enforcement_original_mode" "$enforcement_original_uid" "$enforcement_original_gid" \
    >>"$enforcement_permission_receipt" || return 1
  enforcement_restore_pending=1
  chmod "$enforcement_restricted_mode" -- "$enforcement_commands_path" || return 1
  [ "$(stat -Lc '%a' -- "$enforcement_commands_path")" = "$enforcement_restricted_mode" ] || return 1
  if setpriv --reuid=10001 --regid=10001 --clear-groups /usr/bin/test -w "$enforcement_commands_path"; then
    return 1
  fi

  trigger_output=$(new_artifact enforcement-trigger.json) || return 1
  trigger_gate=$(new_artifact enforcement-trigger.gate) || return 1
  trigger_payload=$(jq -nc '{namespace:"task9-enforcement",run_id:"trigger",cwd:"/workspace",command:"printf must-not-run",timeout:"5s"}') || return 1
  (
    while ! grep -F -x go "$trigger_gate" >/dev/null 2>&1; do sleep 0.001; done
    runtime_post /v1/bash "$trigger_payload"
  ) >"$trigger_output" 2>/dev/null &
  trigger_pid=$!
  if ! track_pid "$trigger_pid"; then
    kill -TERM "$trigger_pid" 2>/dev/null || true
    wait "$trigger_pid" 2>/dev/null || true
    return 1
  fi
  printf 'go\n' >"$trigger_gate" || return 1
  latched=0
  attempts=500
  while [ "$attempts" -gt 0 ]; do
    if grep -F -x 'enforcement_health_observed=1' "$health_observer_evidence" >/dev/null 2>&1; then
      latched=1
      break
    fi
    pid_is_tracked_process "$health_observer_pid" || return 1
    attempts=$((attempts - 1))
    sleep 0.001
  done
  [ "$latched" -eq 1 ] || return 1
  enforcement_active_groups_intact "$commands" "$active_identity_file" || return 1
  restore_enforcement_commands_permissions || result=1
  [ "$result" -eq 0 ] || return 1
  enforcement_active_groups_intact "$commands" "$active_identity_file" || return 1
  printf 'enforcement_restore_before_active_cgroup_removal=1\n' >>"$enforcement_permission_receipt" || return 1
  wait "$health_observer_pid" || return 1
  untrack_pid "$health_observer_pid"

  wait "$trigger_pid" || return 1
  untrack_pid "$trigger_pid"
  jq -e '
    .http_status == 503 and
    (.body.error | contains("bash unavailable: command enforcement failed"))
  ' "$trigger_output" >/dev/null || return 1
  printf 'enforcement_trigger_http_503=1\n' >>"$enforcement_permission_receipt" || return 1

  wait "$enforcement_active_pid_a" || return 1
  untrack_pid "$enforcement_active_pid_a"
  wait "$enforcement_active_pid_b" || return 1
  untrack_pid "$enforcement_active_pid_b"
  jq -e '.http_status == 400 and .body.error == "command canceled"' "$active_output_a" >/dev/null || return 1
  jq -e '.http_status == 400 and .body.error == "command canceled"' "$active_output_b" >/dev/null || return 1
  printf 'enforcement_active_requests_canceled=2\n' >>"$enforcement_permission_receipt" || return 1
  wait_cgroup_root_empty || return 1
  printf 'enforcement_no_unisolated_child=1\n' >>"$enforcement_permission_receipt" || return 1

  status=$(runtime_get /v1/status) || return 1
  printf '%s' "$status" | jq -e '
    .http_status == 200 and (.body.ok | not) and (.body.bash_ready | not) and
    (.body.readiness_error | contains("bash unavailable: command enforcement failed"))
  ' >/dev/null || return 1
  response=$(bash_response 'printf must-not-run-after-latch' task9-enforcement after-latch 5s 2>/dev/null || true)
  printf '%s' "$response" | jq -e '
    .http_status == 503 and
    (.body.error | contains("bash unavailable: command enforcement failed"))
  ' >/dev/null || return 1
  printf 'enforcement_bash_latched=1\n' >>"$enforcement_permission_receipt" || return 1

  write_payload=$(jq -nc '{namespace:"task9-enforcement",run_id:"local-apis",cwd:"/workspace",path:"/workspace/health.txt",content:"health-latch\n"}')
  write_response=$(runtime_post /v1/write "$write_payload") || return 1
  printf '%s' "$write_response" | jq -e '.http_status == 200 and .body.ok' >/dev/null || return 1
  grep_payload=$(jq -nc '{namespace:"task9-enforcement",run_id:"local-apis",cwd:"/workspace",pattern:"health-latch",path:"/workspace/health.txt"}')
  grep_response=$(runtime_post /v1/grep "$grep_payload") || return 1
  printf '%s' "$grep_response" | jq -e '.http_status == 200 and .body.ok and (.body.output | contains("health-latch"))' >/dev/null || return 1
  edit_payload=$(jq -nc '{namespace:"task9-enforcement",run_id:"local-apis",cwd:"/workspace",path:"/workspace/health.txt",patch:"@@ -1 +1 @@\n-health-latch\n+health-restored\n"}')
  edit_response=$(runtime_post /v1/edit "$edit_payload") || return 1
  printf '%s' "$edit_response" | jq -e '.http_status == 200 and .body.ok' >/dev/null || return 1
  read_payload=$(jq -nc '{namespace:"task9-enforcement",run_id:"local-apis",cwd:"/workspace",path:"/workspace/health.txt"}')
  read_response=$(runtime_post /v1/read "$read_payload") || return 1
  printf '%s' "$read_response" | jq -e '.http_status == 200 and .body.content == "health-restored\n"' >/dev/null || return 1
  printf 'enforcement_local_apis_ready=1\n' >>"$enforcement_permission_receipt" || return 1
  reset_namespace task9-enforcement >/dev/null || return 1

  old_runtime_container=$(dc ps -q agent-runtime) || return 1
  old_runtime_container=$(docker inspect -f '{{.Id}}' "$old_runtime_container") || return 1
  dc up -d --no-deps --force-recreate agent-runtime >/dev/null 2>&1 || return 1
  wait_container_health agent-runtime healthy 40 || return 1
  new_runtime_container=$(dc ps -q agent-runtime) || return 1
  new_runtime_container=$(docker inspect -f '{{.Id}}' "$new_runtime_container") || return 1
  [[ "$old_runtime_container" =~ ^[0-9a-f]{64}$ ]] && [[ "$new_runtime_container" =~ ^[0-9a-f]{64}$ ]] || return 1
  [ "$new_runtime_container" != "$old_runtime_container" ] || return 1
  status=$(runtime_get /v1/status) || return 1
  printf '%s' "$status" | jq -e '.http_status == 200 and .body.ok and .body.bash_ready and .body.fetch_ready' >/dev/null || return 1
  wait_cgroup_root_empty || return 1
  printf 'enforcement_runtime_recreated_healthy=1\n' >>"$enforcement_permission_receipt"
}
case_run runtime-enforcement-health-latch 'two active deployed commands are canceled by one real delegated cgroup creation failure; exact permission restoration precedes cleanup, Bash stays latched while local APIs work, and Runtime recreation restores readiness' runtime_enforcement_health_latch || true
if [ "$fail_count" -ne 0 ]; then
  exit 1
fi

topology_ok=1
runtime_networks=$(docker inspect agent-runtime | jq -c '.[0].NetworkSettings.Networks | keys | sort')
broker_networks=$(docker inspect agent-fetch-broker | jq -c '.[0].NetworkSettings.Networks | keys | sort')
fixture_networks=$(docker inspect agent-fetch-fixture | jq -c '.[0].NetworkSettings.Networks | keys | sort')
[ "$runtime_networks" = '["bot-runtime-control"]' ] || topology_ok=0
[ "$broker_networks" = '["fetch-egress"]' ] || topology_ok=0
[ "$fixture_networks" = '["fetch-egress"]' ] || topology_ok=0
docker inspect agent-runtime agent-fetch-fixture | jq -e --arg guard "$AGENT_RUNTIME_SECURITY_TEST_GUARD" '
  (all(.[].Config.Labels;
    .["org.csusters.agent-runtime.security-test-only"] == "true" and
    .["org.csusters.agent-runtime.security-test-guard"] == $guard)) and
  ((.[0].Config.Env | index("AGENT_RUNTIME_SECURITY_TEST_ONLY=1")) != null)
' >/dev/null || topology_ok=0
docker inspect agent-fetch-broker agent-fetch-fixture | jq -e '
  .[0].NetworkSettings.Networks["fetch-egress"].IPAddress == "11.0.0.2" and
  .[1].NetworkSettings.Networks["fetch-egress"].IPAddress == "11.0.0.10"
' >/dev/null || topology_ok=0
docker inspect agent-fetch-broker | jq -e '
  .[0] as $c |
  ($c.Mounts | map(.Destination) | sort) == ["/run/agent-fetch","/run/secrets/agent_fetch_hmac_key","/var/log/agent-fetch"] and
  ($c.Config.Env | map(select(startswith("AGENT_RUNTIME_TOKEN=") or startswith("BOT_") or startswith("REDIS_"))) | length) == 0
' >/dev/null || topology_ok=0
if [ "$topology_ok" -eq 1 ]; then
  pass_case live-topology 'test-only labels, Runtime/control, Broker/Fetch, fixture IP, mounts, and secrets match least-privilege topology'
else
  fail_case live-topology 'live container inspection found an unexpected network, mount, or secret route'
fi

runtime_pcap=$(new_artifact runtime.pcap)
broker_pcap=$(new_artifact broker.pcap)
broker_unapproved_pcap=$(new_artifact broker-unapproved.pcap)
start_capture agent-runtime "$runtime_pcap" '(ip or ip6) and not host 127.0.0.1 and not host ::1' || {
  fail_case namespace-capture-start 'Runtime tcpdump could not start read-only'; exit 1;
}
start_capture agent-fetch-broker "$broker_pcap" 'host 11.0.0.10 and port 8080' || {
  fail_case namespace-capture-start 'Broker tcpdump could not start read-only'; exit 1;
}
start_capture agent-fetch-broker "$broker_unapproved_pcap" '(ip or ip6) and not host 11.0.0.10 and not host 11.0.0.2' || {
  fail_case namespace-capture-start 'Broker unapproved-target tcpdump could not start read-only'; exit 1;
}
pass_case namespace-capture-start 'read-only Runtime and Broker namespace captures are active'

python_socket_probe_command() {
  family=$1
  address=$2
  cat <<EOF
python3 - '$family' '$address' <<'PY'
import errno
import socket
import sys

family = socket.AF_INET if sys.argv[1] == "inet" else socket.AF_INET6
try:
    client = socket.socket(family, socket.SOCK_STREAM)
    client.connect((sys.argv[2], 8080))
except OSError as error:
    raise SystemExit(0 if error.errno == errno.EPERM else 3)
raise SystemExit(2)
PY
EOF
}

unique_egress() {
  fixture_reset >/dev/null || return 1
  python_v4=$(python_socket_probe_command inet 11.0.0.10) || return 1
  python_v6=$(python_socket_probe_command inet6 ::ffff:11.0.0.10) || return 1
  commands=(
    'curl -v --connect-timeout 2 http://11.0.0.10:8080/items >/dev/null 2>/tmp/net.err; rc=$?; test "$rc" -ne 0; grep -qi "operation not permitted" /tmp/net.err'
    'wget -T 2 -O /dev/null http://11.0.0.10:8080/items 2>/tmp/net.err; rc=$?; test "$rc" -ne 0; grep -qi "operation not permitted" /tmp/net.err'
    'GIT_CURL_VERBOSE=1 git ls-remote http://11.0.0.10:8080/repository >/dev/null 2>/tmp/net.err; rc=$?; test "$rc" -ne 0; grep -qi "operation not permitted" /tmp/net.err'
    '(: >/dev/tcp/11.0.0.10/8080) 2>/tmp/net.err; rc=$?; test "$rc" -ne 0; grep -qi "operation not permitted" /tmp/net.err'
    "$python_v4"
    "$python_v6"
    'node -e '\''const n=require("net").connect({host:"11.0.0.10",port:8080});n.on("connect",()=>process.exit(2));n.on("error",e=>process.exit(e.code==="EPERM"?0:3));'\'''
    'test "$(agent-runtime-net-probe socket inet)" = errno=1; test "$(agent-runtime-net-probe socket inet6)" = errno=1'
  )
  for command in "${commands[@]}"; do
    assert_bash_success "$command" task9-main unique-egress 10s || return 1
  done
  state=$(fixture_state) || return 1
  printf '%s' "$state" | jq -e '.requests_total == 0' >/dev/null
}
case_run unique-egress 'curl, wget, git, /dev/tcp, Python v4/v6, Node, and net-probe reach socket EPERM with zero fixture connections' unique_egress || true

fetch_success_paths() {
  fixture_reset >/dev/null || return 1
  write_payload=$(jq -nc '{namespace:"task9-main",run_id:"matrix-run",cwd:"/workspace",path:"/workspace/report.txt",content:"fixture report\n"}')
  write_response=$(runtime_post /v1/write "$write_payload") || return 1
  printf '%s' "$write_response" | jq -e '.http_status == 200 and .body.ok' >/dev/null || return 1

  response=$(bash_response "fetch GET http://11.0.0.10:8080/items | jq '.items[]'") || return 1
  printf '%s' "$response" | jq -e '.http_status == 200 and .body.exit_code == 0 and .body.stdout == "\"alpha\"\n\"beta\"\n" and .body.stderr == ""' >/dev/null || return 1
  response=$(bash_response 'fetch POST http://11.0.0.10:8080/items name=value count:=2') || return 1
  printf '%s' "$response" | jq -e '.body.exit_code == 0 and (.body.stdout | fromjson) == {count:2,name:"value"}' >/dev/null || return 1
  response=$(bash_response 'fetch POST http://11.0.0.10:8080/upload --form file@/workspace/report.txt') || return 1
  printf '%s' "$response" | jq -e '.body.exit_code == 0 and (.body.stdout | fromjson | .received_bytes > 0)' >/dev/null || return 1
  response=$(bash_response 'printf raw | fetch PUT http://11.0.0.10:8080/raw --raw @-') || return 1
  printf '%s' "$response" | jq -e '.body.exit_code == 0 and .body.stdout == "raw"' >/dev/null || return 1
}
case_run fetch-success-paths 'the four exact approved Fetch commands preserve body semantics through the Broker' fetch_success_paths || true

command_control_env_copy_binding() {
  fixture_reset >/dev/null || return 1
  audit_before=$(awk 'END {print NR + 0}' "$AGENT_FETCH_AUDIT_HOST_ROOT/audit.jsonl" 2>/dev/null || printf 0)
  command_a=$(cat <<'COMMAND'
tr '\000' '\n' </proc/$$/environ | sort >/tmp/initial-env
test "$(cut -d= -f1 /tmp/initial-env | paste -sd, -)" = "AGENT_FETCH_CONTROL_FD,HOME,PATH"
test "$(sed -n 's/^PATH=//p' /tmp/initial-env)" = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
test "$(sed -n 's/^HOME=//p' /tmp/initial-env)" = /tmp
fd_number=$(sed -n 's/^AGENT_FETCH_CONTROL_FD=//p' /tmp/initial-env)
test "$fd_number" = 4
test ! -e /run/agent-fetch
test ! -e /run/secrets/agent-fetch-hmac-key
printf '%s' "$fd_number" >/workspace/copied-control-fd
COMMAND
)
  assert_bash_success "$command_a" task9-cross-a control-copy-a 5s || return 1
  command_b=$(cat <<'COMMAND'
copied=$(cat /workspace/copied-control-fd)
test "$copied" = 4
AGENT_FETCH_CONTROL_FD="$copied" fetch GET http://11.0.0.10:8080/items >/tmp/copied-result
jq -e '.items == ["alpha","beta"]' /tmp/copied-result >/dev/null
COMMAND
)
  assert_bash_success "$command_b" task9-cross-a control-copy-b 10s || return 1
  state=$(fixture_state) || return 1
  printf '%s' "$state" | jq -e '.requests_total == 1 and .route_counts["/items"] == 1' >/dev/null || return 1
  namespace_hash=$(printf %s task9-cross-a | sha256sum | awk '{print $1}')
  run_a_hash=$(printf %s control-copy-a | sha256sum | awk '{print $1}')
  run_b_hash=$(printf %s control-copy-b | sha256sum | awk '{print $1}')
  wait_for_audit_completions control-copy-b "$namespace_hash" "$run_b_hash" 1 completed || return 1
  jq -s -e --arg namespace "$namespace_hash" --arg run_a "$run_a_hash" --arg run_b "$run_b_hash" --argjson before "$audit_before" '
    .[$before:] as $records |
    any($records[]; .event == "completion" and .namespace_sha256 == $namespace and .run_id_sha256 == $run_b) and
    ([$records[] | select(.run_id_sha256 == $run_a)] | length) == 0
  ' "$AGENT_FETCH_AUDIT_HOST_ROOT/audit.jsonl" >/dev/null
}
case_run command-control-env-copy-binding 'initial Shell env is exactly PATH/HOME/control-FD=4 with no Broker material; copied number selects B own endpoint and audit identity B' command_control_env_copy_binding || true

post_exit_receiver_command() {
  cat <<'COMMAND'
python3 - <<'PY'
import array
import json
import os
import socket
import time

path = "/workspace/control-transfer.sock"
for stale in (path, "/workspace/control-transfer-ready", "/workspace/control-transfer-go"):
    try:
        os.unlink(stale)
    except FileNotFoundError:
        pass
listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
listener.bind(path)
listener.listen(1)
connection, _ = listener.accept()
message, ancillary, _, _ = connection.recvmsg(1, socket.CMSG_SPACE(array.array("i").itemsize))
descriptors = array.array("i")
for level, kind, payload in ancillary:
    if level == socket.SOL_SOCKET and kind == socket.SCM_RIGHTS:
        descriptors.frombytes(payload[: len(payload) - (len(payload) % descriptors.itemsize)])
assert message == b"F" and len(descriptors) == 1
received = descriptors[0]
with open("/workspace/control-transfer-ready", "w", encoding="ascii") as marker:
    marker.write("ready\n")
deadline = time.monotonic() + 10
while not os.path.exists("/workspace/control-transfer-go"):
    if time.monotonic() >= deadline:
        raise SystemExit("post-exit trigger timed out")
    time.sleep(0.01)

metadata = json.dumps({
    "protocol_version": 1,
    "request": {
        "protocol_version": 1,
        "method": "GET",
        "url": "http://11.0.0.10:8080/items",
        "headers": [],
        "follow": False,
        "check_status": False,
        "timeout_ms": 1000,
        "declared_body_bytes": 0,
    },
}, separators=(",", ":")).encode()
client, runtime = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
control = socket.socket(fileno=received)
unavailable = False
try:
    rights = array.array("i", [runtime.fileno()])
    control.sendmsg([metadata], [(socket.SOL_SOCKET, socket.SCM_RIGHTS, rights)])
except OSError:
    unavailable = True
runtime.close()
if not unavailable:
    client.settimeout(0.5)
    try:
        unavailable = client.recv(1) == b""
    except (ConnectionError, TimeoutError, OSError):
        unavailable = True
assert unavailable
client.close()
control.close()
connection.close()
listener.close()
os.unlink(path)
print("post-exit-revoked")
PY
COMMAND
}

post_exit_sender_command() {
  cat <<'COMMAND'
python3 - <<'PY'
import array
import os
import socket
import time

path = "/workspace/control-transfer.sock"
deadline = time.monotonic() + 5
while True:
    sender = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    try:
        sender.connect(path)
        break
    except FileNotFoundError:
        sender.close()
        if time.monotonic() >= deadline:
            raise
        time.sleep(0.01)
rights = array.array("i", [int(os.environ["AGENT_FETCH_CONTROL_FD"])])
sender.sendmsg([b"F"], [(socket.SOL_SOCKET, socket.SCM_RIGHTS, rights)])
sender.close()
PY
COMMAND
}

command_control_post_exit_revocation() {
  fixture_reset >/dev/null || return 1
  receiver_output=$(new_artifact post-exit-receiver-api.json)
  receiver=$(post_exit_receiver_command) || return 1
  payload=$(jq -nc --arg command "$receiver" '{namespace:"task9-cross-b",run_id:"control-receiver-b",cwd:"/workspace",command:$command,timeout:"20s"}')
  runtime_post /v1/bash "$payload" >"$receiver_output" 2>/dev/null &
  receiver_api_pid=$!
  track_pid "$receiver_api_pid" || return 1
  sleep 0.2
  sender=$(post_exit_sender_command) || return 1
  if ! assert_bash_success "$sender" task9-cross-b control-sender-a 10s; then
    kill -TERM "$receiver_api_pid" 2>/dev/null || true
    wait "$receiver_api_pid" 2>/dev/null || true
    untrack_pid "$receiver_api_pid"
    return 1
  fi
  ready_payload=$(jq -nc '{namespace:"task9-cross-b",run_id:"control-check",cwd:"/workspace",path:"/workspace/control-transfer-ready"}')
  ready=$(runtime_post /v1/read "$ready_payload") || return 1
  printf '%s' "$ready" | jq -e '.http_status == 200 and .body.content == "ready\n"' >/dev/null || return 1
  requests_before=$(fixture_state | jq -r '.requests_total') || return 1
  audit_before=$(awk 'END {print NR + 0}' "$AGENT_FETCH_AUDIT_HOST_ROOT/audit.jsonl") || return 1
  trigger_payload=$(jq -nc '{namespace:"task9-cross-b",run_id:"control-trigger",cwd:"/workspace",path:"/workspace/control-transfer-go",content:"go\n"}')
  trigger=$(runtime_post /v1/write "$trigger_payload") || return 1
  printf '%s' "$trigger" | jq -e '.http_status == 200 and .body.ok' >/dev/null || return 1
  if ! wait "$receiver_api_pid"; then
    untrack_pid "$receiver_api_pid"
    return 1
  fi
  untrack_pid "$receiver_api_pid"
  jq -e '.http_status == 200 and .body.exit_code == 0 and .body.stdout == "post-exit-revoked\n"' "$receiver_output" >/dev/null || return 1
  requests_after=$(fixture_state | jq -r '.requests_total') || return 1
  audit_after=$(awk 'END {print NR + 0}' "$AGENT_FETCH_AUDIT_HOST_ROOT/audit.jsonl") || return 1
  [ "$requests_before" = "$requests_after" ] && [ "$audit_before" = "$audit_after" ] && wait_cgroup_root_empty
}
case_run command-control-post-exit-revocation 'B may receive A control FD while A is active, but only after A API cleanup the retained endpoint is unavailable with zero new fixture/audit work' command_control_post_exit_revocation || true

fetch_stream_contracts() {
  command='fetch --headers GET http://11.0.0.10:8080/items >/workspace/header-body 2>/workspace/header-meta; grep -q '\''"items"'\'' /workspace/header-body; grep -q '\''HTTP 200 OK'\'' /workspace/header-meta; grep -qi '\''x-fixture: items'\'' /workspace/header-meta; ! grep -q '\''"items"'\'' /workspace/header-meta'
  assert_bash_success "$command" || return 1
  assert_bash_success 'fetch GET http://11.0.0.10:8080/items > /workspace/redirected.json; jq -e '\''.items == ["alpha","beta"]'\'' /workspace/redirected.json >/dev/null' || return 1
  assert_bash_success 'printf old > /workspace/atomic.json; fetch --output /workspace/atomic.json GET http://11.0.0.10:8080/items; jq -e '\''.items | length == 2'\'' /workspace/atomic.json >/dev/null; test -z "$(find /workspace -type f -name '\''.atomic.json.agent-runtime-????????????????.tmp'\'' -print -quit)"' || return 1
  assert_bash_success 'printf preserved > /workspace/atomic-failure.txt; fetch --check-status --output /workspace/atomic-failure.txt GET http://11.0.0.10:8080/status/404 >/tmp/body 2>/tmp/meta; test "$?" -eq 22; test "$(cat /workspace/atomic-failure.txt)" = preserved; test -z "$(find /workspace -type f -name '\''.atomic-failure.txt.agent-runtime-????????????????.tmp'\'' -print -quit)"' || return 1
  assert_bash_success 'printf preserved > /workspace/atomic-timeout.txt; fetch --timeout 50ms --output /workspace/atomic-timeout.txt GET http://11.0.0.10:8080/slow >/tmp/body 2>/tmp/meta; rc=$?; test "$rc" -eq 28; test "$(cat /workspace/atomic-timeout.txt)" = preserved; test -z "$(find /workspace -type f -name '\''.atomic-timeout.txt.agent-runtime-????????????????.tmp'\'' -print -quit)"' || return 1
  assert_bash_success 'test "$(fetch GET http://11.0.0.10:8080/compressed/gzip)" = "compressed fixture response"; test "$(fetch GET http://11.0.0.10:8080/compressed/deflate)" = "compressed fixture response"' || return 1
}
case_run fetch-stream-contracts 'stdout/stderr split, redirection, success/failure atomic output, gzip, and deflate are real-surface verified' fetch_stream_contracts || true

header_separator_real_surface() {
  fixture_reset >/dev/null || return 1
  assert_bash_success "fetch GET http://11.0.0.10:8080/items 'X-Equals:value=a=b' 'X-Typed:value:=inside' 'X-At:value@name' >/dev/null" \
    task9-main header-separators 10s || return 1
  state=$(fixture_state) || return 1
  equals_hash=$(printf %s 'value=a=b' | sha256sum | awk '{print $1}')
  typed_hash=$(printf %s 'value:=inside' | sha256sum | awk '{print $1}')
  at_hash=$(printf %s 'value@name' | sha256sum | awk '{print $1}')
  printf '%s' "$state" | jq -e \
    --arg equals "$equals_hash" --arg typed "$typed_hash" --arg at "$at_hash" '
    .requests_total == 1 and
    .events[-1].route == "/items" and
    (first(.events[-1].headers[] | select(.name == "x-equals")) == {name:"x-equals",value_bytes:9,value_sha256:$equals}) and
    (first(.events[-1].headers[] | select(.name == "x-typed")) == {name:"x-typed",value_bytes:13,value_sha256:$typed}) and
    (first(.events[-1].headers[] | select(.name == "x-at")) == {name:"x-at",value_bytes:10,value_sha256:$at})
  ' >/dev/null
}
case_run header-separator-real-surface 'header values containing =, :=, and @ reach the fixture unchanged with exact lengths and hashes' header_separator_real_surface || true

workspace_root_independent_of_cwd() {
  reset_namespace task9-cross-b >/dev/null || return 1
  fixture_reset >/dev/null || return 1
  write_payload=$(jq -nc '{namespace:"task9-cross-b",run_id:"workspace-root",cwd:"/workspace",path:"/workspace/report.txt",content:"workspace-root-sentinel\n"}')
  write_response=$(runtime_post /v1/write "$write_payload") || return 1
  printf '%s' "$write_response" | jq -e '.http_status == 200 and .body.ok' >/dev/null || return 1
  subdir_payload=$(jq -nc '{namespace:"task9-cross-b",run_id:"workspace-root",cwd:"/workspace",path:"/workspace/subdir/marker",content:"marker\n"}')
  subdir_response=$(runtime_post /v1/write "$subdir_payload") || return 1
  printf '%s' "$subdir_response" | jq -e '.http_status == 200 and .body.ok' >/dev/null || return 1

  index=0
  for cwd in / /tmp /workspace /workspace/subdir; do
    index=$((index + 1))
    raw_relative='test "$(fetch PUT http://11.0.0.10:8080/raw --raw @report.txt)" = workspace-root-sentinel'
    assert_bash_success "$raw_relative" task9-cross-b "workspace-raw-relative-$index" 10s "$cwd" || return 1
    raw_absolute='test "$(fetch PUT http://11.0.0.10:8080/raw --raw @/workspace/report.txt)" = workspace-root-sentinel'
    assert_bash_success "$raw_absolute" task9-cross-b "workspace-raw-absolute-$index" 10s "$cwd" || return 1
    upload_relative='fetch POST http://11.0.0.10:8080/upload --form file@report.txt | jq -e '\''.received_bytes > 0 and (.sha256 | length) == 64'\'' >/dev/null'
    assert_bash_success "$upload_relative" task9-cross-b "workspace-upload-relative-$index" 10s "$cwd" || return 1
    upload_absolute='fetch POST http://11.0.0.10:8080/upload --form file@/workspace/report.txt | jq -e '\''.received_bytes > 0 and (.sha256 | length) == 64'\'' >/dev/null'
    assert_bash_success "$upload_absolute" task9-cross-b "workspace-upload-absolute-$index" 10s "$cwd" || return 1
  done
  assert_bash_success 'ln -s report.txt /workspace/report-link; fetch PUT http://11.0.0.10:8080/raw --raw @/workspace/report-link >/tmp/body 2>/tmp/meta; rc=$?; rm -f /workspace/report-link; test "$rc" -eq 65' \
    task9-cross-b workspace-symlink 5s /tmp || return 1
  assert_bash_exit 65 'fetch PUT http://11.0.0.10:8080/raw --raw @../report.txt' task9-cross-b workspace-parent 5s /tmp || return 1
  assert_bash_exit 65 'fetch PUT http://11.0.0.10:8080/raw --raw @/skills' task9-cross-b workspace-skills 5s / || return 1
  state=$(fixture_state) || return 1
  printf '%s' "$state" | jq -e '.route_counts["/raw"] == 8 and .route_counts["/upload"] == 8' >/dev/null
}
case_run workspace-root-independent-of-cwd 'raw/upload relative and absolute workspace inputs are fixed across /, /tmp, /workspace, and subdir while parent/symlink/skills fail 65' workspace_root_independent_of_cwd || true

fetch_exit_contracts() {
  assert_bash_success 'fetch --check-status GET http://11.0.0.10:8080/status/404 >/tmp/body 2>/tmp/meta; test "$?" -eq 22' || return 1
  assert_bash_success 'fetch --timeout 1s GET http://11.0.0.10:8080/slow >/tmp/body 2>/tmp/meta; test "$?" -eq 28' || return 1
  assert_bash_success 'fetch GET http://127.0.0.1:8080/items >/tmp/body 2>/tmp/meta; test "$?" -eq 65' || return 1
  assert_bash_success 'AGENT_FETCH_CONTROL_FD=9 fetch GET http://11.0.0.10:8080/items >/tmp/body 2>/tmp/meta; test "$?" -eq 69' || return 1
  assert_bash_success 'mkdir -p /workspace/locked; chmod 500 /workspace/locked; fetch --output /workspace/locked/out GET http://11.0.0.10:8080/items >/tmp/body 2>/tmp/meta; rc=$?; chmod 700 /workspace/locked; test "$rc" -eq 70; test -z "$(find /workspace -type f -name '\''.out.agent-runtime-????????????????.tmp'\'' -print -quit)"' || return 1
}
case_run fetch-exit-contracts 'check-status=22, timeout=28, policy=65, unavailable control=69, and internal/output=70 remain exact' fetch_exit_contracts || true

fetch_cancellation() {
  before=$(fixture_state | jq -r '.disconnects') || return 1
  assert_bash_success 'fetch GET http://11.0.0.10:8080/endless | head -c 1 >/dev/null' || return 1
  attempts=50
  while [ "$attempts" -gt 0 ]; do
    after=$(fixture_state | jq -r '.disconnects') || return 1
    [ "$after" -gt "$before" ] && return 0
    attempts=$((attempts - 1))
    sleep 0.1
  done
  return 1
}
case_run fetch-cancellation 'head -c 1 propagates cancellation to a bounded endless fixture stream' fetch_cancellation || true

ssrf_routes() {
  fixture_reset >/dev/null || return 1
  urls=(
    'http://127.0.0.1:8080/items'
    'http://10.0.0.1:80/'
    'http://172.16.0.1:80/'
    'http://192.168.0.1:80/'
    'http://169.254.1.1:80/'
    'http://100.64.0.1:80/'
    'http://[::1]:80/'
    'http://[fc00::1]:80/'
    'http://[fe80::1]:80/'
    'http://169.254.169.254/latest/meta-data/'
    'http://11.0.0.1:80/'
    'http://11.0.0.2:8080/'
    "$AGENT_FETCH_TEST_HOST_URL"
    "$AGENT_FETCH_TEST_BOT_URL"
    "$AGENT_FETCH_TEST_REDIS_URL"
  )
  for url in "${urls[@]}"; do
    assert_bash_success "fetch GET '$url' >/tmp/body 2>/tmp/meta; test \"\$?\" -eq 65" task9-main ssrf-routes 10s || return 1
  done
  state=$(fixture_state) || return 1
  printf '%s' "$state" | jq -e '.requests_total == 0' >/dev/null
}
case_run ssrf-control-routes 'loopback/private/link-local/CGNAT/ULA/metadata/gateway/host/Bot/Redis/Broker targets reject before connection' ssrf_routes || true

ssrf_dns_redirect() {
  fixture_reset >/dev/null || return 1
  assert_bash_success 'fetch GET http://mixed.agent.test:8080/items >/tmp/body 2>/tmp/meta; test "$?" -eq 65' || return 1
  assert_bash_success 'fetch GET http://cname.agent.test:8080/items >/tmp/body 2>/tmp/meta; test "$?" -eq 65' || return 1
  state=$(fixture_state) || return 1
  printf '%s' "$state" | jq -e '.requests_total == 0 and (.dns_counts | length) > 0' >/dev/null || return 1
  assert_bash_success 'fetch --follow GET http://11.0.0.10:8080/redirect/control >/tmp/body 2>/tmp/meta; test "$?" -eq 65' || return 1
  fixture_reset >/dev/null || return 1
  assert_bash_success 'fetch --follow GET http://rebind.agent.test:8080/redirect/rebind >/tmp/body 2>/tmp/meta; test "$?" -eq 65' || return 1
  state=$(fixture_state) || return 1
  printf '%s' "$state" | jq -e '.route_counts["/redirect/:target"] == 1 and (.route_counts["/rebound"] // 0) == 0 and .dns_counts["rebind.agent.test.:1"] >= 2' >/dev/null
}
case_run ssrf-dns-redirect-rebinding 'mixed answers, restricted CNAME, control redirect, and second-resolution rebinding fail before forbidden connection' ssrf_dns_redirect || true

audit_redaction() {
  fixture_reset >/dev/null || return 1
  command='printf MATRIX_BODY_SENTINEL | fetch POST '\''http://11.0.0.10:8080/raw?secret=MATRIX_QUERY_SENTINEL'\'' '\''Authorization:Bearer-MATRIX_HEADER_SENTINEL'\'' '\''Cookie:MATRIX_COOKIE_SENTINEL'\'' '\''X-API-Key:MATRIX_APIKEY_SENTINEL'\'' --raw @- >/dev/null'
  assert_bash_success "$command" task9-main audit-redaction-stream 10s || return 1
  namespace_hash=$(printf %s task9-main | sha256sum | awk '{print $1}')
  run_hash=$(printf %s audit-redaction-stream | sha256sum | awk '{print $1}')
  wait_for_audit_completions audit-redaction "$namespace_hash" "$run_hash" 1 completed || return 1
  state_file=$(new_artifact fixture-state.json)
  fixture_state >"$state_file" || return 1
  for sentinel in MATRIX_BODY_SENTINEL MATRIX_QUERY_SENTINEL MATRIX_HEADER_SENTINEL MATRIX_COOKIE_SENTINEL MATRIX_APIKEY_SENTINEL; do
    ! grep -F -- "$sentinel" "$AGENT_FETCH_AUDIT_HOST_ROOT/audit.jsonl" >/dev/null 2>&1 || return 1
    ! grep -F -- "$sentinel" "$state_file" >/dev/null 2>&1 || return 1
  done
  grep -F 'query_sha256' "$AGENT_FETCH_AUDIT_HOST_ROOT/audit.jsonl" >/dev/null || return 1
  grep -F 'request_body_sha256' "$AGENT_FETCH_AUDIT_HOST_ROOT/audit.jsonl" >/dev/null || return 1
  grep -F 'sensitive_headers' "$AGENT_FETCH_AUDIT_HOST_ROOT/audit.jsonl" >/dev/null || return 1
  sentinel_bytes=$(printf %s MATRIX_BODY_SENTINEL | wc -c | tr -d ' ')
  sentinel_hash=$(printf %s MATRIX_BODY_SENTINEL | sha256sum | awk '{print $1}')
  empty_hash=e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
  jq -s -e \
    --arg namespace "$namespace_hash" --arg run "$run_hash" \
    --arg sentinel_hash "$sentinel_hash" --arg empty_hash "$empty_hash" \
    --argjson sentinel_bytes "$sentinel_bytes" '
    [.[] | select(
      .namespace_sha256 == $namespace and .run_id_sha256 == $run and
      .normalized_origin == "http://11.0.0.10:8080"
    )] as $records |
    [$records[] | select(.event == "start")] as $starts |
    [$records[] | select(.event == "completion")] as $completions |
    ($starts | length) == 1 and ($completions | length) == 1 and
    $starts[0].command_id_sha256 == $completions[0].command_id_sha256 and
    $starts[0].request_body_byte_len == 0 and
    $starts[0].request_body_sha256 == $empty_hash and
    $starts[0].query_byte_len > 0 and $starts[0].query_sha256 != $empty_hash and
    ($starts[0].sensitive_headers | length) >= 3 and
    all($starts[0].sensitive_headers[]; .byte_len > 0 and .sha256 != $empty_hash) and
    $completions[0].request_body_bytes == $sentinel_bytes and
    $completions[0].request_body_sha256 == $sentinel_hash and
    $completions[0].cancellation_reason == null and
    $completions[0].rejection_reason == null and
    ($completions[0].quota.requests_used >= 1) and
    ($completions[0].quota.concurrent_requests >= 0) and
    ($completions[0].quota.request_bytes_used >= 0) and
    ($completions[0].quota.response_bytes_used >= 0)
  ' "$AGENT_FETCH_AUDIT_HOST_ROOT/audit.jsonl" >/dev/null || return 1
  jq -e --argjson sentinel_bytes "$sentinel_bytes" --arg sentinel_hash "$sentinel_hash" '
    .server_errors == 0 and
    .limits == {bytes_max_bytes:8388608,max_events:256,max_request_body_bytes:8388608,stream_max_bytes:67108864} and
    all(.events[];
      (.query | keys | sort) == ["byte_len","sha256"] and
      (.request_body_sha256 | length) == 64 and
      (.response_body_sha256 | length) == 64 and
      all(.headers[]; (.value_sha256 | length) == 64))
    and .events[-1].request_body_bytes == $sentinel_bytes
    and .events[-1].request_body_sha256 == $sentinel_hash
  ' "$state_file" >/dev/null || return 1
}
case_run audit-redaction 'successful fixture/audit records contain bounded hashes, lengths, exact completed metadata, and zero sentinel plaintext' audit_redaction || true

broker_preauth_semaphore_deadline() {
  fixture_reset >/dev/null || return 1
  broker_probe_log=$(new_artifact broker-preauth-live.log)
  dc exec -T --user 10001:10001 agent-runtime python3 -c '
import json
import socket
import struct
import sys
import time

path = sys.argv[1]
held = []
for _ in range(64):
    stream = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    stream.connect(path)
    held.append(stream)
extra = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
extra.settimeout(0.5)
extra.connect(path)
assert extra.recv(1) == b""
extra.close()
print("held=64 extra=eof", flush=True)
time.sleep(2.2)
for stream in held:
    stream.settimeout(0.5)
    assert stream.recv(1) == b""
    stream.close()

fragmented = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
fragmented.settimeout(3)
fragmented.connect(path)
payload = json.dumps({"protocol_version": 1}, separators=(",", ":")).encode()
frame = b"\x01" + struct.pack("!I", len(payload)) + payload
fragmented.sendall(frame[:3])
time.sleep(2.2)
assert fragmented.recv(1) == b""
fragmented.close()
print("deadline=eof permits=recovered", flush=True)
' /run/agent-fetch/fetch.sock >"$broker_probe_log" 2>&1 &
  probe_pid=$!
  track_pid "$probe_pid" || return 1
  attempts=100
  while [ "$attempts" -gt 0 ] && ! grep -F 'held=64 extra=eof' "$broker_probe_log" >/dev/null 2>&1; do
    pid_is_tracked_process "$probe_pid" || break
    attempts=$((attempts - 1))
    sleep 0.02
  done
  [ "$attempts" -gt 0 ] || return 1
  broker_pid=$(docker inspect -f '{{.State.Pid}}' agent-fetch-broker) || return 1
  broker_cgroup_path=$(read_exact_unified_cgroup "$broker_pid") || return 1
  broker_cgroup=$(realpath -e -- "/sys/fs/cgroup$broker_cgroup_path") || return 1
  [ "$(cat "$broker_cgroup/pids.max")" = 128 ] || return 1
  [ "$(cat "$broker_cgroup/memory.max")" = 268435456 ] || return 1
  [ "$(cat "$broker_cgroup/memory.swap.max")" = 0 ] || return 1
  [ "$(cat "$broker_cgroup/pids.current")" -le 128 ] || return 1
  [ "$(cat "$broker_cgroup/memory.current")" -le 268435456 ] || return 1
  if ! wait "$probe_pid"; then
    untrack_pid "$probe_pid"
    return 1
  fi
  untrack_pid "$probe_pid"
  grep -F 'deadline=eof permits=recovered' "$broker_probe_log" >/dev/null || return 1
  assert_bash_success 'fetch GET http://11.0.0.10:8080/items | jq -e '\''.items == ["alpha","beta"]'\'' >/dev/null' \
    task9-main preauth-recovery 10s || return 1
  run_legacy_test_exact c2-image-preauth-task-metrics fetch_broker preauth_connection_limit_rejects_before_spawning_more_tasks || return 1
  run_legacy_test_exact c2-image-handshake-deadline fetch_broker silent_peer_is_closed_at_one_absolute_handshake_deadline || return 1
}
case_run broker-preauth-semaphore-deadline 'trusted UID10001 exec holds 64 silent Broker peers, 65th EOFs, one 2s deadline recovers permits/resources, normal proxy Fetch works, and built C2 task/metrics tests pass' broker_preauth_semaphore_deadline || true

trusted_broker_protocol_mismatch() {
  fixture_reset >/dev/null || return 1
  dc exec -T --user 10001:10001 agent-runtime python3 -c '
import json
import socket
import struct
import sys

payload = json.dumps({"protocol_version": 2}, separators=(",", ":")).encode()
stream = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
stream.settimeout(1)
stream.connect(sys.argv[1])
stream.sendall(b"\x01" + struct.pack("!I", len(payload)) + payload)
header = stream.recv(5)
assert len(header) == 5 and header[0] == 0x87
size = struct.unpack("!I", header[1:])[0]
body = b""
while len(body) < size:
    body += stream.recv(size - len(body))
assert json.loads(body)["code"] == "protocol"
' /run/agent-fetch/fetch.sock || return 1
  state=$(fixture_state) || return 1
  printf '%s' "$state" | jq -e '.requests_total == 0' >/dev/null
}
case_run direct-uds-invalid-protocol 'trusted non-Shell UID10001 Broker protocol mismatch returns Protocol before body or fixture connection without exposing path to command Shell' trusted_broker_protocol_mismatch || true

broker_server_connect_denial() {
  fixture_reset >/dev/null || return 1
  audit_before=$(awk 'END {print NR + 0}' "$AGENT_FETCH_AUDIT_HOST_ROOT/audit.jsonl") || return 1
  command=$(cat <<'COMMAND'
python3 - <<'PY'
import array
import json
import os
import socket
import struct

metadata = json.dumps({
    "protocol_version": 1,
    "request": {
        "protocol_version": 1,
        "method": "CONNECT",
        "url": "http://11.0.0.10:8080/items",
        "headers": [],
        "follow": False,
        "check_status": False,
        "timeout_ms": 1000,
        "declared_body_bytes": 0,
    },
}, separators=(",", ":")).encode()
client, runtime = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
control = socket.socket(fileno=os.dup(int(os.environ["AGENT_FETCH_CONTROL_FD"])))
control.sendmsg([metadata], [(socket.SOL_SOCKET, socket.SCM_RIGHTS, array.array("i", [runtime.fileno()]))])
runtime.close()
header = b""
while len(header) < 5:
    header += client.recv(5 - len(header))
kind, size = header[0], struct.unpack("!I", header[1:])[0]
body = b""
while len(body) < size:
    body += client.recv(size - len(body))
error = json.loads(body)
assert kind == 0xA5 and error["code"] == "policy"
client.close()
control.close()
PY
COMMAND
)
  assert_bash_success "$command" task9-main connect-denial 10s || return 1
  state=$(fixture_state) || return 1
  audit_after=$(awk 'END {print NR + 0}' "$AGENT_FETCH_AUDIT_HOST_ROOT/audit.jsonl") || return 1
  [ "$audit_before" = "$audit_after" ] || return 1
  printf '%s' "$state" | jq -e '.requests_total == 0 and (.dns_counts | length) == 0' >/dev/null || return 1
  run_legacy_test_exact c2-image-connect-zero-counters fetch_broker broker_rejects_connect_before_audit_body_dns_or_connector
}
case_run broker-server-connect-denial 'local command-control CONNECT reaches Broker internal auth then Policy with unchanged fixture/audit; built C2 proves body/DNS/connector counters stay zero' broker_server_connect_denial || true

if all_backgrounds_alive; then
  pass_case namespace-capture-liveness 'all tcpdump processes remained live through the complete network attack phase'
else
  fail_case namespace-capture-liveness 'a tcpdump process exited before packet assertions completed'
fi
if stop_backgrounds; then
  pass_case namespace-capture-stop 'all exact tracked tcpdump PIDs stopped and were reaped'
else
  fail_case namespace-capture-stop 'a tracked capture PID changed identity before exact cleanup'
fi
runtime_capture_text=$(new_artifact runtime-capture.txt)
broker_capture_text=$(new_artifact broker-capture.txt)
broker_unapproved_capture_text=$(new_artifact broker-unapproved-capture.txt)
capture_decode_ok=1
tcpdump -nn -r "$runtime_pcap" >"$runtime_capture_text" 2>/dev/null || capture_decode_ok=0
tcpdump -nn -r "$broker_pcap" >"$broker_capture_text" 2>/dev/null || capture_decode_ok=0
tcpdump -nn -r "$broker_unapproved_pcap" >"$broker_unapproved_capture_text" 2>/dev/null || capture_decode_ok=0
if [ "$capture_decode_ok" -eq 1 ]; then
  pass_case namespace-capture-decode 'all packet captures are valid and readable without timeout-as-success'
else
  fail_case namespace-capture-decode 'one or more packet captures could not be decoded'
fi
if [ ! -s "$runtime_capture_text" ]; then
  pass_case runtime-zero-packets 'Runtime namespace emitted zero non-loopback IP packets during direct-client and Fetch attacks'
else
  fail_case runtime-zero-packets 'Runtime namespace capture contains non-loopback IP packets'
fi
if [ ! -s "$broker_unapproved_capture_text" ]; then
  pass_case broker-zero-control-packets 'Broker namespace emitted zero packets outside its fixture/DNS test target'
else
  fail_case broker-zero-control-packets 'Broker namespace capture contains an unapproved control or non-fixture target'
fi
if grep -F '11.0.0.10.8080' "$broker_capture_text" >/dev/null 2>&1; then
  pass_case broker-approved-packets 'Broker namespace capture contains approved fixture HTTP flow'
else
  fail_case broker-approved-packets 'Broker namespace capture lacks the approved fixture flow'
fi

nft_counter() {
  chain=$1
  handle=$2
  case "$chain" in input|forward) ;; *) return 1 ;; esac
  nft -j list chain inet agent_fetch "$chain" | jq -er --argjson handle "$handle" '
    [.nftables[] | .rule? | select(.handle == $handle) | .expr[] | .counter?.packets] |
    map(select(. != null)) | if length == 1 then .[0] else error("counter handle missing") end'
}

nft_bypass_probe() {
  before_input=$(nft_counter input "$AGENT_FETCH_NFT_TEST_INPUT_RULE_HANDLE") || return 1
  before4=$(nft_counter forward "$AGENT_FETCH_NFT_TEST_IPV4_RULE_HANDLE") || return 1
  before6=$(nft_counter forward "$AGENT_FETCH_NFT_TEST_IPV6_RULE_HANDLE") || return 1
  nsenter -t "$AGENT_FETCH_NFT_TEST_PID" -n python3 -c '
import socket, sys
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM); s.settimeout(2)
try: s.connect((sys.argv[1], 80)); raise SystemExit(2)
except OSError: raise SystemExit(0)
' "$AGENT_FETCH_NFT_TEST_INPUT_TARGET" || return 1
  nsenter -t "$AGENT_FETCH_NFT_TEST_PID" -n python3 -c '
import socket, sys
family = socket.AF_INET6 if ":" in sys.argv[1] else socket.AF_INET
s = socket.socket(family, socket.SOCK_STREAM); s.settimeout(2)
try: s.connect((sys.argv[1], 80)); raise SystemExit(2)
except OSError: raise SystemExit(0)
' "$AGENT_FETCH_NFT_TEST_IPV4_TARGET" || return 1
  nsenter -t "$AGENT_FETCH_NFT_TEST_PID" -n python3 -c '
import socket, sys
s = socket.socket(socket.AF_INET6, socket.SOCK_STREAM); s.settimeout(2)
try: s.connect((sys.argv[1], 80)); raise SystemExit(2)
except OSError: raise SystemExit(0)
' "$AGENT_FETCH_NFT_TEST_IPV6_TARGET" || return 1
  after_input=$(nft_counter input "$AGENT_FETCH_NFT_TEST_INPUT_RULE_HANDLE") || return 1
  after4=$(nft_counter forward "$AGENT_FETCH_NFT_TEST_IPV4_RULE_HANDLE") || return 1
  after6=$(nft_counter forward "$AGENT_FETCH_NFT_TEST_IPV6_RULE_HANDLE") || return 1
  [ "$after_input" -gt "$before_input" ] \
    && [ "$after4" -gt "$before4" ] \
    && [ "$after6" -gt "$before6" ]
}
case_run nft-independent-input-forward 'preprovisioned namespace increments agent_fetch input gateway plus forward IPv4/IPv6 reject counters' nft_bypass_probe || true

aggregate_limits() {
  aggregate=$(realpath -e "/sys/fs/cgroup/$AGENT_RUNTIME_CGROUP_PARENT") || return 1
  [ "$(cat "$aggregate/pids.max")" = 512 ] \
    && [ "$(cat "$aggregate/memory.max")" = 1073741824 ] \
    && [ "$(cat "$aggregate/memory.swap.max")" = 0 ] \
    && [ "$(cat "$aggregate/cpu.max")" = '200000 100000' ]
}
case_run aggregate-resource-limits 'common ancestor remains pids512, memory1GiB, swap0, and CPU2' aggregate_limits || true

nproc_limit_observation() {
  response=$(bash_response 'limit=$(ulimit -u); printf "nproc=%s\n" "$limit"; test "$limit" -eq 480' task9-resource nproc-limit 5s) || return 1
  printf '%s' "$response" | jq -e \
    '.http_status == 200 and .body.exit_code == 0 and .body.stdout == "nproc=480\n"' >/dev/null
}

fork_bomb_case() {
  wait_cgroup_root_empty || return 1
  nproc_limit_observation || return 1
  runtime_pid=$(docker inspect -f '{{.State.Pid}}' agent-runtime) || return 1
  baseline=$(awk '/^Threads:/ {print $2}' "/proc/$runtime_pid/status") || return 1
  [ "$((baseline + 64))" -lt 480 ] || return 1
  output=$(new_artifact fork-bomb-api.json)
  payload=$(jq -nc '{namespace:"task9-resource",run_id:"fork-bomb",cwd:"/workspace",command:"for i in $(seq 1 200); do (sleep 3) & done; wait",timeout:"10s"}')
  runtime_post /v1/bash "$payload" >"$output" 2>/dev/null &
  api_pid=$!
  track_pid "$api_pid"
  group=$(wait_new_cgroup) || return 1
  attempts=100
  hit=0
  while [ "$attempts" -gt 0 ] && [ -d "$group" ]; do
    max_events=$(awk '$1 == "max" {print $2}' "$group/pids.events" 2>/dev/null || printf 0)
    if [ "$max_events" -gt 0 ]; then hit=1; break; fi
    attempts=$((attempts - 1))
    sleep 0.05
  done
  [ "$hit" -eq 1 ] || return 1
  assert_bash_success 'printf concurrent-ok' task9-resource fork-concurrent 5s || return 1
  if ! wait "$api_pid"; then
    untrack_pid "$api_pid"
    return 1
  fi
  untrack_pid "$api_pid"
  jq -e '.http_status == 200' "$output" >/dev/null || return 1
  wait_cgroup_root_empty
}
case_run fork-pids-containment 'command observes RLIMIT_NPROC=480; baseline headroom, pids.events:max, and concurrent execution all pass' fork_bomb_case || true

memory_bomb_case() {
  wait_cgroup_root_empty || return 1
  output=$(new_artifact memory-bomb-api.json)
  memory_command=$(cat <<'COMMAND'
python3 - <<'PY'
blocks = []
while True:
    blocks.append(bytearray(8 * 1024 * 1024))
PY
COMMAND
)
  payload=$(jq -nc --arg command "$memory_command" \
    '{namespace:"task9-resource",run_id:"memory-bomb",cwd:"/workspace",command:$command,timeout:"15s"}')
  runtime_post /v1/bash "$payload" >"$output" 2>/dev/null &
  api_pid=$!
  track_pid "$api_pid"
  group=$(wait_new_cgroup) || return 1
  attempts=200
  oom=0
  while [ "$attempts" -gt 0 ] && [ -d "$group" ]; do
    oom_events=$(awk '$1 == "oom_kill" {print $2}' "$group/memory.events" 2>/dev/null || printf 0)
    if [ "$oom_events" -gt 0 ]; then oom=1; break; fi
    attempts=$((attempts - 1))
    sleep 0.05
  done
  if ! wait "$api_pid"; then
    untrack_pid "$api_pid"
    return 1
  fi
  untrack_pid "$api_pid"
  [ "$oom" -eq 1 ] || return 1
  assert_bash_success 'printf supervisor-alive' task9-resource memory-survivor 5s || return 1
  wait_cgroup_root_empty
}
case_run memory-oom-containment 'memory bomb increments command OOM kill evidence without killing the Runtime supervisor' memory_bomb_case || true

cpu_fd_fsize_case() {
  response=$(bash_response 'while :; do :; done' task9-resource cpu-budget 30s) || true
  printf '%s' "$response" | jq -e '.http_status == 500 and (.body.error | contains("command CPU budget exceeded"))' >/dev/null || return 1
  response=$(bash_response 'sleep 10' task9-resource wall-budget 2s) || true
  printf '%s' "$response" | jq -e '.http_status == 400 and .body.error == "command timed out"' >/dev/null || return 1
  wait_cgroup_root_empty || return 1
  fd_command=$(cat <<'COMMAND'
python3 - <<'PY'
import errno

files = []
try:
    while True:
        files.append(open("/dev/null", "rb"))
except OSError as error:
    assert error.errno == errno.EMFILE
    assert len(files) <= 256
PY
COMMAND
)
  assert_bash_success "$fd_command" task9-resource fd-limit 10s || return 1
  assert_bash_success 'rm -f /workspace/fsize.bin; dd if=/dev/zero of=/workspace/fsize.bin bs=1M count=65 status=none 2>/tmp/fsize.err; rc=$?; size=$(stat -c %s /workspace/fsize.bin); rm -f /workspace/fsize.bin; test "$rc" -ne 0; test "$size" -le 67108864; { test "$rc" -eq 153 || grep -Eqi "file size limit|file too large" /tmp/fsize.err; }' task9-resource fsize-limit 20s || return 1
  wait_cgroup_root_empty
}
case_run cpu-fd-fsize 'busy loop hits CPU and wall budgets, FD opener stops at 256, and 65 MiB write hits RLIMIT_FSIZE' cpu_fd_fsize_case || true

bounded_disk_case() {
  reset_namespace task9-resource >/dev/null || return 1
  root_before=$(df --block-size=1 --output=avail / | awk 'NR == 2 {print $1}') || return 1
  response=$(bash_response 'i=0; while dd if=/dev/zero of="/workspace/fill-$i" bs=4096 count=1 status=none 2>/dev/null; do i=$((i+1)); done; test "$i" -gt 100; printf "filled=%s" "$i"' task9-resource disk-fill 30s) || return 1
  printf '%s' "$response" | jq -e '.http_status == 200 and .body.exit_code == 0 and (.body.stdout | startswith("filled="))' >/dev/null || return 1
  root_after=$(df --block-size=1 --output=avail / | awk 'NR == 2 {print $1}') || return 1
  [ "$root_before" = "$root_after" ] || return 1
  reset_namespace task9-resource >/dev/null || return 1
  wait_cgroup_root_empty
}
case_run bounded-workspace-exhaustion 'many exact 4 KiB files exhaust only the bounded workspace and leave host root free bytes unchanged' bounded_disk_case || true

run_observed_lifecycle() {
  evidence_name=$1
  command=$2
  timeout_value=$3
  expected=$4
  wait_cgroup_root_empty || return 1
  output=$(new_artifact "$evidence_name-api.json")
  payload=$(jq -nc \
    --arg run_id "$evidence_name" \
    --arg command "$command" \
    --arg timeout "$timeout_value" \
    '{namespace:"task9-resource",run_id:$run_id,cwd:"/workspace",command:$command,timeout:$timeout}')
  runtime_post /v1/bash "$payload" >"$output" 2>/dev/null &
  api_pid=$!
  if ! track_pid "$api_pid"; then
    kill -TERM "$api_pid" 2>/dev/null || true
    wait "$api_pid" 2>/dev/null || true
    return 1
  fi
  if ! capture_active_command_cgroup "$evidence_name"; then
    if pid_is_tracked_process "$api_pid"; then
      kill -TERM "$api_pid" 2>/dev/null || true
    fi
    wait "$api_pid" 2>/dev/null || true
    untrack_pid "$api_pid"
    wait_cgroup_root_empty || true
    return 1
  fi
  if ! start_cgroup_cleanup_observer; then
    if pid_is_tracked_process "$api_pid"; then
      kill -TERM "$api_pid" 2>/dev/null || true
    fi
    wait "$api_pid" 2>/dev/null || true
    untrack_pid "$api_pid"
    wait_cgroup_root_empty || true
    return 1
  fi

  request_ok=1
  if [ "$expected" = api-cancel ]; then
    pid_is_tracked_process "$api_pid" || request_ok=0
    kill -TERM "$api_pid" 2>/dev/null || request_ok=0
    wait "$api_pid" 2>/dev/null || true
  elif ! wait "$api_pid"; then
    request_ok=0
  fi
  untrack_pid "$api_pid"

  case "$expected" in
    success)
      jq -e '.http_status == 200 and .body.exit_code == 0' "$output" >/dev/null || request_ok=0
      ;;
    timeout)
      jq -e '.http_status == 400 and .body.error == "command timed out"' "$output" >/dev/null || request_ok=0
      ;;
    api-cancel) ;;
    *) request_ok=0 ;;
  esac
  capture_runtime_lifecycle_markers "$evidence_name" "$observed_command_cgroup" || request_ok=0
  finish_cgroup_cleanup_observer || request_ok=0
  grep -F -x 'runtime_drain_marker_count=1' "$observed_lifecycle_evidence" >/dev/null || request_ok=0
  grep -F -x 'runtime_cleanup_marker_count=1' "$observed_lifecycle_evidence" >/dev/null || request_ok=0
  grep -F -x 'runtime_marker_order=drain-before-cleanup' "$observed_lifecycle_evidence" >/dev/null || request_ok=0
  grep -F -x 'directory_removed=1' "$observed_lifecycle_evidence" >/dev/null || request_ok=0
  [ "$request_ok" -eq 1 ]
}

command_control_packet_probe() {
  cat <<'COMMAND'
python3 - <<'PY'
import array
import json
import os
import socket
import struct

control = socket.socket(fileno=os.dup(int(os.environ["AGENT_FETCH_CONTROL_FD"])))
control.settimeout(2)

def one_fd_packet(payload):
    client, runtime = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
    control.sendmsg([payload], [(socket.SOL_SOCKET, socket.SCM_RIGHTS, array.array("i", [runtime.fileno()]))])
    runtime.close()
    return client

def read_frame(stream):
    header = b""
    while len(header) < 5:
        chunk = stream.recv(5 - len(header))
        if not chunk:
            return None, None
        header += chunk
    size = struct.unpack("!I", header[1:])[0]
    payload = b""
    while len(payload) < size:
        payload += stream.recv(size - len(payload))
    return header[0], payload

# malformed JSON
one_fd_packet(b"{").close()
# receiver-side MSG_TRUNC at the exact 32 KiB metadata allocation
one_fd_packet(b"x" * (32 * 1024 + 1)).close()
# more than one descriptor
left_a, right_a = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
left_b, right_b = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
control.sendmsg([b"{}"], [(socket.SOL_SOCKET, socket.SCM_RIGHTS, array.array("i", [right_a.fileno(), right_b.fileno()]))])
for stream in (left_a, right_a, left_b, right_b):
    stream.close()
# missing descriptor
control.send(b"{}")
# syntactically truncated metadata carrying one descriptor
one_fd_packet(b"{\"protocol_version\":").close()

metadata = json.dumps({
    "protocol_version": 1,
    "request": {
        "protocol_version": 1,
        "method": "GET",
        "url": "http://11.0.0.10:8080/items",
        "headers": [],
        "follow": False,
        "check_status": False,
        "timeout_ms": 5000,
        "declared_body_bytes": 0,
    },
}, separators=(",", ":")).encode()
active = [one_fd_packet(metadata), one_fd_packet(metadata)]
for stream in active:
    kind, payload = read_frame(stream)
    assert kind == 0xA1 and payload == b""
third = one_fd_packet(metadata)
third.settimeout(1)
assert third.recv(1) == b""
third.close()

# 5 invalid + 3 valid + 12 malformed packets reaches the exact bound of 20.
for _ in range(12):
    one_fd_packet(b"not-json").close()
assert control.recv(1) == b""
for stream in active:
    stream.close()
control.close()
print("packets=20 active=2 third=zero-broker")
PY
COMMAND
}

command_control_packet_session_bounds() {
  fixture_reset >/dev/null || return 1
  command=$(command_control_packet_probe) || return 1
  if ! run_observed_lifecycle command-control-bounds "$command" 15s success; then
    return 1
  fi
  state=$(fixture_state) || return 1
  printf '%s' "$state" | jq -e '.requests_total == 0' >/dev/null || return 1
  namespace_hash=$(printf %s task9-resource | sha256sum | awk '{print $1}')
  run_hash=$(printf %s command-control-bounds | sha256sum | awk '{print $1}')
  wait_for_audit_completions command-control-bounds "$namespace_hash" "$run_hash" 2 canceled || return 1
  jq -s -e --arg namespace "$namespace_hash" --arg run "$run_hash" '
    [.[] | select(.namespace_sha256 == $namespace and .run_id_sha256 == $run)] as $records |
    ([$records[] | select(.event == "start")] | length) == 2 and
    ([$records[] | select(.event == "completion")] | length) == 2 and
    all($records[] | select(.event == "completion"); .cancellation_reason != null)
  ' "$AGENT_FETCH_AUDIT_HOST_ROOT/audit.jsonl" >/dev/null || return 1
  grep -F 'directory_removed=1' "$observed_lifecycle_evidence" >/dev/null || return 1
  wait_cgroup_root_empty
}
case_run command-control-packet-session-bounds 'malformed/truncated/oversize/multi-FD packets close at 20, only two sessions start, third creates zero Broker work, Runtime drain precedes cleanup in one logger stream, and Broker audit completes independently' command_control_packet_session_bounds || true

workspace_logical_size() {
  root=$1
  total=0
  while IFS= read -r -d '' path; do
    size=$(stat -Lc %s -- "$path") || return 1
    total=$((total + size))
  done < <(find "$root" -type f -print0)
  printf '%s\n' "$total"
}

fetch_output_shared_quota() {
  reset_namespace task9-quota >/dev/null || return 1
  fixture_reset >/dev/null || return 1
  for target in fetch-quota.bin write-quota.bin; do
    payload=$(jq -nc --arg path "/workspace/$target" --arg content "old-$target" \
      '{namespace:"task9-quota",run_id:"quota-setup",cwd:"/workspace",path:$path,content:$content}')
    response=$(runtime_post /v1/write "$payload") || return 1
    printf '%s' "$response" | jq -e '.http_status == 200 and .body.ok' >/dev/null || return 1
  done
  before_prefill=$(workspace_logical_size "$AGENT_RUNTIME_WORKSPACE_HOST_ROOT") || return 1
  free_for_overlap=1572864
  [ "$AGENT_RUNTIME_WORKSPACE_MAX_BYTES" -gt "$((before_prefill + free_for_overlap))" ] || return 1
  prefill=$((AGENT_RUNTIME_WORKSPACE_MAX_BYTES - before_prefill - free_for_overlap))
  assert_bash_success "truncate -s $prefill /workspace/quota-prefill.bin" task9-quota quota-prefill 5s || return 1

  fetch_output=$(new_artifact quota-fetch-api.json)
  fetch_payload=$(jq -nc '{namespace:"task9-quota",run_id:"quota-fetch",cwd:"/workspace",command:"fetch --output /workspace/fetch-quota.bin GET http://11.0.0.10:8080/bytes/1048576",timeout:"10s"}')
  runtime_post /v1/bash "$fetch_payload" >"$fetch_output" 2>/dev/null &
  fetch_api_pid=$!
  track_pid "$fetch_api_pid" || return 1
  attempts=100
  while [ "$attempts" -gt 0 ]; do
    started=$(fixture_state | jq -r '.route_counts["/bytes/:n"] // 0') || return 1
    [ "$started" -gt 0 ] && break
    pid_is_tracked_process "$fetch_api_pid" || break
    attempts=$((attempts - 1))
    sleep 0.02
  done
  [ "$attempts" -gt 0 ] || return 1
  write_payload=$("$python_for_syntax" -c '
import json
print(json.dumps({
    "namespace": "task9-quota",
    "run_id": "quota-write",
    "cwd": "/workspace",
    "path": "/workspace/write-quota.bin",
    "content": "w" * 1048576,
}, separators=(",", ":")))
') || return 1
  write_response=$(runtime_post /v1/write "$write_payload" 2>/dev/null || true)
  if ! wait "$fetch_api_pid"; then
    untrack_pid "$fetch_api_pid"
    return 1
  fi
  untrack_pid "$fetch_api_pid"
  fetch_success=0
  write_success=0
  jq -e '.http_status == 200 and .body.exit_code == 0' "$fetch_output" >/dev/null 2>&1 && fetch_success=1
  printf '%s' "$write_response" | jq -e '.http_status == 200 and .body.ok' >/dev/null 2>&1 && write_success=1
  [ "$((fetch_success + write_success))" -eq 1 ] || return 1
  if [ "$fetch_success" -eq 0 ]; then
    jq -e '.http_status == 200 and .body.exit_code == 65' "$fetch_output" >/dev/null || return 1
    read_payload=$(jq -nc '{namespace:"task9-quota",run_id:"quota-check",cwd:"/workspace",path:"/workspace/fetch-quota.bin"}')
    old_response=$(runtime_post /v1/read "$read_payload") || return 1
    printf '%s' "$old_response" | jq -e '.http_status == 200 and .body.content == "old-fetch-quota.bin"' >/dev/null || return 1
  fi
  if [ "$write_success" -eq 0 ]; then
    read_payload=$(jq -nc '{namespace:"task9-quota",run_id:"quota-check",cwd:"/workspace",path:"/workspace/write-quota.bin"}')
    old_response=$(runtime_post /v1/read "$read_payload") || return 1
    printf '%s' "$old_response" | jq -e '.http_status == 200 and .body.content == "old-write-quota.bin"' >/dev/null || return 1
  fi
  final_size=$(workspace_logical_size "$AGENT_RUNTIME_WORKSPACE_HOST_ROOT") || return 1
  [ "$final_size" -le "$AGENT_RUNTIME_WORKSPACE_MAX_BYTES" ] || return 1
  [ -z "$(find "$AGENT_RUNTIME_WORKSPACE_HOST_ROOT" -type f -name '.fetch-quota.bin.agent-runtime-????????????????.tmp' -print -quit)" ] || return 1

  reset_namespace task9-quota >/dev/null || return 1
  response=$(bash_response 'fetch --output /workspace/noncwd.bin GET http://11.0.0.10:8080/bytes/4096; test "$(stat -c %s /workspace/noncwd.bin)" -eq 4096; test -z "$(find /workspace -type f -name '\''.noncwd.bin.agent-runtime-????????????????.tmp'\'' -print -quit)"' \
    task9-quota quota-nonworkspace 10s /tmp) || return 1
  printf '%s' "$response" | jq -e '.http_status == 200 and .body.exit_code == 0' >/dev/null || return 1
  reset_namespace task9-quota >/dev/null || return 1
  wait_cgroup_root_empty
}
case_run fetch-output-shared-quota 'overlapped Runtime write and /bytes output share one logical ceiling, at most one commits, old loser survives, temps vanish, and nonworkspace cwd remains bounded' fetch_output_shared_quota || true

nonreading_peer_command() {
  cat <<'COMMAND'
dd if=/dev/zero of=/workspace/nonreader-body.bin bs=1M count=2 status=none
python3 - <<'PY'
import array
import os
import socket
import subprocess
import time

baseline_fds = len(os.listdir("/proc/self/fd"))
fetch_control, malicious_control = socket.socketpair(socket.AF_UNIX, socket.SOCK_SEQPACKET)
environment = dict(os.environ)
environment["AGENT_FETCH_CONTROL_FD"] = "4"

def install_control():
    os.dup2(fetch_control.fileno(), 4)

started = time.monotonic()
process = subprocess.Popen(
    [
        "fetch", "--timeout", "50ms", "POST",
        "http://11.0.0.10:8080/raw", "--raw", "@/workspace/nonreader-body.bin",
    ],
    stdin=subprocess.DEVNULL,
    stdout=subprocess.PIPE,
    stderr=subprocess.PIPE,
    env=environment,
    close_fds=True,
    pass_fds=(fetch_control.fileno(),),
    preexec_fn=install_control,
)
message, ancillary, flags, _ = malicious_control.recvmsg(32768, socket.CMSG_SPACE(array.array("i").itemsize))
assert message and flags == 0
descriptors = array.array("i")
for level, kind, payload in ancillary:
    if level == socket.SOL_SOCKET and kind == socket.SCM_RIGHTS:
        descriptors.frombytes(payload[: len(payload) - (len(payload) % descriptors.itemsize)])
assert len(descriptors) == 1
session = socket.socket(fileno=descriptors[0])
session.setsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF, 4096)
session.sendall(b"\xa1\x00\x00\x00\x00")
stdout, stderr = process.communicate(timeout=0.5)
elapsed = time.monotonic() - started
assert process.returncode == 28, (process.returncode, stdout, stderr)
assert elapsed <= 0.2, elapsed
process.stdout.close()
process.stderr.close()
session.close()
fetch_control.close()
malicious_control.close()
assert len(os.listdir("/proc/self/fd")) == baseline_fds
os.unlink("/workspace/nonreader-body.bin")
print(f"bounded-cancel-ms={elapsed * 1000:.3f}")
PY
COMMAND
}

nonreading_peer_bounded_cancel() {
  fixture_reset >/dev/null || return 1
  run_legacy_test_exact c3-image-drop-before-cancel fetch_cli linux_cli::timeout_drops_inflight_future_before_cancel 1 || return 1
  run_legacy_test_exact c3-image-nonreader-deadline fetch_cli linux_cli::cancel_is_bounded_when_peer_never_reads 1 || return 1
  run_legacy_test_exact c3-image-broken-pipe-diagnostic fetch_cli linux_cli::broken_pipe_does_not_emit_a_second_diagnostic 1 || return 1
  command=$(nonreading_peer_command) || return 1
  run_observed_lifecycle nonreading-peer "$command" 5s success || return 1
  state=$(fixture_state) || return 1
  printf '%s' "$state" | jq -e '.requests_total == 0' >/dev/null || return 1
  grep -F 'directory_removed=1' "$observed_lifecycle_evidence" >/dev/null || return 1
  wait_cgroup_root_empty
}
case_run nonreading-peer-bounded-cancel 'malicious local peer fills the session buffer; real fetch returns within 200ms and built C3 proves inflight-drop-before-cancel, broken-pipe, and FD/task cleanup' nonreading_peer_bounded_cancel || true

cleanup_lifecycle_case() {
  baseline=$(docker top agent-runtime -eo pid,ppid,args | sha256sum | awk '{print $1}') || return 1
  run_observed_lifecycle normal-cleanup 'sleep 2; true' 5s success || return 1
  run_observed_lifecycle timeout-cleanup 'sleep 10' 3s timeout || return 1
  for syscall in setsid setpgid unshare setns; do
    run_observed_lifecycle "$syscall-cleanup" \
      "sleep 2; test \"\$(agent-runtime-net-probe syscall $syscall)\" = errno=1" \
      5s success || return 1
  done
  run_observed_lifecycle api-cancel 'sleep 30' 30s api-cancel || return 1
  sleep 1
  after=$(docker top agent-runtime -eo pid,ppid,args | sha256sum | awk '{print $1}') || return 1
  [ "$baseline" = "$after" ]
}
case_run command-cleanup-lifecycle 'each lifecycle captures populated=1/PID identities, then observes populated=0 or kernel-empty removal, directory removal, no tracked PID, and baseline Runtime PIDs' cleanup_lifecycle_case || true

graceful_shutdown_observer() {
  runtime_exec python3 - <<'PY'
import json
import os
import threading
import time
import urllib.error
import urllib.request

base = "http://127.0.0.1:8080"
headers = {
    "Authorization": "Bearer " + os.environ["AGENT_RUNTIME_TOKEN"],
    "Content-Type": "application/json",
}

def request(path, payload=None, timeout=1):
    raw = None if payload is None else json.dumps(payload, separators=(",", ":")).encode()
    req = urllib.request.Request(
        base + path,
        data=raw,
        method="GET" if raw is None else "POST",
        headers=headers,
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as response:
            return response.status, json.loads(response.read())
    except urllib.error.HTTPError as error:
        return error.code, json.loads(error.read())

long_result = {}

def run_long_request():
    try:
        status, body = request(
            "/v1/bash",
            {
                "namespace": "task9-resource",
                "run_id": "deployed-graceful-sigterm",
                "cwd": "/workspace",
                "command": "fetch GET http://11.0.0.10:8080/endless >/dev/null",
                "timeout": "30s",
            },
            timeout=35,
        )
        long_result.update(status=status, body=body)
    except Exception as error:
        long_result.update(transport_error=type(error).__name__)
    finally:
        long_result["completed_ns"] = time.monotonic_ns()

active = threading.Thread(target=run_long_request, daemon=True)
active.start()
deadline = time.monotonic() + 25
latched_ns = None
active_at_latch = False
rejected_status = None
rejected_body = None
while time.monotonic() < deadline:
    try:
        status_code, status = request("/v1/status", timeout=0.25)
    except Exception:
        time.sleep(0.005)
        continue
    if status_code == 200 and not status.get("bash_ready", True):
        latched_ns = time.monotonic_ns()
        active_at_latch = active.is_alive()
        rejected_status, rejected_body = request(
            "/v1/bash",
            {
                "namespace": "task9-resource",
                "run_id": "deployed-graceful-rejected",
                "cwd": "/workspace",
                "command": "printf must-not-start",
                "timeout": "5s",
            },
            timeout=1,
        )
        break
    time.sleep(0.005)

active.join(10)
assert latched_ns is not None, "shutdown health never latched"
assert active_at_latch, "active request terminated before health latched"
assert rejected_status == 503, (rejected_status, rejected_body)
assert "runtime is shutting down" in rejected_body.get("error", ""), rejected_body
assert not active.is_alive(), "active request did not terminate"
assert long_result.get("completed_ns", 0) > latched_ns, long_result
print("shutdown_health_latched=1", flush=True)
print("shutdown_acceptance_closed=1", flush=True)
print("request_terminated=1", flush=True)
PY
}

deployed_graceful_sigterm_lifecycle() {
  wait_cgroup_root_empty || return 1
  resolve_runtime_supervisor_host_pid || return 1
  graceful_aggregate=$(realpath -e -- "/sys/fs/cgroup/$AGENT_RUNTIME_CGROUP_PARENT") || return 1
  [ "$(dirname -- "$service_cgroup")" = "$graceful_aggregate" ] || return 1

  runtime_container_id=$(dc ps -q agent-runtime) || return 1
  runtime_container_id=$(docker inspect -f '{{.Id}}' "$runtime_container_id") || return 1
  [[ "$runtime_container_id" =~ ^[0-9a-f]{64}$ ]] || return 1
  docker inspect "$runtime_container_id" | jq -e --arg project "$PROJECT_NAME" '
    .[0].Config.Labels["com.docker.compose.project"] == $project and
    .[0].Config.Labels["com.docker.compose.service"] == "agent-runtime" and
    .[0].State.Running
  ' >/dev/null || return 1
  runtime_process_baseline=$(docker top agent-runtime -eo pid,ppid,args | sha256sum | awk '{print $1}') || return 1
  old_runtime_pid=$runtime_supervisor_host_pid
  old_runtime_start=$(awk '{print $22}' "/proc/$old_runtime_pid/stat") || return 1

  broker_container_id=$(dc ps -q agent-fetch-broker) || return 1
  broker_container_id=$(docker inspect -f '{{.Id}}' "$broker_container_id") || return 1
  broker_host_pid=$(docker inspect -f '{{.State.Pid}}' "$broker_container_id") || return 1
  [[ "$broker_host_pid" =~ ^[1-9][0-9]*$ ]] || return 1
  broker_start=$(awk '{print $22}' "/proc/$broker_host_pid/stat") || return 1
  broker_exe=$(stat -Lc '%d:%i' "/proc/$broker_host_pid/exe") || return 1

  fixture_reset >/dev/null || return 1
  disconnects_before=$(fixture_state | jq -r '.disconnects') || return 1
  graceful_evidence=$(new_artifact graceful-sigterm-evidence.txt) || return 1
  graceful_observer_log=$(new_artifact graceful-sigterm-observer.log) || return 1
  graceful_shutdown_observer >"$graceful_evidence" 2>"$graceful_observer_log" &
  graceful_observer_pid=$!
  track_pid "$graceful_observer_pid" || return 1

  result=0
  cgroup_observer_started=0
  if capture_active_command_cgroup graceful-sigterm; then
    if start_cgroup_cleanup_observer; then
      cgroup_observer_started=1
    else
      result=1
    fi
  else
    result=1
  fi

  attempts=100
  while [ "$attempts" -gt 0 ]; do
    graceful_started=$(fixture_state | jq -r '.route_counts["/endless"] // 0' 2>/dev/null || printf 0)
    [ "$graceful_started" -gt 0 ] && break
    attempts=$((attempts - 1))
    sleep 0.05
  done
  [ "$attempts" -gt 0 ] || result=1

  stop_log=$(new_artifact graceful-sigterm-docker-stop.log) || result=1
  stop_started=$SECONDS
  docker stop --signal SIGTERM --time 15 "$runtime_container_id" >"$stop_log" 2>&1 &
  graceful_stop_pid=$!
  if ! track_pid "$graceful_stop_pid"; then
    result=1
  fi
  if ! wait "$graceful_stop_pid"; then
    result=1
  fi
  untrack_pid "$graceful_stop_pid"
  stop_elapsed=$((SECONDS - stop_started))
  [ "$stop_elapsed" -le 15 ] || result=1

  if ! wait "$graceful_observer_pid"; then
    result=1
  fi
  untrack_pid "$graceful_observer_pid"
  if [ "$cgroup_observer_started" -eq 1 ]; then
    finish_cgroup_cleanup_observer || result=1
  fi

  [ "$(docker inspect -f '{{.State.Running}}:{{.State.ExitCode}}' "$runtime_container_id" 2>/dev/null || true)" = false:0 ] || result=1
  current_old_start=$(awk '{print $22}' "/proc/$old_runtime_pid/stat" 2>/dev/null || true)
  [ "$current_old_start" != "$old_runtime_start" ] || result=1
  grep -F -x 'shutdown_health_latched=1' "$graceful_evidence" >/dev/null || result=1
  grep -F -x 'shutdown_acceptance_closed=1' "$graceful_evidence" >/dev/null || result=1
  grep -F -x 'request_terminated=1' "$graceful_evidence" >/dev/null || result=1
  capture_runtime_lifecycle_markers graceful-sigterm "$observed_command_cgroup" "$runtime_container_id" || result=1
  if [ "$cgroup_observer_started" -eq 1 ]; then
    grep -F 'directory_removed=1' "$observed_lifecycle_evidence" >/dev/null || result=1
  fi
  graceful_namespace_hash=$(printf %s task9-resource | sha256sum | awk '{print $1}')
  graceful_run_hash=$(printf %s deployed-graceful-sigterm | sha256sum | awk '{print $1}')
  wait_for_audit_completions graceful-sigterm "$graceful_namespace_hash" "$graceful_run_hash" 1 canceled || result=1
  jq -s -e --arg namespace "$graceful_namespace_hash" --arg run "$graceful_run_hash" '
    [.[] | select(.event == "completion" and .namespace_sha256 == $namespace and .run_id_sha256 == $run)] as $records |
    ($records | length) == 1 and
    ($records[0].cancellation_reason == "client_cancel" or
     $records[0].cancellation_reason == "client_disconnect" or
     $records[0].cancellation_reason == "broken_pipe")
  ' "$AGENT_FETCH_AUDIT_HOST_ROOT/audit.jsonl" >/dev/null || result=1
  disconnects_after=$(fixture_state | jq -r '.disconnects' 2>/dev/null || printf 0)
  [ "$disconnects_after" -gt "$disconnects_before" ] || result=1

  broker_current_start=$(awk '{print $22}' "/proc/$broker_host_pid/stat" 2>/dev/null || true)
  broker_current_exe=$(stat -Lc '%d:%i' "/proc/$broker_host_pid/exe" 2>/dev/null || true)
  [ "$(docker inspect -f '{{.Id}}:{{.State.Running}}' "$broker_container_id" 2>/dev/null || true)" = "$broker_container_id:true" ] || result=1
  [ "$broker_current_start" = "$broker_start" ] && [ "$broker_current_exe" = "$broker_exe" ] || result=1

  printf 'runtime_process_baseline=%s\n' "$runtime_process_baseline" >>"$graceful_evidence"
  printf 'runtime_exited=1\n' >>"$graceful_evidence"
  printf 'binding_session_drained=1\n' >>"$graceful_evidence"
  printf 'command_cgroup_removed=1\n' >>"$graceful_evidence"
  printf 'captured_pid_identities_gone=1\n' >>"$graceful_evidence"
  printf 'broker_identity_unchanged=1\n' >>"$graceful_evidence"

  if ! dc up -d --no-deps --force-recreate agent-runtime >"$(new_artifact graceful-sigterm-recreate.log)" 2>&1; then
    result=1
  fi
  wait_container_health agent-runtime healthy 40 || result=1
  if resolve_runtime_supervisor_host_pid; then
    new_runtime_start=$(awk '{print $22}' "/proc/$runtime_supervisor_host_pid/stat" 2>/dev/null || true)
    new_runtime_container_id=$(dc ps -q agent-runtime 2>/dev/null || true)
    new_runtime_container_id=$(docker inspect -f '{{.Id}}' "$new_runtime_container_id" 2>/dev/null || true)
    [ "$runtime_supervisor_host_pid:$new_runtime_start" != "$old_runtime_pid:$old_runtime_start" ] || result=1
    [ "$new_runtime_container_id" != "$runtime_container_id" ] || result=1
    [ "$(dirname -- "$service_cgroup")" = "$graceful_aggregate" ] || result=1
  else
    result=1
  fi
  [ "$(docker inspect -f '{{.Id}}:{{.State.Running}}' "$broker_container_id" 2>/dev/null || true)" = "$broker_container_id:true" ] || result=1
  printf 'runtime_recreated_healthy=1\n' >>"$graceful_evidence"

  for receipt in \
    shutdown_health_latched shutdown_acceptance_closed request_terminated \
    binding_session_drained command_cgroup_removed captured_pid_identities_gone \
    runtime_exited runtime_recreated_healthy broker_identity_unchanged; do
    grep -F -x "$receipt=1" "$graceful_evidence" >/dev/null || result=1
  done
  wait_cgroup_root_empty || result=1
  [ "$result" -eq 0 ]
}
case_run deployed-graceful-sigterm-lifecycle 'real Runtime SIGTERM latches health and admission before cancel, drains the active Fetch binding/session and exact command cgroup/PIDs, exits boundedly without touching Broker, then recreates healthy' deployed_graceful_sigterm_lifecycle || true

broker_disconnect_cleanup_case() {
  wait_cgroup_root_empty || return 1
  baseline=$(docker top agent-runtime -eo pid,ppid,args | sha256sum | awk '{print $1}') || return 1
  fixture_reset >/dev/null || return 1
  before=$(fixture_state | jq -r '.disconnects') || return 1
  output=$(new_artifact broker-disconnect-api.json)
  payload=$(jq -nc '{namespace:"task9-resource",run_id:"broker-disconnect",cwd:"/workspace",command:"fetch GET http://11.0.0.10:8080/endless >/dev/null",timeout:"30s"}')
  runtime_post /v1/bash "$payload" >"$output" 2>/dev/null &
  api_pid=$!
  track_pid "$api_pid" || return 1
  if ! capture_active_command_cgroup broker-disconnect; then
    if pid_is_tracked_process "$api_pid"; then
      kill -TERM "$api_pid" 2>/dev/null || true
    fi
    wait "$api_pid" 2>/dev/null || true
    untrack_pid "$api_pid"
    wait_cgroup_root_empty || true
    return 1
  fi
  attempts=50
  while [ "$attempts" -gt 0 ]; do
    started=$(fixture_state | jq -r '.route_counts["/endless"] // 0') || { attempts=0; break; }
    [ "$started" -gt 0 ] && break
    attempts=$((attempts - 1))
    sleep 0.1
  done
  if [ "$attempts" -eq 0 ]; then
    if pid_is_tracked_process "$api_pid"; then
      kill -TERM "$api_pid" 2>/dev/null || true
    fi
    wait "$api_pid" 2>/dev/null || true
    untrack_pid "$api_pid"
    wait_cgroup_root_empty || true
    return 1
  fi
  if ! start_cgroup_cleanup_observer; then
    if pid_is_tracked_process "$api_pid"; then
      kill -TERM "$api_pid" 2>/dev/null || true
    fi
    wait "$api_pid" 2>/dev/null || true
    untrack_pid "$api_pid"
    wait_cgroup_root_empty || true
    return 1
  fi

  result=0
  dc stop agent-fetch-broker >/dev/null 2>&1 || result=1
  if ! wait "$api_pid"; then
    result=1
  fi
  untrack_pid "$api_pid"
  capture_runtime_lifecycle_markers broker-disconnect "$observed_command_cgroup" || result=1
  finish_cgroup_cleanup_observer || result=1
  broker_disconnect_namespace_hash=$(printf %s task9-resource | sha256sum | awk '{print $1}')
  broker_disconnect_run_hash=$(printf %s broker-disconnect | sha256sum | awk '{print $1}')
  wait_for_audit_completions broker-disconnect "$broker_disconnect_namespace_hash" "$broker_disconnect_run_hash" 1 canceled || result=1
  jq -s -e --arg namespace "$broker_disconnect_namespace_hash" --arg run "$broker_disconnect_run_hash" '
    [.[] | select(.event == "completion" and .namespace_sha256 == $namespace and .run_id_sha256 == $run)] as $records |
    ($records | length) == 1 and
    ($records[0].cancellation_reason == "client_cancel" or
     $records[0].cancellation_reason == "client_disconnect" or
     $records[0].cancellation_reason == "broken_pipe")
  ' "$AGENT_FETCH_AUDIT_HOST_ROOT/audit.jsonl" >/dev/null || result=1
  attempts=50
  after=$before
  while [ "$attempts" -gt 0 ]; do
    after=$(fixture_state | jq -r '.disconnects') || { result=1; break; }
    [ "$after" -gt "$before" ] && break
    attempts=$((attempts - 1))
    sleep 0.1
  done
  [ "${after:-0}" -gt "$before" ] || result=1
  assert_bash_success 'printf local-tools-survive-broker-stop' task9-resource broker-stop-local 5s || result=1
  dc start agent-fetch-broker >/dev/null 2>&1 || result=1
  wait_container_health agent-runtime healthy 40 || result=1
  sleep 1
  after_runtime=$(docker top agent-runtime -eo pid,ppid,args | sha256sum | awk '{print $1}') || result=1
  [ "$baseline" = "$after_runtime" ] || result=1
  [ "$result" -eq 0 ]
}
case_run broker-disconnect-cleanup 'Broker stop cancels the proxy command, Runtime drain precedes cleanup in one logger stream, Broker audit completes independently, local Bash remains usable, and restart restores baseline Runtime PIDs' broker_disconnect_cleanup_case || true

no_delegation_case() {
  result=0
  export AGENT_RUNTIME_TEST_CGROUP_ROOT=/sys/fs/cgroup/$AGENT_RUNTIME_CGROUP_PARENT/missing-task9
  dc up -d --no-deps --force-recreate agent-runtime >/dev/null 2>&1 || result=1
  wait_container_running agent-runtime 30 || result=1
  wait_container_health agent-runtime unhealthy 40 || result=1
  health=$(runtime_health_json 2>/dev/null || true)
  printf '%s' "$health" | jq -e '.docker_health == "unhealthy" and (.ok | not) and (.bash_ready | not)' >/dev/null || result=1
  before=$(find "$AGENT_RUNTIME_CGROUP_HOST_ROOT" -mindepth 1 -maxdepth 1 -type d | wc -l)
  response=$(bash_response 'printf must-not-run' task9-rollback no-delegation 5s 2>/dev/null || true)
  printf '%s' "$response" | jq -e '.http_status == 503 and (.body.error | contains("cgroup v2 delegation"))' >/dev/null || result=1
  after=$(find "$AGENT_RUNTIME_CGROUP_HOST_ROOT" -mindepth 1 -maxdepth 1 -type d | wc -l)
  [ "$before" = "$after" ] || result=1
  unset AGENT_RUNTIME_TEST_CGROUP_ROOT
  dc up -d --no-deps --force-recreate agent-runtime >/dev/null 2>&1 || result=1
  wait_container_health agent-runtime healthy 40 || result=1
  [ "$result" -eq 0 ]
}
case_run fail-closed-no-delegation 'missing delegated root reports non-ready, Bash 503, starts no child, and restores without unisolated fallback' no_delegation_case || true

broker_absent_case() {
  result=0
  dc stop agent-fetch-broker >/dev/null 2>&1 || result=1
  wait_container_health agent-runtime unhealthy 40 || result=1
  health=$(runtime_health_json 2>/dev/null || true)
  printf '%s' "$health" | jq -e '(.ok | not) and .bash_ready and (.fetch_ready | not)' >/dev/null || result=1
  assert_bash_success '
    tr "\000" "\n" </proc/$$/environ | sort >/tmp/initial-env;
    test "$(cut -d= -f1 /tmp/initial-env | paste -sd, -)" = "AGENT_FETCH_CONTROL_FD,HOME,PATH";
    test ! -e /run/agent-fetch;
    test ! -e /run/secrets/agent-fetch-hmac-key
  ' task9-rollback broker-material-absent 5s || result=1
  assert_bash_success 'printf local-bash-ok' task9-rollback broker-absent 5s || result=1
  write_payload=$(jq -nc '{namespace:"task9-rollback",run_id:"broker-absent",cwd:"/workspace",path:"/workspace/local.txt",content:"local-data\n"}')
  write_response=$(runtime_post /v1/write "$write_payload" 2>/dev/null || true)
  printf '%s' "$write_response" | jq -e '.http_status == 200 and .body.ok' >/dev/null || result=1
  grep_payload=$(jq -nc '{namespace:"task9-rollback",run_id:"broker-absent",cwd:"/workspace",pattern:"local-data",path:"/workspace/local.txt"}')
  grep_response=$(runtime_post /v1/grep "$grep_payload" 2>/dev/null || true)
  printf '%s' "$grep_response" | jq -e '.http_status == 200 and .body.ok and (.body.output | contains("local-data"))' >/dev/null || result=1
  edit_payload=$(jq -nc '{namespace:"task9-rollback",run_id:"broker-absent",cwd:"/workspace",path:"/workspace/local.txt",patch:"@@ -1 +1 @@\n-local-data\n+edited-data\n"}')
  edit_response=$(runtime_post /v1/edit "$edit_payload" 2>/dev/null || true)
  printf '%s' "$edit_response" | jq -e '.http_status == 200 and .body.ok' >/dev/null || result=1
  read_payload=$(jq -nc '{namespace:"task9-rollback",run_id:"broker-absent",cwd:"/workspace",path:"/workspace/local.txt"}')
  read_response=$(runtime_post /v1/read "$read_payload" 2>/dev/null || true)
  printf '%s' "$read_response" | jq -e '.http_status == 200 and .body.content == "edited-data\n"' >/dev/null || result=1
  assert_bash_exit 69 'fetch GET http://11.0.0.10:8080/items' task9-rollback broker-absent-fetch 5s || result=1
  assert_bash_success 'test "$(agent-runtime-net-probe socket inet)" = errno=1; curl -v http://11.0.0.10:8080/items >/dev/null 2>/tmp/curl.err; test "$?" -ne 0; grep -qi "operation not permitted" /tmp/curl.err' task9-rollback no-fallback 5s || result=1
  dc start agent-fetch-broker >/dev/null 2>&1 || result=1
  wait_container_health agent-runtime healthy 40 || result=1
  [ "$result" -eq 0 ]
}
case_run broker-absent-degraded 'Broker/UDS absence keeps local Bash/read/grep/write/edit working, Fetch=69, and direct clients EPERM with no blacklist fallback' broker_absent_case || true

broken_audit_case() {
  fixture_reset >/dev/null || return 1
  result=0
  export AGENT_FETCH_TEST_AUDIT_PATH=/var/log/agent-fetch
  dc up -d --no-deps --force-recreate agent-fetch-broker >/dev/null 2>&1 || true
  attempts=20
  while [ "$attempts" -gt 0 ]; do
    running=$(docker inspect -f '{{.State.Running}}' agent-fetch-broker 2>/dev/null || true)
    [ "$running" = false ] && break
    attempts=$((attempts - 1))
    sleep 0.5
  done
  [ "$(docker inspect -f '{{.State.Running}}' agent-fetch-broker 2>/dev/null || true)" = false ] || result=1
  assert_bash_exit 69 'fetch GET http://11.0.0.10:8080/items' task9-rollback broken-audit 5s || result=1
  state=$(fixture_state 2>/dev/null || true)
  printf '%s' "$state" | jq -e '.requests_total == 0' >/dev/null || result=1
  unset AGENT_FETCH_TEST_AUDIT_PATH
  dc up -d --no-deps --force-recreate agent-fetch-broker >/dev/null 2>&1 || result=1
  wait_container_running agent-fetch-broker 30 || result=1
  wait_container_health agent-runtime healthy 40 || result=1
  [ "$result" -eq 0 ]
}
case_run audit-fail-closed 'broken audit path prevents Broker startup and fixture connection; Fetch returns unavailable' broken_audit_case || true

policy_mismatch_case() {
  fixture_reset >/dev/null || return 1
  result=0
  audit_before=$(awk 'END {print NR + 0}' "$AGENT_FETCH_AUDIT_HOST_ROOT/audit.jsonl") || return 1
  export AGENT_FETCH_TEST_BROKER_POLICY_VERSION="${AGENT_FETCH_POLICY_VERSION}-mismatch"
  dc up -d --no-deps --force-recreate agent-fetch-broker >/dev/null 2>&1 || result=1
  wait_container_running agent-fetch-broker 30 || result=1
  wait_container_health agent-runtime unhealthy 40 || result=1
  policy_pcap=$(new_artifact policy-mismatch-broker.pcap) || return 1
  policy_capture_text=$(new_artifact policy-mismatch-broker-capture.txt) || return 1
  start_capture agent-fetch-broker "$policy_pcap" '(ip or ip6)' || return 1
  assert_bash_exit 69 'fetch GET http://11.0.0.10:8080/items' task9-cross-a policy-mismatch 10s || result=1
  state=$(fixture_state 2>/dev/null || true)
  printf '%s' "$state" | jq -e '
    .requests_total == 0 and .request_bytes == 0 and .responses_total == 0 and
    (.dns_counts | length) == 0 and (.events | length) == 0
  ' >/dev/null || result=1
  audit_after=$(awk 'END {print NR + 0}' "$AGENT_FETCH_AUDIT_HOST_ROOT/audit.jsonl") || result=1
  [ "$audit_before" = "$audit_after" ] || result=1
  stop_backgrounds || result=1
  tcpdump -nn -r "$policy_pcap" >"$policy_capture_text" 2>/dev/null || result=1
  [ ! -s "$policy_capture_text" ] || result=1
  export AGENT_FETCH_TEST_BROKER_POLICY_VERSION="$AGENT_FETCH_POLICY_VERSION"
  dc up -d --no-deps --force-recreate agent-fetch-broker >/dev/null 2>&1 || result=1
  wait_container_health agent-runtime healthy 40 || result=1
  [ "$result" -eq 0 ]
}
case_run protocol-policy-mismatch 'real Broker policy mismatch is unavailable/auth exit 69 before audit/body/DNS/egress; trusted protocol mismatch is independent' policy_mismatch_case || true

run_exact_go_test() {
  package=$1
  test_name=$2
  evidence_name=$3
  evidence=$(new_artifact "$evidence_name.log") || return 1
  timeout 5m go test "$package" -run "^${test_name}\$" -count=1 -v >"$evidence" 2>&1 || return 1
  awk -v test_name="$test_name" '
    $0 == "=== RUN   " test_name { runs++ }
    index($0, "--- PASS: " test_name " (") == 1 { passes++ }
    END { exit(runs == 1 && passes == 1 ? 0 : 1) }
  ' "$evidence"
}

rollback_gate_case() {
  run_exact_go_test ./config TestAgentV3RuntimeFetchDefaultsDisabled rollback-fetch-default-disabled || return 1
  run_exact_go_test ./config TestAgentV3RuntimeFetchRequiresExplicitTrue rollback-fetch-explicit-true || return 1
  run_exact_go_test ./agent TestAgentV3FetchGuidanceIsOmittedWhenDisabled rollback-fetch-guidance-disabled || return 1
  run_exact_go_test ./agent TestBuildAgentV3StablePrefixHashIncludesRuntimeRules rollback-stable-prefix-runtime-rules || return 1
  assert_bash_success 'test "$(agent-runtime-net-probe socket inet)" = errno=1' task9-rollback rollback-seccomp 5s
}
case_run fetch-disabled-rollback 'isolated Go gate omits Fetch and changes Stable Prefix while live Runtime seccomp remains EPERM' rollback_gate_case || true

reset_test_namespaces() {
  for namespace in "${namespaces[@]}"; do
    response=$(reset_namespace "$namespace") || return 1
    printf '%s' "$response" | jq -e '.http_status == 200 and .body.ok' >/dev/null || return 1
  done
}
case_run test-namespace-cleanup 'all exact Task 9 namespaces are removed through the authenticated Runtime reset API' reset_test_namespaces || true

if wait_cgroup_root_empty \
  && [ -z "$(find "$AGENT_RUNTIME_WORKSPACE_HOST_ROOT" -mindepth 1 -maxdepth 1 -type d ! -name .runtime-jails -print -quit)" ]; then
  pass_case final-residual-check 'delegated command root is empty and test namespaces are reset before teardown'
else
  fail_case final-residual-check 'residual command cgroup or workspace namespace remains before teardown'
fi

if [ "$fail_count" -ne 0 ]; then
  exit 1
fi
