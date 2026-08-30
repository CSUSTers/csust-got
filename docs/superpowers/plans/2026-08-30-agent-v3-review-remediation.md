# Agent v3 Review Remediation Implementation Plan

> **For agentic workers:** Use the subagent-driven-development skill to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remediate every confirmed Agent v3 review defect in the approved design while preserving the existing product, protocol, and deployment contracts.

**Architecture:** Harden each existing trust boundary in place: collision-resistant Runtime identities feed a namespace use/reset gate and bounded filesystem workers; exec and Fetch retain their current protocols but linearize descriptor cleanup and the request-head deadline; Redis and JSONL writers gain bounded optimistic/serialized writes. Startup validation, optional override diagnostics, production probe packaging, and bilingual deployment guidance are corrected without adding a new service, schema, or capability.

**Tech Stack:** Go 1.26+ (`go.mod` currently declares `go 1.26.0`), Rust 1.95+ edition 2024 (`agent-runtime/Cargo.toml` currently declares `rust-version = "1.95"`), `github.com/redis/go-redis/v9` v9.22.0, `miniredis` v2.38.0, Tokio 1, Axum 0.8, `cap_std`, Docker Compose v2, PowerShell 7, and native-Linux deployment scripts.

**Spec:** `docs/superpowers/specs/2026-08-30-agent-v3-review-remediation-design.md`

**Global Constraints:**
- Fix the defects confirmed by the full Agent v3 review without changing intentional product contracts or adding unrelated capabilities.
- The following reviewed areas are intentionally excluded because current code, tests, or later contracts establish the behavior: turn-scoped rich-skill activation, volatile memory/tool definitions outside the stable prefix, direct MCP/MCPO egress, generic read-only `/skills` access, logical Bash workspace quotas, ptrace/process-vm isolation, redirect-hop response-header forwarding rules, and Broker process-level graceful drain.
- This retains the existing data model and protocols.
- A generation-based workspace model, a new memory schema, distributed filesystem locks, force-reset, and single-writer services are out of scope.
- Validate non-empty namespace and run identifiers with bounded lengths before any filesystem or command operation.
- Derive workspace directory names from the full namespace using a fixed lowercase SHA-256 key rather than lossy character replacement.
- Derive each jail path from that namespace key plus the Runtime-generated command ID; caller-provided `run_id` remains logical/audit identity and never selects a path.
- Read, grep, write, edit, Bash, and command-bound Fetch output hold a shared use lease for their lifetime.
- Reset obtains a non-blocking exclusive lease; if any operation is active it returns an HTTP conflict without deleting anything.
- New operations wait while the short reset operation owns the namespace.
- The existing singleton Runtime deployment is the supported coordination boundary; cross-process Runtime replicas remain unsupported.
- Changing the storage key intentionally stops automatically opening legacy lossy directories.
- Because the previous mapping was many-to-one, automatic migration could assign collided data to the wrong namespace.
- Deployment notes will require an offline, explicit backup/migration when old workspaces must be retained.
- Read only enough bytes to produce the configured output plus a truncation sentinel.
- Grep uses fixed maximum scanned bytes, directory entries, and elapsed time in addition to the existing output limit.
- Workspace traversal continues to originate from `cap_std::Dir`, opens final files no-follow, and never reconstructs an ambient path for traversal.
- Blocking scan workers receive a cancellation flag checked between entries and bounded read chunks.
- Dropping or timing out the async wrapper signals cancellation.
- A kernel call already blocked on an unsupported remote filesystem cannot be preempted; the production contract remains a bounded local workspace filesystem.
- Capture the inherited hard descriptor limit and close inherited descriptors before applying the command's lower `RLIMIT_NOFILE`.
- Use `close_range` in ranges around retained control/status descriptors, with a bounded per-FD fallback only for `ENOSYS`.
- Any cleanup failure aborts exec initialization.
- Keep the pre-auth admission permit and its original absolute deadline through the first authenticated `ClientFrame::Request`.
- Authentication does not reset the clock.
- A peer that authenticates but stalls before Request is closed at the same bounded deadline without quota, audit, DNS, or connector activity.
- Keep existing Redis keys and add compare-and-set writers based on `WATCH`/`MULTI`.
- Memory mutation rebuilds also clean stale active IDs and refresh all remaining item, active-set, and snapshot TTLs to the same configured lifetime.
- Retries are bounded and return an error rather than committing stale content.
- Marshal outside the write lock, append the newline to the payload, then serialize each process's complete-record writes through a shared sink lock.
- Existing single-process deployment contracts do not require cross-process file locking.
- Go creates trace directories and files as owner-only and tightens existing targets before writing.
- Runtime keeps its existing audit semantics while making JSONL records indivisible within the process.
- Configuration validation rejects a globally enabled Agent v3 setup when an enabled v3 chat cannot use the configured Runtime.
- Request-time checks remain defensive.
- `custom.yaml` stays optional and ignored by Git/Docker, but an existing unreadable or malformed file produces an explicit diagnostic instead of disappearing silently.
- Update English and Chinese quick-start/upgrade documentation to list required Compose inputs and distinguish pulled images from source-built Runtime targets.
- Replace the production-image Python network probe with the already shipped `agent-runtime-net-probe`; test-only and fixture Python usage remains unchanged.
- Final acceptance: the same final code identity is reviewed independently by the configured Oracle and primary Reviewer lanes.
- No rich-message contract change, stable-prefix redesign, MCP routing change, `/skills` capability removal, per-namespace disk quota, force-reset, log rotation service, Broker shutdown protocol, or unrelated cleanup.

---

## Execution and Validation Boundaries

- Do not install or upgrade Go, Rust, Docker, Redis, Python, Bash, or any other software. The executor must use the toolchains already present; Go 1.26+ and Rust 1.95+ are minimums, not requests to alter the repository toolchain.
- All commands below use PowerShell syntax. Quoted test filters are passed literally to Go or Cargo.
- The Windows-local lane may run Go tests, non-Linux Rust tests, formatting, static `rg` assertions, Docker Compose rendering, and builds supported by the existing Docker Desktop setup. It must not claim evidence for `#[cfg(target_os = "linux")]`, `close_range`, PRoot, cgroup v2, seccomp, Unix modes, or the native host scripts.
- The native-Linux lane is mandatory for the high-FD helper test, Runtime sandbox/cgroup suites, owner-mode assertions, Docker image execution, `scripts/validate-agent-runtime-host.sh`, `scripts/test-agent-runtime-compose.sh`, and `scripts/agent-runtime-attack-matrix.sh`. Invoke those three scripts from PowerShell on that host using the exact `/usr/bin/bash` commands in Task 13; do not substitute WSL or a non-privileged container for host cgroup/nftables evidence.
- Security tests are evidence only when the exact new assertion is observed RED before implementation and the same assertion is GREEN afterward. Store command/output in the implementation task notes; do not commit generated logs or receipts.
- Every RED command must compile and reach a precise behavioral assertion. An undefined symbol, missing module, fixture-construction error, panic before the target assertion, timeout waiting for a test barrier, or unrelated compile failure is invalid RED evidence. When an internal future API has no current callable surface, first add the smallest `#[cfg(test)]` seam or test-local legacy adapter that delegates to existing behavior without fixing it; rerun the identical behavior assertion after replacing that adapter with the production interface in GREEN.
- Implementation workers and task subagents must not run `git add`, `git commit`, `git push`, `git tag`, rebase, or any other Git write. The orchestrator owns the commit groups below and performs them only after the group is GREEN; the user has already authorized the final remediation commits.

## Dependency Waves

| Wave | Tasks | Dependency rule |
|---|---|---|
| 1 | 1, 4, 5, 6, 9, 10, 11 | Independent foundations; these may execute in parallel because they do not consume each other's new interfaces. Task 1 is not complete until every BindingContext/C7 fixture and the full `runtime_fetch_proxy` integration target are GREEN. |
| 2 | 2, 7, 8, 12 | Task 2 starts only after Task 1's original/derived identity contract is GREEN and uses fixture-private deterministic barriers, including two phases in one Bash request; Task 7 consumes Task 6's bounded CAS helper; Task 8 consumes Task 6's monotonic `trace:last` and retains forced writer/barrier RED evidence; Task 12 documents Task 1 migration semantics and Task 11 packaging. |
| 3 | 3 | Bounded read/grep rewires all local handlers and therefore consumes both Task 1 identity validation and Task 2 namespace leases. |
| 4 | 13 | Run only after Tasks 1–12 are GREEN and all intended files are present in one working tree. |

## File Map

| File | Responsibility in this remediation |
|---|---|
| `agent-runtime/src/identity.rs` | New bounded `namespace`/`run_id` validation, lowercase SHA-256 storage key, and command-ID jail path construction. |
| `agent-runtime/src/namespace_gate.rs` | New process-local per-namespace shared-use and non-blocking reset leases. |
| `agent-runtime/src/scan.rs` | New bounded text reads, grep byte/entry/time budgets, and cancellation-on-drop wrapper for blocking work. |
| `agent-runtime/src/trace.rs` | New cloneable process-local Runtime JSONL sink that writes one complete record while holding its sink lock. |
| `agent-runtime/src/lib.rs` | Register new modules/state; validate identities; acquire leases; use bounded scans; build unique jails; return reset conflict; delegate Runtime trace appends; host fixture-private `#[cfg(test)]` namespace checkpoint state with no production behavior. |
| `agent-runtime/src/main.rs` | Construct `NamespaceGate` and `JsonlTraceSink` in production `AppState`. |
| `agent-runtime/src/runtime_fetch_proxy/binding.rs` | Derive the namespace storage key when constructing command-bound Fetch context without changing token/audit identity. |
| `agent-runtime/src/runtime_fetch_proxy/registry.rs` | Store the derived namespace key in `BindingContext`. |
| `agent-runtime/src/runtime_fetch_proxy/lifecycle.rs` | Update the detached lifecycle test fixture for the expanded `BindingContext` identity contract. |
| `agent-runtime/src/runtime_fetch_proxy/lifecycle/test_support.rs` | Update reusable lifecycle fixtures to carry original namespace plus its single derived storage key. |
| `agent-runtime/src/runtime_fetch_proxy/lifecycle/tests.rs` | Update direct `BindingContext` fixtures and retain lifecycle/drain assertions. |
| `agent-runtime/src/runtime_fetch_proxy/guardian/tests.rs` | Update guardian fixtures for the expanded binding identity. |
| `agent-runtime/src/runtime_fetch_proxy/c7_test_support/lifecycle/fixture.rs` | Update the C7 binding fixture for original/derived namespace identity. |
| `agent-runtime/src/runtime_fetch_proxy/c7_test_support/output.rs` | Replace plaintext C7 output-directory assertions with the derived storage directory. |
| `agent-runtime/src/runtime_fetch_proxy/session.rs` | Pass the stored namespace key through the response relay. |
| `agent-runtime/src/runtime_fetch_proxy/response.rs` | Construct Fetch output commits with the already-derived namespace key. |
| `agent-runtime/src/runtime_fetch_proxy/output.rs` | Consume the already-derived namespace storage key for no-follow output commits. |
| `agent-runtime/src/runtime_fetch_proxy/output/path.rs` | Remove the duplicate lossy namespace sanitizer. |
| `agent-runtime/tests/runtime_fetch_proxy.rs` | Update plaintext namespace-directory fixtures and add command-bound Fetch identity/single-hash output coverage. |
| `agent-runtime/src/exec/helper.rs` | Capture hard nofile, clean inherited descriptors, then lower rlimits. |
| `agent-runtime/src/exec/helper/test_support.rs` | Preserve exec-stage fault tests under the new hard-limit/cleanup method signatures. |
| `agent-runtime/src/sandbox.rs` | Close ranges around retained descriptors and use per-FD fallback only after `ENOSYS`. |
| `agent-runtime/tests/linux_exec_helper.rs` | Prove a non-CLOEXEC FD above 256 is closed and control/status descriptors survive initialization. |
| `agent-runtime/src/fetch_broker/session.rs` | Carry `PreAuthAdmission` and its absolute deadline through the first authenticated Request frame. |
| `agent-runtime/tests/fetch_broker.rs` | Prove authenticated-idle closure, original-clock behavior, permit lifetime, and absence of side effects. |
| `orm/agentv3.go` | Add bounded `WATCH`/`MULTI` retry helpers, summary/memory rebuild APIs, monotonic `trace:last`, stale-ID cleanup, and unified TTL refresh. |
| `orm/agentv3_test.go` | Deterministic conflict, monotonic trace, stale-ID, equal-TTL, and retry-exhaustion tests using `miniredis`. |
| `chatv2/agentv3_context.go` | Render summary and memory content inside the new retryable ORM rebuild callbacks. |
| `chatv2/agentv3_trace.go` | Serialize complete Go JSONL records under one shared sink lock and write owner-only targets. |
| `chatv2/agentv3_trace_test.go` | New concurrent JSONL parsing and Unix mode regression tests. |
| `chatv2/chatv2.go` | Reject invalid globally enabled Agent v3 Runtime setup before compiling chats. |
| `chatv2/chatv2_test.go` | New startup preflight matrix tests. |
| `main.go` | Treat `chatv2.Init` failure as fatal rather than logging and continuing. |
| `config/config.go` | Distinguish absent optional `custom.yaml` from existing unreadable/malformed overrides and log a diagnostic. |
| `config/config_test.go` | Assert malformed/unreadable existing override diagnostics and retain valid merge/absent-file behavior. |
| `agent-runtime/Dockerfile` | Ship `agent-runtime-net-probe` in the production Runtime image as well as the security-test image. |
| `scripts/agent-runtime-attack-matrix.sh` | Replace only the production-default-off Python socket probe with `agent-runtime-net-probe`. |
| `scripts/test-agent-runtime-compose.sh` | Add static checks that lock production probe packaging/use and retain test-only Python allowances. |
| `README.md` | Correct English Compose inputs, source/image upgrade paths, and offline storage-key migration warning. |
| `README_zh-CN.md` | Mirror the corrected deployment and migration contract in Chinese. |

### Task 1: Runtime Identity, Workspace Key, and Unique Jail Paths

**Files:**
- Create: `agent-runtime/src/identity.rs`
- Modify: `agent-runtime/src/lib.rs: CommonRequest, request handlers, namespace_workspace, shell_exec_target, sandboxed_shell_command, identity tests`
- Modify: `agent-runtime/src/runtime_fetch_proxy/binding.rs: BindingContext construction`
- Modify: `agent-runtime/src/runtime_fetch_proxy/registry.rs: BindingContext namespace field`
- Modify: `agent-runtime/src/runtime_fetch_proxy/lifecycle.rs: CommandLifecycleLease::with_test_tasks BindingContext fixture`
- Modify: `agent-runtime/src/runtime_fetch_proxy/lifecycle/test_support.rs: RuntimeFetchProxy::with_test_binding_for_tests fixture`
- Modify: `agent-runtime/src/runtime_fetch_proxy/lifecycle/tests.rs: test_proxy_lease fixture`
- Modify: `agent-runtime/src/runtime_fetch_proxy/guardian/tests.rs: guardian BindingContext fixtures`
- Modify: `agent-runtime/src/runtime_fetch_proxy/c7_test_support/lifecycle/fixture.rs: C7 BindingContext fixture`
- Modify: `agent-runtime/src/runtime_fetch_proxy/c7_test_support/output.rs: C7 output directory fixtures/assertions`
- Modify: `agent-runtime/src/runtime_fetch_proxy/session.rs: response relay namespace argument`
- Modify: `agent-runtime/src/runtime_fetch_proxy/response.rs: OutputCommitGuard construction`
- Modify: `agent-runtime/src/runtime_fetch_proxy/output.rs: OutputCommitGuard constructors`
- Modify: `agent-runtime/src/runtime_fetch_proxy/output/path.rs: namespace path selection and tests`
- Test: `agent-runtime/src/lib.rs: tests module`
- Test: `agent-runtime/src/runtime_fetch_proxy/output/tests.rs`
- Test: `agent-runtime/tests/runtime_fetch_proxy.rs`

**Interfaces:**
- Consumes: existing `CommonRequest { namespace, run_id, cwd }`, `RuntimeFetchProxy::new_command_id()`, and SHA-256 dependency `sha2`.
- Produces: `MAX_NAMESPACE_BYTES: usize = 256`, `MAX_RUN_ID_BYTES: usize = 128`, `RuntimeIdentity`, `RuntimeIdentity::from_common(&CommonRequest) -> Result<RuntimeIdentity, RuntimeError>`, `RuntimeIdentity::namespace() -> &str`, `RuntimeIdentity::run_id() -> &str`, `RuntimeIdentity::namespace_key() -> &str`, `namespace_storage_key(&str) -> String`, `command_jail_root(&Path, &RuntimeIdentity, &str) -> PathBuf`, `BindingContext { namespace: String, namespace_key: String, .. }`, and `OutputCommitGuard::new_with_health_and_namespace_key` for Tasks 2–3 and command-bound Fetch output.

**Recommended executor:** `complex`

- [ ] **Step 1: Write the failing identity and path tests**

  First add only a compile-safe `#[cfg(test)] fn command_jail_path_under_test(root: &Path, common: &CommonRequest, command_id: &str) -> PathBuf` beside the current jail helper; for RED it delegates to the current `run_id`-selected path and ignores `command_id`. This is a test seam, not the new identity implementation. Add existing-handler tests `runtime_identity_rejects_empty_and_oversize_values_before_path_use` and `path_significant_namespaces_use_distinct_storage_directories`: submit ordinary `/v1/write` requests, assert 400/no workspace side effect for invalid identity and two distinct on-disk namespace directories for `a:b` and `a/b`. They compile against current routes and fail on behavior.

  Add `fn common(namespace: &str, run_id: &str) -> CommonRequest` in the test module; it copies both strings and sets `cwd` to `/workspace`. For the jail assertion, call only the compile-safe seam in RED:

  ```rust
  #[test]
  fn caller_run_id_never_selects_the_jail_path() {
      let first = common("namespace", "caller-a");
      let second = common("namespace", "caller-b");
      assert_eq!(command_jail_path_under_test(Path::new("/runtime/workspaces"), &first, "cmd-1"),
                 command_jail_path_under_test(Path::new("/runtime/workspaces"), &second, "cmd-1"));
      assert_ne!(command_jail_path_under_test(Path::new("/runtime/workspaces"), &first, "cmd-1"),
                 command_jail_path_under_test(Path::new("/runtime/workspaces"), &first, "cmd-2"));
  }
  ```

  In `agent-runtime/tests/runtime_fetch_proxy.rs`, replace every fixture/expectation that assumes `workspace.join("namespace")` or `workspace.join("ns")` with a locally computed `Sha256` digest of the original namespace; never call the not-yet-created `namespace_storage_key` in RED. Add `command_bound_fetch_output_hashes_namespace_once_and_preserves_identity`: bind namespace `tenant/a:b` and run the proxy against a real `FetchBroker::with_components` session using local deterministic resolver/connector fakes patterned after `agent-runtime/tests/fetch_broker.rs` plus a real `JsonlAuditSink` at a temporary path. The broker must verify the actual token, return one response body, and write its normal audit record; no test code may synthesize an audit record from expected values. Parse the emitted flattened `AuditRecord::Start` JSONL and assert (a) successful broker authentication plus top-level `namespace_sha256 == sha256("tenant/a:b")` proves the token-derived audit used the original namespace, (b) that value differs from SHA-256 of the storage key, (c) output exists at `workspace/<sha256("tenant/a:b")>/result.txt`, (d) `workspace/<sha256(sha256("tenant/a:b"))>/result.txt` does not exist, and (e) there is exactly one committed destination and no temporary file. Direct `BindingContext`/C7 fixture field changes wait until Step 3 adds `namespace_key`; they are not part of RED setup.

- [ ] **Step 2: Run the focused tests and retain the RED evidence**

  Run from PowerShell:

  ```powershell
  cargo test --manifest-path "agent-runtime/Cargo.toml" runtime_identity_ -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" path_significant_namespaces_use_distinct_storage_directories -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" caller_run_id_never_selects_the_jail_path -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test runtime_fetch_proxy command_bound_fetch_output_hashes_namespace_once_and_preserves_identity -- --nocapture
  ```

  Expected RED: every target compiles; the current routes accept at least one invalid identity or create a side effect, `a:b`/`a/b` share the lossy directory, caller `run_id` still selects the jail, and the end-to-end Fetch output is not present at the single SHA-256 directory. A missing symbol or unrelated compile error is not valid RED evidence.

- [ ] **Step 3: Implement the exact identity contract and wire every handler before filesystem work**

  Define the focused module contract:

  ```rust
  pub(crate) const MAX_NAMESPACE_BYTES: usize = 256;
  pub(crate) const MAX_RUN_ID_BYTES: usize = 128;

  #[derive(Clone, Debug, PartialEq, Eq)]
  pub(crate) struct RuntimeIdentity {
      namespace: String,
      run_id: String,
      namespace_key: String,
  }

  impl RuntimeIdentity {
      pub(crate) fn from_common(common: &crate::CommonRequest) -> Result<Self, crate::RuntimeError>;
      pub(crate) fn namespace(&self) -> &str;
      pub(crate) fn run_id(&self) -> &str;
      pub(crate) fn namespace_key(&self) -> &str;
  }

  pub(crate) fn namespace_storage_key(namespace: &str) -> String;
  pub(crate) fn command_jail_root(root: &Path, identity: &RuntimeIdentity, command_id: &str) -> PathBuf;
  ```

  Define the command-bound output entry point explicitly:

  ```rust
  pub(crate) fn new_with_health_and_namespace_key(
      workspace_root: &Path,
      namespace_key: &str,
      output_path: &str,
      budget: &WorkspaceBudget,
      phase: Arc<Mutex<CommandBindingPhase>>,
      health: BashHealth,
  ) -> Result<OutputCommitGuard, RuntimeFetchProxyError>;
  ```

  Reject values whose trimmed content is empty or whose UTF-8 byte length exceeds the constants. Preserve the original validated strings for token, cgroup, audit, and trace identity. Hash the complete original namespace with SHA-256 and encode all 32 bytes as 64 lowercase hexadecimal characters. Do not truncate the digest and do not retain `sanitize_namespace` under another name.

  In each request handler, call `RuntimeIdentity::from_common` immediately after authorization and before timing, path resolution, directory creation, command-ID creation, or trace emission. Pass `&RuntimeIdentity` to local operation helpers. Generate the command ID before `shell_exec_target`, and derive the jail with `workspace_root.join(".runtime-jails").join(identity.namespace_key()).join(command_id)`; never pass `run_id` to jail path construction.

  In Fetch binding, keep the original namespace for `issue_for_command`, but store `namespace_key: namespace_storage_key(namespace)` in `BindingContext`. Pass that field through `session::proxy_session` and `response::relay_response`. Preserve `OutputCommitGuard::new` for direct callers by deriving the key once there; add `new_with_health_and_namespace_key(workspace_root, namespace_key, output_path, budget, phase, health)` for the response path so it consumes the stored key directly. Remove `runtime_fetch_proxy::output::path::sanitize_namespace`.

  Update all `BindingContext` constructors in `binding.rs`, `lifecycle.rs`, `lifecycle/test_support.rs`, `lifecycle/tests.rs`, `guardian/tests.rs`, and `c7_test_support/lifecycle/fixture.rs`; there must be no fixture that fills only `namespace`. Update `c7_test_support/output.rs`, `runtime_fetch_proxy/output/tests.rs`, and every plaintext namespace-directory assertion in `agent-runtime/tests/runtime_fetch_proxy.rs` to derive exactly one storage key. After the production API exists, replace the RED jail seam body with `RuntimeIdentity::from_common` plus `command_jail_root`, and add direct unit assertions that both SHA-256 outputs are 64 lowercase hex characters and differ.

- [ ] **Step 4: Run the focused GREEN tests and path regressions**

  ```powershell
  cargo test --manifest-path "agent-runtime/Cargo.toml" runtime_identity_ -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" namespace_storage_key_ -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" caller_run_id_never_selects_the_jail_path -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" path_significant_namespaces_use_distinct_storage_directories -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test runtime_fetch_proxy command_bound_fetch_output_hashes_namespace_once_and_preserves_identity -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test runtime_fetch_proxy -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" write_read_and_grep_are_namespace_isolated -- --nocapture
  ```

  Expected GREEN: invalid identities return 400 without side effects; path-significant namespaces have distinct workspace/output keys; same caller `run_id` with different Runtime command IDs yields different jail paths; all lifecycle/guardian/C7 fixtures compile; the full `runtime_fetch_proxy` integration target passes; token/audit retain original identity while output commits once under exactly one SHA-256 layer.

- [ ] **Step 5: Record commit-group A readiness without Git writes**

  Record Task 1 test commands and GREEN results in the execution notes. Do not commit: Tasks 2 and 3 must join this task in commit group A.

### Task 2: Namespace Use/Reset Gate

**Files:**
- Create: `agent-runtime/src/namespace_gate.rs`
- Modify: `agent-runtime/src/lib.rs: AppState, read/grep/write/edit/bash/reset handlers, RuntimeError, tests`
- Modify: `agent-runtime/src/main.rs: AppState construction`
- Modify: `agent-runtime/tests/linux_cgroup.rs: AppState fixture`
- Test: `agent-runtime/src/namespace_gate.rs: tests module`
- Test: `agent-runtime/src/lib.rs: table-driven endpoint/reset barrier tests`

**Interfaces:**
- Consumes: `RuntimeIdentity::namespace_key()` from Task 1 and the existing singleton `AppState` lifecycle.
- Produces: cloneable `NamespaceGate`, `NamespaceGate::acquire_use(&str) -> Result<NamespaceUseLease, NamespaceGateError>`, `NamespaceGate::try_acquire_reset(&str) -> Result<Option<NamespaceResetLease>, NamespaceGateError>`, shared-use lease coverage for all six operation classes, HTTP 409 through `RuntimeError::conflict`, and fixture-private compile-time-test-only `NamespaceUseTestHooks { slots: Arc<Mutex<HashMap<(NamespaceOperation, NamespaceUseTestPhase), TestHookSlot>>> }` exposed only as `#[cfg(test)] AppState::namespace_use_test_hooks`; Bash checkpoints `LeaseAcquired` and `BashWaitReturned` observe one uninterrupted `NamespaceUseLease`.

**Recommended executor:** `complex`

- [ ] **Step 1: Add a compile-safe barrier seam, then write failing table-driven endpoint tests**

  Before the RED run, add only this `#[cfg(test)]` seam in `lib.rs`; import `std::{collections::HashMap, sync::{Mutex, Weak}}` and `tokio::sync::oneshot` under `cfg(test)`. It must compile while leaving all non-test behavior and the current reset implementation unchanged. Each `AppState` test fixture owns a fresh hook map—there is no static, `OnceLock`, thread-local, or process-global registry:

  ```rust
  #[cfg(test)]
  #[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
  enum NamespaceOperation { Read, Grep, Write, Edit, Bash }

  #[cfg(test)]
  #[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
  enum NamespaceUseTestPhase { LeaseAcquired, BashWaitReturned }

  #[cfg(test)]
  #[derive(Clone, Default)]
  struct NamespaceUseTestHooks {
      slots: Arc<Mutex<HashMap<(NamespaceOperation, NamespaceUseTestPhase), TestHookSlot>>>,
  }

  #[cfg(test)]
  impl NamespaceUseTestHooks {
      fn arm(&self, operation: NamespaceOperation, phase: NamespaceUseTestPhase) -> TestBarrier;
      async fn checkpoint(&self, operation: NamespaceOperation, phase: NamespaceUseTestPhase);
  }

  #[cfg(test)]
  struct TestHookSlot {
      entered: oneshot::Sender<()>,
      release: oneshot::Receiver<()>,
  }

  #[cfg(test)]
  struct TestBarrier {
      key: (NamespaceOperation, NamespaceUseTestPhase),
      owner: Weak<Mutex<HashMap<(NamespaceOperation, NamespaceUseTestPhase), TestHookSlot>>>,
      entered: oneshot::Receiver<()>,
      release: Option<oneshot::Sender<()>>,
  }
  ```

  `arm` creates the two one-shot channel pairs, rejects a duplicate armed `(operation, phase)` in the same fixture, inserts one `TestHookSlot`, and returns the fixture-owned `TestBarrier`. `checkpoint` removes its slot while holding the short mutex, returns immediately when no slot is armed, sends `entered`, and awaits `release` after dropping the mutex. Removing before await means handler cancellation cannot strand a map entry. `TestBarrier::release` sends once; dropping it before or during a checkpoint removes any still-armed slot and drops the release sender, so an in-flight checkpoint wakes and returns. A canceled checkpoint, released barrier, and barrier `Drop` all leave the fixture map empty. Cloning `AppState` clones this one fixture's `Arc`; constructing another fixture creates an unrelated map, preventing cross-test interference.

  Add `#[cfg(test)] namespace_use_test_hooks: NamespaceUseTestHooks` to `AppState` and initialize it with `NamespaceUseTestHooks::default()` in every test fixture. Handlers may access checkpoints only as `state.namespace_use_test_hooks`; no hook parameter enters a production interface. In RED, call `checkpoint(operation, LeaseAcquired)` at the future lease-acquisition point immediately before existing read/grep/write/edit/Bash supervisor startup; despite the phase name, no gate exists yet. Call `checkpoint(Bash, BashWaitReturned)` immediately after the existing `CommandHandle::wait().await` returns and before building the response. These compile-time-test-only calls preserve production behavior.

  Add `namespace_use_test_hooks_are_fixture_private_and_cleanup_slots` before RED: arm identical keys in two independently constructed `AppState` fixtures, prove each handler wakes only its own barrier, release one without affecting the other, drop/cancel armed and entered barriers, and assert both maps are empty. An unarmed checkpoint must complete under a short timeout. This seam test is expected to pass in RED and proves the test mechanism itself is deterministic and isolated before endpoint behavior is evaluated.

  Add table-driven `active_endpoint_use_makes_reset_conflict_without_deletion` for `/v1/read`, `/v1/grep`, `/v1/write`, and `/v1/edit`: create `<workspace>/<namespace-key>/keep.txt`; arm `LeaseAcquired`; spawn the real endpoint request; await `entered`; issue `/v1/reset`; assert 409 and non-deletion; release the checkpoint; await endpoint success; issue reset again; assert 200 and both workspace/jail roots are absent.

  Add separate `bash_same_use_lease_spans_supervisor_execution_and_binding_drain` using one Bash request and two independently armed slots before spawning it. First await `(Bash, LeaseAcquired)` before supervisor startup, issue reset, and assert 409 plus `keep.txt` remains. Release only that test barrier—not the namespace lease—so the command and any command-bound Fetch output may execute. Then await `(Bash, BashWaitReturned)` after `CommandHandle::wait` has returned its binding-drain result, issue reset a second time, and again assert 409/non-deletion. Release the second test barrier, await the original Bash response, then assert a third reset returns 200 and removes workspace/jail roots. The same request must reach both receipts; do not split phases across two requests or reacquire a lease between them.

  Keep this first RED independent of `NamespaceGate`; direct gate unit tests are added after the type skeleton exists in Step 3. Also include in the endpoint fixture an assertion that a successful post-release reset removes both `<workspace>/<namespace-key>` and `<workspace>/.runtime-jails/<namespace-key>`.

- [ ] **Step 2: Run the focused tests and retain the RED evidence**

  ```powershell
  cargo test --manifest-path "agent-runtime/Cargo.toml" active_endpoint_use_makes_reset_conflict_without_deletion -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" bash_same_use_lease_spans_supervisor_execution_and_binding_drain -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" namespace_use_test_hooks_are_fixture_private_and_cleanup_slots -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" reset_removes_only_target_namespace -- --nocapture
  ```

  Expected RED: every target compiles and reaches its fixture-owned armed checkpoint; hook isolation/cleanup passes without global state, while current reset returns success at the first active endpoint/Bash checkpoint and violates the exact 409/non-deletion assertion. In GREEN the unchanged Bash test continues through its second checkpoint; an undefined gate symbol, cross-fixture wakeup, stale slot, barrier timeout, or endpoint setup failure is invalid RED evidence.

- [ ] **Step 3: Implement the process-local gate and lease all operations**

  Use only standard-library and Tokio synchronization already in the repository:

  ```rust
  #[derive(Clone, Default)]
  pub(crate) struct NamespaceGate {
      locks: Arc<Mutex<HashMap<String, Weak<tokio::sync::RwLock<()>>>>>,
  }

  #[derive(Debug)]
  pub(crate) struct NamespaceGateError {
      message: &'static str,
  }

  pub(crate) struct NamespaceUseLease {
      _guard: tokio::sync::OwnedRwLockReadGuard<()>,
  }

  pub(crate) struct NamespaceResetLease {
      _guard: tokio::sync::OwnedRwLockWriteGuard<()>,
  }
  ```

  Introduce the type/method skeleton with a temporary compile-safe no-coordination implementation that creates a fresh `Arc<RwLock<()>>` per call (so it can construct the exact owned guard types but cannot coordinate two calls), then add direct tests `active_use_lease_makes_reset_conflict_without_deletion`, `new_use_waits_while_reset_lease_is_held`, and `different_namespace_keys_do_not_block_each_other`. Run `cargo test --manifest-path "agent-runtime/Cargo.toml" namespace_gate -- --nocapture`; it must compile and fail the conflict/wait assertions against the fresh-lock skeleton. Then replace the skeleton with the real shared lock implementation below. Do not count skeleton compile failures as RED and do not change these assertions after the failing run.

  Resolve or create one `Arc<RwLock<()>>` under the short map mutex, retain only a `Weak` entry in the map, and acquire the Tokio lock after releasing the map mutex. `acquire_use` awaits an owned read guard. `try_acquire_reset` calls `try_write_owned`; return `Ok(None)` when readers or another writer exist, never wait and never cancel active work.

  Add `namespace_gate: NamespaceGate` to every `AppState` constructor. Read, grep, write, edit, and Bash acquire a use lease after identity validation and hold it until the operation result is complete. Place `LeaseAcquired` immediately after the real use lease is obtained. For Bash the required order is: acquire one `NamespaceUseLease`; await `(Bash, LeaseAcquired)`; start the supervisor; permit command and command-bound Fetch output to finish; await `CommandHandle::wait` (including existing binding drain); await `(Bash, BashWaitReturned)` while that same lease variable remains in scope; only after the second checkpoint is released may the lease drop and the response return. Do not shadow, replace, reacquire, move into a shorter block, or explicitly drop the lease between checkpoints. Reset uses `try_acquire_reset`, returns HTTP 409 `namespace is in use` when it gets `None`, and performs no metadata lookup or deletion before the exclusive lease exists.

  Retain `namespace_use_test_hooks_are_fixture_private_and_cleanup_slots` unchanged while wiring the real gate. It remains `#[cfg(test)]`; production `AppState` layout and handler behavior contain no hook state/calls.

- [ ] **Step 4: Run the focused GREEN tests**

  ```powershell
  cargo test --manifest-path "agent-runtime/Cargo.toml" namespace_gate -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" active_use_lease_makes_reset_conflict_without_deletion -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" active_endpoint_use_makes_reset_conflict_without_deletion -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" bash_same_use_lease_spans_supervisor_execution_and_binding_drain -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" namespace_use_test_hooks_are_fixture_private_and_cleanup_slots -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" reset_removes_only_target_namespace -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" write_read_and_grep_are_namespace_isolated -- --nocapture
  ```

  Expected GREEN: all five real operation classes return 409/non-deletion while held; one Bash request returns 409 both after its real lease is acquired/before supervisor start and after `CommandHandle::wait` has completed binding drain; only releasing the second checkpoint permits that lease to drop and the final reset to return 200. Hook maps are fixture-private, unarmed checkpoints are immediate, release/cancel/Drop leaves no slot, unrelated fixtures/namespaces remain concurrent, and no test hook changes production behavior.

- [ ] **Step 5: Record commit-group A readiness without Git writes**

  Record Task 2 evidence. Do not commit until Task 3 completes the handler/file-operation group.

### Task 3: Bounded, Cancellable Read and Grep

**Files:**
- Create: `agent-runtime/src/scan.rs`
- Modify: `agent-runtime/src/lib.rs: read_file, grep_files, grep_one_file, workspace read/grep helpers, tests`
- Test: `agent-runtime/src/scan.rs: tests module`
- Test: `agent-runtime/src/lib.rs: read/grep integration tests`

**Interfaces:**
- Consumes: validated `RuntimeIdentity` from Task 1, operation lease lifetime from Task 2, existing `max_output_chars`, `cap_std::Dir`, no-follow opens, and `TRUNCATION_MARKER`.
- Produces: `READ_CHUNK_BYTES: usize = 8 * 1024`, `GREP_MAX_SCANNED_BYTES: u64 = 32 * 1024 * 1024`, `GREP_MAX_ENTRIES: u64 = 10_000`, `GREP_MAX_ELAPSED: Duration = Duration::from_secs(2)`, `BoundedText { text, truncated }`, `ScanLimits`, `ScanBudget`, and `spawn_cancellable_blocking(F)`, whose dropped async future signals the worker.

**Recommended executor:** `complex`

- [ ] **Step 1: Write failing byte, entry, elapsed, no-follow, and cancellation tests**

  Before RED, add compile-safe `#[cfg(test)]` adapters in the existing `lib.rs` tests: `read_text_under_test` delegates to the current complete `read_to_string` behavior through a supplied `Read`, `grep_under_test` delegates to the current traversal with injected open/clock observers but no enforced budget, and `blocking_scan_under_test` delegates to the current `spawn_blocking` path without cancellation. Do not create or reference `scan.rs` in RED. After Task 3 implementation, keep the adapters and replace only their bodies with `read_text_bounded`, bounded grep, and `spawn_cancellable_blocking`, so the same assertions become GREEN.

  Add a test fixture `CountingReader<R> { inner: R, bytes_read: usize }` that implements `Read` by delegating to `inner` and incrementing `bytes_read`, plus `bytes_read(&self) -> usize`. Add tests named exactly:

  - `read_stops_after_output_limit_plus_utf8_completion_and_sentinel_detection`: instrument a reader that counts bytes and assert a 128-byte output limit never consumes more than 133 bytes, returns valid UTF-8, and sets `truncated=true`.
  - `grep_stops_at_scanned_byte_budget_without_visiting_later_files`: use a test-only small `ScanBudget` of 64 bytes, assert the later matching file is not opened, partial output ends with the existing sentinel, and `truncated=true`.
  - `grep_stops_at_directory_entry_budget`: create more entries than a test-only budget and assert visited entries equal the budget.
  - `grep_stops_at_elapsed_budget`: inject a deterministic clock into `ScanBudget` under `cfg(test)` and assert expiration is observed between entries without sleeping.
  - `dropping_blocking_scan_future_sets_worker_cancellation`: block a worker between 8 KiB chunks, abort the async owner, and assert the worker observes the atomic cancellation flag and exits.
  - `workspace_grep_remains_cap_dir_nofollow`: retain existing symlink and symlink-race assertions and prove the bounded worker never follows a final symlink.

  Core bounded-read assertion:

  ```rust
  let result = read_text_under_test(&mut counted_reader, 128, Arc::clone(&cancel)).unwrap();
  assert!(result.truncated);
  assert!(result.text.ends_with("\n[truncated]"));
  assert!(counted_reader.bytes_read() <= 133);
  assert!(result.text.is_char_boundary(result.text.len()));
  ```

- [ ] **Step 2: Run the focused tests and retain the RED evidence**

  ```powershell
  cargo test --manifest-path "agent-runtime/Cargo.toml" read_stops_after_output_limit_ -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" grep_stops_at_ -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" dropping_blocking_scan_future_ -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" workspace_grep_remains_cap_dir_nofollow -- --nocapture
  ```

  Expected RED: every target compiles and reaches its bound/cancellation assertion; complete files are still read, traversal exceeds byte/entry/time budgets, and dropping the async owner leaves the worker cancellation flag unset. Missing `scan.rs` symbols or observer setup failures are invalid RED evidence.

- [ ] **Step 3: Implement bounded helpers and preserve capability traversal**

  Define these exact shapes in `scan.rs`:

  ```rust
  pub(crate) struct BoundedText {
      pub(crate) text: String,
      pub(crate) truncated: bool,
  }

  pub(crate) struct ScanBudget {
      scanned_bytes: u64,
      entries: u64,
      started: Instant,
      cancel: Arc<AtomicBool>,
      limits: ScanLimits,
  }

  pub(crate) struct ScanLimits {
      pub(crate) max_scanned_bytes: u64,
      pub(crate) max_entries: u64,
      pub(crate) max_elapsed: Duration,
  }

  pub(crate) async fn spawn_cancellable_blocking<T, F>(job: F) -> Result<T, tokio::task::JoinError>
  where
      T: Send + 'static,
      F: FnOnce(Arc<AtomicBool>) -> T + Send + 'static;

  pub(crate) fn read_text_bounded<R: Read>(reader: &mut R, limit: usize, cancel: Arc<AtomicBool>) -> io::Result<BoundedText>;
  ```

  `spawn_cancellable_blocking` owns a drop guard around one `Arc<AtomicBool>`; disarm it only after the join completes. `read_text_bounded` reads 8 KiB chunks but retains at most `limit + 4 + 1` bytes, checks cancellation before each read, validates UTF-8 up to the retained boundary, and appends the existing sentinel exactly once when another byte exists. Invalid UTF-8 before truncation remains an error.

  Route both `/workspace` and `/skills` reads through the bounded helper. For workspace files, continue opening from `cap_std::Dir` with `FollowSymlinks::No`; do not reconstruct an ambient workspace path. Grep shares one `ScanBudget` across recursive traversal, checks cancellation/deadline before every entry and chunk, counts every directory entry (including skipped non-files), and reads at most the remaining byte budget. A budget stop is a successful partial `TextResponse` with `truncated=true` and one sentinel, not an unbounded retry and not an internal error.

- [ ] **Step 4: Run focused and existing GREEN filesystem tests**

  ```powershell
  cargo test --manifest-path "agent-runtime/Cargo.toml" read_stops_after_output_limit_ -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" grep_stops_at_ -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" dropping_blocking_scan_future_ -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" workspace_grep_remains_cap_dir_nofollow -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" symlink -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" write_read_and_grep_are_namespace_isolated -- --nocapture
  ```

  Expected GREEN: retained/read bytes, entries, and elapsed work are bounded; cancellation is observed cooperatively; all no-follow and namespace isolation regressions pass.

- [ ] **Step 5: Close commit group A through the orchestrator**

  After Tasks 1–3 are GREEN, the orchestrator—not an implementation worker—may create commit group A with suggested title `fix(agent-runtime): harden namespace filesystem operations` and a brief body covering hashed storage keys, unique command jails, reset conflict leases, and bounded cancellable scans.

### Task 4: Fail-Closed Inherited-FD Cleanup Before `RLIMIT_NOFILE`

**Files:**
- Modify: `agent-runtime/src/exec/helper.rs: ExecInitOps, run_exec_init, RealExecInitOps`
- Modify: `agent-runtime/src/exec/helper/test_support.rs: FaultExecInitOps`
- Modify: `agent-runtime/src/sandbox.rs: close_inherited_fds_except and unit tests`
- Modify: `agent-runtime/tests/linux_exec_helper.rs: Linux helper scenarios`
- Test: `agent-runtime/tests/linux_exec_helper.rs`

**Interfaces:**
- Consumes: existing `capture_hard_nofile()`, `close_inherited_fds_except(hard_nofile, retained)`, `COMMAND_CONTROL_FD = 4`, `EXEC_STATUS_FD = 5`, `ExecInitStage::CloseInheritedFds`, and `RlimitSpec::approved_defaults().nofile = 256`.
- Produces: `ExecInitOps::capture_hard_nofile() -> Result<libc::rlim_t, ()>` and `ExecInitOps::close_inherited_fds(libc::rlim_t) -> Result<(), ()>` with initialization order `cgroup_join -> capture hard limit -> close inherited FDs -> rlimit -> no_new_privs -> seccomp -> target_exec`.

**Recommended executor:** `complex`

- [ ] **Step 1: Write failing Linux tests for the high descriptor, ordering, retained FDs, and cleanup failure**

  Add `helper_closes_non_cloexec_fd_above_lowered_nofile` to duplicate `/dev/null` onto FD 300 without `FD_CLOEXEC`, launch the existing helper/target surface, assert `/proc/self/fd/300` is absent, and retain approved `nofile=256`. Keep the existing FD 4/5 mapping assertions. This is the first RED because it compiles against the current helper and fails on the surviving descriptor.

  After recording that RED, perform a behavior-preserving trait split only: add `capture_hard_nofile` and the hard-limit argument to `close_inherited_fds`, update real/fault implementations, but deliberately keep the current bad `rlimit -> capture -> close` call order. Add a pure fake-ops order test whose recorded stages equal:

  ```rust
  assert_eq!(ops.calls, [
      "status_cloexec", "config_read", "config_decode", "config_close",
      "cgroup_join", "capture_hard_nofile", "close_inherited_fds",
      "rlimit", "no_new_privs", "seccomp", "target_exec",
  ]);
  ```

  Run this order test before reordering; it must compile and fail by observing `rlimit` before `capture_hard_nofile`. Then add a minimal test-only syscall adapter around the existing fallback and sandbox tests proving ranges skip retained 4 and 5, an error other than `ENOSYS` is returned immediately, and only `ENOSYS` selects the bounded per-FD fallback. Reuse `ExecInitStage::CloseInheritedFds` for capture or cleanup failure so the fixed four-byte status protocol does not change.

- [ ] **Step 2: Run RED on a native Linux host**

  From PowerShell on native Linux:

  ```powershell
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test linux_exec_helper helper_closes_non_cloexec_fd_above_lowered_nofile -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" exec_init_captures_and_closes_before_rlimit -- --nocapture
  ```

  Expected RED: both commands compile and reach their assertions; FD 300 survives because the helper captures the already-lowered hard limit, and the behavior-preserving trait split records `rlimit` before capture/cleanup. Undefined trait methods or a helper startup failure are invalid RED evidence.

- [ ] **Step 3: Reorder helper initialization and implement ranged closure**

  Change the trait and call sequence to:

  ```rust
  let hard_nofile = ops
      .capture_hard_nofile()
      .map_err(|()| ExecInitStage::CloseInheritedFds)?;
  ops.close_inherited_fds(hard_nofile)
      .map_err(|()| ExecInitStage::CloseInheritedFds)?;
  ops.rlimit(&spec).map_err(|()| ExecInitStage::Rlimit)?;
  ```

  Sort/deduplicate retained descriptors at or above 3, then invoke `close_range` for each non-retained range: with retained `[4, 5]`, close `3..=3` and `6..=u32::MAX`. If any ranged syscall returns `ENOSYS`, run one bounded loop from 3 up to the captured finite hard limit, skipping retained descriptors and accepting only `EBADF`. Any other syscall/close error aborts initialization; never continue to rlimits or target exec after cleanup failure.

- [ ] **Step 4: Run GREEN Linux helper and seccomp regressions**

  ```powershell
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test linux_exec_helper -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test linux_seccomp helper_closes_inherited_fds_and_sets_no_new_privs -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" exec_init_captures_and_closes_before_rlimit -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" close_range_ -- --nocapture
  ```

  Expected GREEN: FD 300 is absent after target exec, required control/status behavior remains intact, and injected cleanup failure prevents target execution.

- [ ] **Step 5: Close commit group B through the orchestrator**

  The orchestrator may commit this independently GREEN Linux boundary with suggested title `fix(agent-runtime): close inherited fds before rlimits`. Workers do not execute the commit.

### Task 5: Fetch Authentication and Request-Head Absolute Deadline

**Files:**
- Modify: `agent-runtime/src/fetch_broker/session.rs: serve_connection request-head flow`
- Modify: `agent-runtime/tests/fetch_broker.rs: pre-auth permit/deadline tests`
- Test: `agent-runtime/tests/fetch_broker.rs`

**Interfaces:**
- Consumes: `PreAuthAdmission::deadline()`, `handshake::PreAuthOutcome::Authenticated`, `read_client_frame`, existing `BrokerMetrics`, quota registry, resolver/connector fakes, and audit recording sink.
- Produces: one uninterrupted admission lifetime and absolute timeout from accept through the first authenticated `ClientFrame::Request`; no protocol type or wire-frame change.

**Recommended executor:** `coding`

- [ ] **Step 1: Write the failing authenticated-idle and permit-lifetime tests**

  Add `authenticated_idle_peer_is_closed_at_original_request_head_deadline_without_side_effects`. Configure one permit and a 100 ms deadline, authenticate after advancing/consuming part of that interval, send no Request, and assert EOF by the original deadline. Assert all of:

  ```rust
  assert_eq!(broker.metrics().active_pre_auth, 0);
  assert_eq!(broker.metrics().handshake_timeouts, 1);
  assert_eq!(broker.metrics().body_frames_read, 0);
  assert_eq!(broker.metrics().resolver_calls, 0);
  assert_eq!(broker.metrics().connector_calls, 0);
  assert!(records.lock().unwrap().is_empty());
  ```

  Replace `preauth_permit_is_released_after_authentication` with `preauth_permit_is_released_after_first_request_head`: an authenticated peer that has not sent Request retains the only permit; a third peer is rejected. After the first peer sends a syntactically valid Request head, the admission permit becomes available while the ordinary reviewed-request flow retains its existing ordering.

- [ ] **Step 2: Run the focused tests and retain the RED evidence**

  ```powershell
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test fetch_broker authenticated_idle_peer_ -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test fetch_broker preauth_permit_is_released_after_first_request_head -- --nocapture
  ```

  Expected RED: both existing integration targets compile and reach the deadline/permit assertions; authentication drops admission before `read_client_frame`, so authenticated idle survives the original deadline and the only permit is released too early.

- [ ] **Step 3: Extend the existing deadline through Request without resetting it**

  Keep `admission` alive after successful authentication and wrap the first frame read in `tokio::time::timeout_at(admission.deadline(), read_client_frame(&mut reader))`. On timeout, increment the existing `handshake_timeouts` metric, close by returning `Ok(())`, and perform no rejection write, quota acquire, audit begin, request review, DNS, or connector call. Accept only `ClientFrame::Request`; retain the existing protocol rejection for other completed frames. Drop admission immediately after a Request frame is fully decoded and before `review_request`.

- [ ] **Step 4: Run GREEN Fetch broker tests**

  ```powershell
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test fetch_broker authenticated_idle_peer_ -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test fetch_broker preauth_permit_is_released_after_first_request_head -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test fetch_broker silent_peer_is_closed_at_one_absolute_handshake_deadline -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test fetch_broker readiness_probe_obeys_handshake_deadline_without_consuming_quota -- --nocapture
  ```

  Expected GREEN: pre-auth, authenticated-idle, and readiness paths share the accepted-at deadline; request work starts only after the first Request frame.

- [ ] **Step 5: Close commit group C through the orchestrator**

  Suggested orchestrator commit title: `fix(agent-runtime): bound authenticated fetch request head`. No worker performs Git writes.

### Task 6: Redis Summary CAS and Monotonic `trace:last`

**Files:**
- Modify: `orm/agentv3.go: summary and trace persistence plus bounded retry helper`
- Modify: `orm/agentv3_test.go: conflict and trace ordering tests`
- Modify: `chatv2/agentv3_context.go: rebuildAgentV3Summary`
- Test: `orm/agentv3_test.go`
- Test: `chatv2/agentv3_runtime_test.go`

**Interfaces:**
- Consumes: existing summary/turn/trace Redis keys, `redis.Client.Watch`, `redis.Tx.Watch`, `redis.Tx.TxPipelined`, and `redis.TxFailedErr` from go-redis v9.22.0.
- Produces: `agentV3CASMaxAttempts = 5`, exported `ErrAgentV3StateConflict`, `AgentV3SummaryBuilder`, `AgentV3UpdateSummary(ctx, scope, maxTurns, ttl, builder) error`, and monotonic existing `AgentV3SaveTraceSummary(ctx, scope, summary, ttl) error` ordered by `(FinishedAt, RunID)`.

**Recommended executor:** `complex`

- [ ] **Step 1: Write deterministic failing conflict and monotonic trace tests**

  Add test-local compile-safe adapters in `orm/agentv3_test.go`: `agentV3UpdateSummaryUnderTest` performs the current single read/compute/`AgentV3SetSummary` sequence with no retry, and `errAgentV3StateConflictUnderTest` is only the expected test sentinel. These adapters exist only to let the exact desired behavior assertions run before exported production APIs exist; the legacy adapter must not simulate CAS or the five-attempt result. After Step 3, replace their bodies/identity with `AgentV3UpdateSummary` and `ErrAgentV3StateConflict` without changing assertions.

  Add `TestAgentV3UpdateSummaryRetriesWholeReadComputeCAS`: call the adapter, block the first builder invocation after it sees version 1, commit a version-2 update from a second goroutine, release the first callback, and assert the first operation reruns its complete callback against version 2 and commits version 3 rather than stale version 2. Assert callback count is exactly 2.

  Add `TestAgentV3TraceLastNeverRegresses` with candidates ordered as follows:

  ```go
  finished := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
  newer := AgentV3TraceSummary{RunID: "run-b", FinishedAt: finished}
  olderTime := AgentV3TraceSummary{RunID: "run-z", FinishedAt: finished.Add(-time.Second)}
  olderTie := AgentV3TraceSummary{RunID: "run-a", FinishedAt: finished}
  ```

  Save `newer`, then concurrently attempt both older values through the existing `AgentV3SaveTraceSummary`; assert `run-b` remains. Save `run-c` at the same timestamp and assert it replaces `run-b`. Add `TestAgentV3CASRetryExhaustionReturnsErrAgentV3StateConflict` by modifying the watched summary key in every adapter callback and asserting `errors.Is(err, errAgentV3StateConflictUnderTest)` after exactly five attempts.

- [ ] **Step 2: Run RED Go tests**

  ```powershell
  go test ./orm -run 'TestAgentV3(UpdateSummaryRetriesWholeReadComputeCAS|TraceLastNeverRegresses|CASRetryExhaustionReturnsErrAgentV3StateConflict)$' -count=1 -v
  ```

  Expected RED: tests compile and reach assertions; the legacy summary adapter commits stale content without rerunning the full callback, and the existing unconditional `trace:last` `SET` lets an older candidate overwrite `run-b`. Undefined exported APIs are invalid RED evidence.

- [ ] **Step 3: Implement bounded WATCH/MULTI retries and move summary compute inside the retry**

  Define:

  ```go
  const agentV3CASMaxAttempts = 5

  var ErrAgentV3StateConflict = errors.New("agent v3 state changed during bounded retry")

  type AgentV3SummaryBuilder func(
      turns []AgentV3Turn,
      current *AgentV3Summary,
  ) (*AgentV3Summary, error)

  func AgentV3UpdateSummary(
      ctx context.Context,
      scope AgentV3Scope,
      maxTurns int,
      ttl time.Duration,
      builder AgentV3SummaryBuilder,
  ) error
  ```

  For each attempt, `WATCH` both `agentV3SummaryCurrentKey(scope)` and `agentV3TurnsKey(scope)`. Inside the watch callback, read/decode the current summary first, then read/decode recent turns, call `builder`, and use `tx.TxPipelined` for the conditional `SET` only when the builder returns a non-nil summary. Retry only `redis.TxFailedErr`; return all other errors immediately; after five conflicts return `ErrAgentV3StateConflict`.

  Rewrite `rebuildAgentV3Summary` as a builder that applies the existing raw-turn cutoff, rendering, hash, and version increment on every retry. It must return nil when no rebuild or no content change is required. Do not add a new Redis key or change serialized `AgentV3Summary`.

  Rewrite `AgentV3SaveTraceSummary` as the same five-attempt WATCH/MULTI pattern on the existing trace key. Missing current state accepts the candidate. Otherwise write only when `candidate.FinishedAt.After(current.FinishedAt)` or timestamps are equal and `candidate.RunID > current.RunID`; an older/equal candidate returns nil without writing or refreshing TTL.

  Point `agentV3UpdateSummaryUnderTest` at `AgentV3UpdateSummary` and `errAgentV3StateConflictUnderTest` at `ErrAgentV3StateConflict`; do not change test scheduling or expected assertions between RED and GREEN.

- [ ] **Step 4: Run GREEN summary/trace tests**

  ```powershell
  go test ./orm -run 'TestAgentV3(UpdateSummaryRetriesWholeReadComputeCAS|TraceLastNeverRegresses|CASRetryExhaustionReturnsErrAgentV3StateConflict)$' -count=1 -v
  go test ./orm ./chatv2 -run 'TestAgentV3|TestRebuildAgentV3Summary' -count=1
  ```

  Expected GREEN: deterministic conflict reruns read/compute, exhaustion is explicit, and `trace:last` never regresses.

- [ ] **Step 5: Record commit-group D readiness without Git writes**

  Record RED/GREEN output. Do not commit until Task 7 completes memory consistency and TTL refresh.

### Task 7: Redis Memory Snapshot CAS, Stale-ID Cleanup, and Unified TTL

**Files:**
- Modify: `orm/agentv3.go: memory state loading/rebuild transaction`
- Modify: `orm/agentv3_test.go: memory conflict and TTL tests`
- Modify: `chatv2/agentv3_context.go: rebuildAgentV3MemorySnapshot`
- Verify: `chatv2/agentv3_commands.go: existing add/forget rebuild callers`
- Test: `orm/agentv3_test.go`

**Interfaces:**
- Consumes: `agentV3CASMaxAttempts` and `ErrAgentV3StateConflict` from Task 6; existing memory item, active-set, current snapshot, and versioned snapshot keys.
- Produces: `AgentV3MemorySnapshotBuilder` and `AgentV3RebuildMemorySnapshot(ctx, scope, ttl, builder) error`, which watches current/active/item keys, removes stale active IDs, writes the snapshot, and expires every remaining memory key in one transaction.

**Recommended executor:** `complex`

- [ ] **Step 1: Write failing deterministic memory conflict and TTL tests**

  Change `setupAgentV3Redis(t)` to return its `*miniredis.Miniredis` while preserving existing call sites. Define test-local `agentV3MemorySnapshotBuilderUnderTest` with the signature shown below and `agentV3RebuildMemorySnapshotUnderTest`, which composes the current `AgentV3ListMemory`/snapshot setter flow and therefore compiles but retains stale-ID, non-CAS, and split-TTL behavior. Do not reference the future exported builder/API in RED. After Step 3, alias the test-local builder to `AgentV3MemorySnapshotBuilder` and replace only the adapter body with `AgentV3RebuildMemorySnapshot`; the same tests and barriers must remain. Add:

  - `TestAgentV3MemorySnapshotRetriesAfterConcurrentMutation`: block the first builder, add a second memory item through a concurrent mutation, release, and assert the retried snapshot contains both items at a strictly newer version.
  - `TestAgentV3MemoryRebuildCleansStaleIDsAndAlignsTTLs`: create two live items plus one active-set ID with no item key, seed an old snapshot, rebuild with `ttl := 2*time.Hour`, assert the stale ID is removed, and compare Redis TTL for both live item keys, active-set key, current snapshot key, and new versioned snapshot key; every value must equal `ttl` in `miniredis`.
  - `TestAgentV3MemoryRetryExhaustionReturnsConflict`: mutate the watched active set in all five builder attempts and assert `ErrAgentV3StateConflict` with no stale snapshot commit.

  Builder use in the test must match the production signature:

  ```go
  type agentV3MemorySnapshotBuilderUnderTest func(
      items []AgentV3MemoryItem,
      current *AgentV3MemorySnapshot,
  ) (*AgentV3MemorySnapshot, error)
  ```

- [ ] **Step 2: Run RED memory tests**

  ```powershell
  go test ./orm -run 'TestAgentV3Memory(SnapshotRetriesAfterConcurrentMutation|RebuildCleansStaleIDsAndAlignsTTLs|RetryExhaustionReturnsConflict)$' -count=1 -v
  ```

  Expected RED: every test compiles and reaches its state assertions; the legacy adapter commits a stale snapshot, leaves the missing-item ID active, and exposes unequal/missing TTLs. An undefined rebuild API is invalid RED evidence.

- [ ] **Step 3: Implement one watched memory read/compute/commit sequence**

  Define:

  ```go
  func AgentV3RebuildMemorySnapshot(
      ctx context.Context,
      scope AgentV3Scope,
      ttl time.Duration,
      builder AgentV3MemorySnapshotBuilder,
  ) error
  ```

  On every bounded attempt, watch current snapshot and active set; inside the callback read active IDs, call `tx.Watch` for every corresponding item key, then read/decode those items. Classify only missing item keys as stale active IDs; return malformed JSON or Redis errors without deleting data. Read the current snapshot after the watch set is complete, sort remains the chat layer's responsibility, and invoke the builder. In one `TxPipelined` transaction: `SREM` stale IDs, `EXPIRE` each live item key, `EXPIRE` the active-set key, and `SET` both current and versioned snapshot keys with the identical `ttl`. Retry the complete ID/item/current read and builder only on `redis.TxFailedErr`; return the shared bounded-conflict error after five attempts.

  Rewrite `rebuildAgentV3MemorySnapshot` to sort and render inside the callback, preserve existing token truncation/hash logic, and derive version 1 or `current.Version + 1` on every retry. Keep `AgentV3AddMemory`, `AgentV3ForgetMemory`, existing keys, and command call flow; mutation callers still invoke rebuild and surface its error.

  Replace `agentV3RebuildMemorySnapshotUnderTest` with a direct call to `AgentV3RebuildMemorySnapshot`; retain the same barrier schedule, stale-ID fixture, TTL keys, and assertions used for RED.

- [ ] **Step 4: Run GREEN memory and command regressions**

  ```powershell
  go test ./orm -run 'TestAgentV3Memory(SnapshotRetriesAfterConcurrentMutation|RebuildCleansStaleIDsAndAlignsTTLs|RetryExhaustionReturnsConflict)$' -count=1 -v
  go test ./orm ./chatv2 -run 'TestAgentV3|TestMemory' -count=1
  ```

  Expected GREEN: concurrent mutation forces full recompute, stale IDs disappear, and all remaining memory TTLs are identical.

- [ ] **Step 5: Close commit group D through the orchestrator**

  After Tasks 6–7 are GREEN, the orchestrator may commit with suggested title `fix(agentv3): linearize redis state rebuilds` and a brief body naming summary/memory CAS, monotonic trace-last, stale-ID cleanup, and unified TTL. Workers do not commit.

### Task 8: Complete-Record Go and Runtime Trace Sinks

**Files:**
- Create: `agent-runtime/src/trace.rs`
- Create: `chatv2/agentv3_trace_test.go`
- Modify: `agent-runtime/src/lib.rs: AppState, TraceRecord visibility, write_trace, trace tests`
- Modify: `agent-runtime/src/main.rs: JsonlTraceSink construction`
- Modify: `agent-runtime/tests/linux_cgroup.rs: AppState fixture`
- Modify: `chatv2/agentv3_trace.go: shared sink lock, append, permissions`
- Test: `agent-runtime/src/trace.rs: tests module`
- Test: `chatv2/agentv3_trace_test.go`

**Interfaces:**
- Consumes: Task 6's monotonic `AgentV3SaveTraceSummary`, existing Runtime `TraceRecord`, existing Go `AgentV3Trace.Finish`, and current JSON serialization.
- Produces: Rust `JsonlTraceSink::new(PathBuf) -> JsonlTraceSink` and `JsonlTraceSink::append_record(Vec<u8>) -> io::Result<()>`; Go `writeAgentV3TraceRecord(io.Writer, []byte) error` plus package-level `agentV3TraceSinkMu sync.Mutex`; Rust `append_runtime_trace_record_with_writer<W: AsyncWrite + Unpin>(...)`; `#[cfg(test)]` controllable writer/barrier adapters in both languages; complete payload-plus-newline writes in both processes; Go directory mode `0700` and file mode `0600` with tightening of existing targets.

**Recommended executor:** `complex`

- [ ] **Step 1: Add controllable writer seams, then write deterministic interleaving and permission tests**

  First make only a behavior-preserving testability extraction in each existing trace writer. In Go, move the current payload `Write` plus newline `Write` into `writeAgentV3TraceRecord(w io.Writer, payload []byte) error`; production `appendAgentV3TraceJSONL` calls it unchanged. In Rust, move the current two writes into internal generic `append_runtime_trace_record_with_writer<W: tokio::io::AsyncWrite + Unpin>(writer: &mut W, payload: &[u8], #[cfg(test)] checkpoint: Option<&TraceWriteCheckpoint>) -> io::Result<()>`, used by `write_trace`; the test checkpoint waits only after the payload part in RED. Neither extraction may add a lock, combine writes, tighten permissions, or reference `JsonlTraceSink` yet.

  Implement `barrierTraceWriter`/`BarrierTraceWriter` test doubles over a shared byte buffer. Arm two appenders so both payload parts are appended and reported `entered` before either newline part is released. This deterministically produces `payload-1payload-2\n\n` under the legacy two-part writer. Release both, split the same captured bytes by newline, assert exactly two non-empty lines, deserialize every line with `json.Unmarshal`/`serde_json::from_slice`, and assert both unique IDs appear once. This exact parse assertion is the RED and must be rerun unchanged after GREEN; a probabilistic scheduler-only 128-goroutine failure is insufficient evidence.

  Also start 128 concurrent appenders against the real path/sink, await all, split the file by newline, assert exactly 128 non-empty records, and deserialize every line. Give each record a unique integer/run ID and assert the set contains all values once.

  Go permission test core:

  ```go
  func mustMode(t *testing.T, path string) fs.FileMode {
      t.Helper()
      info, err := os.Stat(path)
      require.NoError(t, err)
      return info.Mode()
  }

  if runtime.GOOS == "windows" {
      t.Skip("Unix owner modes are not represented on Windows")
  }
  require.NoError(t, os.Chmod(traceDir, 0o755))
  require.NoError(t, os.Chmod(tracePath, 0o644))
  require.NoError(t, appendAgentV3TraceJSONL(tracePath, payload))
  assert.Equal(t, fs.FileMode(0o700), mustMode(t, traceDir).Perm())
  assert.Equal(t, fs.FileMode(0o600), mustMode(t, tracePath).Perm())
  ```

  Name the deterministic tests `TestAppendAgentV3TraceJSONLForcedPayloadNewlineInterleave` and `trace_sink_forced_payload_newline_interleave_is_indivisible`. Add a Rust unit test that passes already-marshaled payloads to one cloned sink and proves complete JSONL records. Do not change Fetch audit tests or add cross-process file locking.

- [ ] **Step 2: Run RED trace tests**

  ```powershell
  go test ./chatv2 -run 'TestAppendAgentV3TraceJSONL(ForcedPayloadNewlineInterleave|ConcurrentRecords|TightensPermissions)$' -count=1 -v
  cargo test --manifest-path "agent-runtime/Cargo.toml" trace_sink_forced_payload_newline_interleave_is_indivisible -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" trace_sink_concurrent_records_are_indivisible -- --nocapture
  ```

  Expected RED: both targets compile; the armed writers confirm both payload writes occurred before either newline, and the shared JSONL parse assertion fails deterministically on the concatenated line; the Go mode assertion also observes 0755/0644. Undefined sink types, a barrier timeout, or a failure before both `entered` receipts are invalid RED evidence.

- [ ] **Step 3: Implement shared complete-record sinks**

  Rust contract:

  ```rust
  #[derive(Clone)]
  pub struct JsonlTraceSink {
      path: Arc<PathBuf>,
      lock: Arc<tokio::sync::Mutex<()>>,
  }

  impl JsonlTraceSink {
      pub fn new(path: PathBuf) -> Self;
      pub async fn append_record(&self, payload_with_newline: Vec<u8>) -> io::Result<()>;
  }
  ```

  Marshal `TraceRecord` and append `b'\n'` before locking. `append_record` returns `io::ErrorKind::InvalidInput` if the provided payload lacks exactly one terminal newline; it never mutates the payload. It acquires the shared lock, creates the parent as currently done, opens append/create, and performs one `write_all` call while locked. Replace `AppState::trace_jsonl_path` with `trace_sink`; preserve warning/info trace semantics and ignore sink failures exactly as the existing `write_trace` does.

  In Go, marshal before `agentV3TraceSinkMu.Lock`, append `\n` to the byte slice before locking, then under the lock: `MkdirAll(dir, 0o700)`, `Chmod(dir, 0o700)`, `OpenFile(path, O_CREATE|O_APPEND|O_WRONLY, 0o600)`, `Chmod(path, 0o600)`, and one `Write(record)`. Keep the lock through close/error handling so writes to the shared path cannot interleave. Do not alter payload fields, preview policy, or trace save timeout.

  Route the extracted test seams through the complete-record primitive: one call receives payload-plus-newline and no checkpoint can occur between them. Keep the controllable writers and the exact JSONL parse assertions from RED; do not replace them with a new sink-only assertion.

- [ ] **Step 4: Run GREEN trace and race tests**

  ```powershell
  go test ./chatv2 -run 'TestAppendAgentV3TraceJSONL(ForcedPayloadNewlineInterleave|ConcurrentRecords|TightensPermissions)$' -count=1 -v
  go test -race ./chatv2 -run 'TestAppendAgentV3TraceJSONLConcurrentRecords$' -count=1
  cargo test --manifest-path "agent-runtime/Cargo.toml" trace_sink_forced_payload_newline_interleave_is_indivisible -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" trace_sink_concurrent_records_are_indivisible -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" runtime_writes_trace_jsonl -- --nocapture
  ```

  Expected GREEN: the same forced schedule still records both append attempts but each indivisible payload-plus-newline is a valid line; every line parses and appears once; Go owner modes are tightened on Unix; race detector reports no data races.

- [ ] **Step 5: Close commit group E through the orchestrator**

  Suggested orchestrator commit title: `fix(agentv3): serialize trace record writes`. Workers do not execute Git writes.

### Task 9: Fatal Startup Validation for Enabled Agent v3 Chats

**Files:**
- Create: `chatv2/chatv2_test.go`
- Modify: `chatv2/chatv2.go: Init and new startup validator`
- Modify: `main.go: chatv2.Init error handling`
- Verify: `chatv2/agentv3_context.go: validateAgentV3RuntimeConfig remains request-time defense`
- Test: `chatv2/chatv2_test.go`

**Interfaces:**
- Consumes: `ChatConfigSingle.IsAgentV3Enabled()`, `validateAgentV3RuntimeConfig(*config.AgentV3Config) error`, `config.BotConfig.Agents`, and `log.Panic`.
- Produces: `validateAgentV3StartupConfig() error`; `chatv2.Init(context.Context) error` returns the named-chat validation error before manager/compile work; `main` treats that error as fatal.

**Recommended executor:** `coding`

- [ ] **Step 1: Write the failing startup matrix**

  Add table-driven `TestValidateAgentV3StartupConfig` covering:

  ```go
  enabledV3Chat := &config.ChatConfigSingle{
      Name: "v3-chat",
      Agent: &config.AgentConfig{Enable: true, V3: true},
  }
  ```

  For RED, test the existing public `Init` behavior only: table-drive global Agent v3 disabled, globally enabled with no v3 chat, globally enabled plus enabled v3 chat and `Runtime.Enable=false`, and valid `remote_http`/`system_prompt`. Add `TestInitRejectsInvalidAgentV3RuntimeBeforeCompilation`; assert invalid enabled Runtime returns an error containing `v3-chat` and `runtime is disabled` before `compiledChats` changes. Retain `TestValidateAgentV3RuntimeConfig` to prove request-time checks still exist. Add direct `TestValidateAgentV3StartupConfig` only after Step 3 defines that internal helper; the public `Init` assertion is the unchanged RED/GREEN acceptance surface.

- [ ] **Step 2: Run RED startup tests**

  ```powershell
  go test ./chatv2 -run 'Test(InitRejectsInvalidAgentV3RuntimeBeforeCompilation|ValidateAgentV3RuntimeConfig)$' -count=1 -v
  ```

  Expected RED: tests compile and `Init` returns nil or continues compilation for the invalid enabled Runtime, failing the exact error/pre-compilation assertion. An undefined private validator is invalid RED evidence.

- [ ] **Step 3: Implement startup preflight and fatal main handling**

  Define `validateAgentV3StartupConfig() error` in `chatv2.go`. Return nil unless `BotConfig`, global `AgentV3`, and `Agents` exist. Iterate only enabled v3 chats; for each, call the existing request-time validator and wrap failures as `chatv2: agent v3 chat %q cannot use runtime: %w`. Call this validator as the first operation in `Init`, before `NewMcpManager` and before compile logging.

  Change `main` to:

  ```go
  if err := chatv2.Init(context.Background()); err != nil {
      log.Panic("chatv2: init failed", zap.Error(err))
  }
  ```

  Preserve graceful per-chat compilation skipping for unrelated compile errors; only the global-enabled v3 Runtime preflight becomes fatal.

- [ ] **Step 4: Run GREEN startup tests**

  ```powershell
  go test ./chatv2 -run 'Test(ValidateAgentV3StartupConfig|InitRejectsInvalidAgentV3RuntimeBeforeCompilation|ValidateAgentV3RuntimeConfig)$' -count=1 -v
  go test . ./chatv2 -count=1
  ```

  Expected GREEN: invalid enabled v3 Runtime fails before compilation, valid/irrelevant configurations retain existing startup behavior, and request-time validation tests pass.

- [ ] **Step 5: Record commit-group F readiness without Git writes**

  Record the startup evidence. Task 10 shares this configuration commit group.

### Task 10: Explicit Existing `custom.yaml` Diagnostics

**Files:**
- Modify: `config/config.go: InitViper custom override branch`
- Modify: `config/config_test.go: custom override tests`
- Verify: `.gitignore: custom.yaml remains ignored`
- Verify: `Dockerfile` and `docker-compose.yml: only config.yaml remains mounted/copied`
- Test: `config/config_test.go`

**Interfaces:**
- Consumes: existing `InitViper(configFile, envPrefix)`, `viper.New`, `zap.L`, `os.Stat`, and `errors.Is(err, os.ErrNotExist)`.
- Produces: silent behavior only for an absent optional override; warning with `customConfigFile` and error for an existing unreadable or malformed override; unchanged merge for valid override.

**Recommended executor:** `coding`

- [ ] **Step 1: Write failing absent/malformed/valid tests with an observed logger**

  Add `TestCustomConfigMissingIsSilent`, `TestCustomConfigMalformedLogsDiagnostic`, `TestCustomConfigUnreadableLogsDiagnostic`, and retain/rename the existing valid merge test as `TestCustomConfigValidOverridesBase`. For malformed input, write `debug: [unterminated` to an existing `custom.yaml`, install `zaptest/observer`, call `InitViper`, and assert one warning contains message `an error was produced when reading custom config!`, field `customConfigFile` equal to the exact temp path, and a non-empty error field. For the portable unreadable-path case, create a directory named `custom.yaml` so `os.Stat` succeeds and `ReadInConfig` fails. The missing-file test asserts no warning with that message.

- [ ] **Step 2: Run RED config tests**

  ```powershell
  go test ./config -run 'TestCustomConfig(MissingIsSilent|MalformedLogsDiagnostic|UnreadableLogsDiagnostic|ValidOverridesBase)$' -count=1 -v
  ```

  Expected RED: the config package compiles and the observed logger assertion fails because the existing malformed override is silently ignored; missing-file and valid-merge controls still pass.

- [ ] **Step 3: Distinguish absence from existing read failure without changing optionality**

  Before reading the override, call `os.Stat(customConfigFile)`. Ignore only `errors.Is(err, os.ErrNotExist)`. Warn for any other stat error. If the path exists, call `v.ReadInConfig`; warn on any parse/read error; merge and retain the existing success info only on success. Keep `InitViper`'s public signature and base-config behavior unchanged. Do not copy or mount `custom.yaml`, and do not remove it from `.gitignore`.

- [ ] **Step 4: Run GREEN override tests**

  ```powershell
  go test ./config -run 'TestCustomConfig(MissingIsSilent|MalformedLogsDiagnostic|UnreadableLogsDiagnostic|ValidOverridesBase)$' -count=1 -v
  rg -n '^custom\.yaml$' ".gitignore"
  rg -n 'config\.yaml' "Dockerfile" "docker-compose.yml"
  ```

  Expected GREEN: malformed or unreadable existing paths emit one explicit warning; absent remains optional/silent; valid override still wins; Git/Docker surfaces remain unchanged.

- [ ] **Step 5: Close commit group F through the orchestrator**

  After Tasks 9–10 are GREEN, the orchestrator may commit with suggested title `fix(agentv3): fail fast on invalid startup config` and a brief body that also names malformed `custom.yaml` diagnostics. Workers do not commit.

### Task 11: Production Runtime Network Probe Packaging and Attack Matrix

**Files:**
- Modify: `agent-runtime/Dockerfile: runtime-base and runtime-security-test copies`
- Modify: `scripts/agent-runtime-attack-matrix.sh: production_activation_default_off only`
- Modify: `scripts/test-agent-runtime-compose.sh: static production-probe checks`
- Test: `scripts/test-agent-runtime-compose.sh`

**Interfaces:**
- Consumes: existing `agent-runtime-net-probe socket inet|inet6`, production `runtime-base`, `assert_bash_success`, and test-only `runtime-security-test` Python tooling.
- Produces: `/usr/local/bin/agent-runtime-net-probe` in the production Runtime image and a production-default-off probe containing no Python; fixture/security-test Python remains untouched.

**Recommended executor:** `coding`

- [ ] **Step 1: Add failing static checks before changing packaging/script**

  In `scripts/test-agent-runtime-compose.sh`, add checks that require the net-probe `COPY` before `USER runtime`, require `production_activation_default_off` to call both `agent-runtime-net-probe socket inet` and `socket inet6`, and reject a `python3` token within that function's source range. Keep existing checks that permit Python in `runtime-security-test`, fixture health checks, and the broad unique-egress matrix.

  Windows-local RED inspection:

  ```powershell
  rg -n 'COPY --from=build .*agent-runtime-net-probe|FROM runtime-base AS runtime-security-test|USER runtime' "agent-runtime/Dockerfile"
  rg -n 'production_activation_default_off|python3 - <<|agent-runtime-net-probe socket' "scripts/agent-runtime-attack-matrix.sh"
  ```

  Expected RED: the static assertions run against existing files and fail because the only probe copy is after `FROM runtime-base AS runtime-security-test` while the production-default-off function contains Python.

- [ ] **Step 2: Run the executable RED check on a provisioned native Linux host**

  ```powershell
  & "/usr/bin/bash" "scripts/test-agent-runtime-compose.sh"
  ```

  Expected RED: the script itself runs and its new production-probe assertions fail before packaging/script edits; a parser error or missing prerequisite is not behavioral RED. Existing environment prerequisites must already be supplied; do not install missing host tools.

- [ ] **Step 3: Ship and invoke the Rust probe only in the production-default-off case**

  Move/add this copy into `runtime-base` before `USER runtime`:

  ```dockerfile
  COPY --from=build /src/target/release/agent-runtime-net-probe /usr/local/bin/agent-runtime-net-probe
  ```

  Remove the now-redundant security-test-only copy. Replace lines 1125–1133 of the production-default-off command with exact shell assertions:

  ```text
  test "$(agent-runtime-net-probe socket inet)" = errno=1;
  test "$(agent-runtime-net-probe socket inet6)" = errno=1;
  ```

  Do not alter `python_socket_probe_command`, fixture Python, Node/curl/wget probes, or any test-only image packages because the design excludes their removal.

- [ ] **Step 4: Run GREEN static and image checks**

  Windows-local static lane:

  ```powershell
  rg -n 'COPY --from=build /src/target/release/agent-runtime-net-probe /usr/local/bin/agent-runtime-net-probe' "agent-runtime/Dockerfile"
  rg -n 'agent-runtime-net-probe socket inet|agent-runtime-net-probe socket inet6' "scripts/agent-runtime-attack-matrix.sh"
  ```

  Native-Linux lane:

  ```powershell
  & "/usr/bin/bash" "scripts/test-agent-runtime-compose.sh"
  docker build --file "agent-runtime/Dockerfile" --target runtime --tag "csust-got-agent-runtime-remediation:test" .
  docker run --rm --entrypoint "/bin/sh" "csust-got-agent-runtime-remediation:test" -c 'test -x /usr/local/bin/agent-runtime-net-probe'
  ```

  Expected GREEN: static suite passes and the production target contains an executable probe. EPERM is asserted only inside Runtime-controlled Bash in the attack matrix.

- [ ] **Step 5: Record commit-group G readiness without Git writes**

  Record packaging and script evidence. Task 12 joins this deployment-facing group.

### Task 12: Bilingual Compose, Upgrade, and Legacy Workspace Guidance

**Files:**
- Modify: `README.md: System Requirements, Quick Deployment, Build from Source, Upgrade, Agent v3 Runtime and Controlled Fetch`
- Modify: `README_zh-CN.md: 系统要求、快速部署、从源码构建、升级、Agent v3 Runtime 与受控 Fetch`
- Verify: `docker-compose.yml` and `docker-compose.fetch.yml` required interpolation variables
- Test: static README assertions and Compose rendering

**Interfaces:**
- Consumes: required base variables from `docker-compose.yml`, Fetch variables/profile from `docker-compose.fetch.yml`, source targets `agent-runtime/Dockerfile: runtime|broker`, and Task 1's non-migrating namespace key.
- Produces: mirrored English/Chinese operator steps for base Compose, controlled-Fetch overlay, source-built Runtime/Broker upgrades, image-based upgrades, and offline explicit legacy workspace backup/migration.

**Recommended executor:** `documenting`

- [ ] **Step 1: Record failing documentation assertions before editing**

  ```powershell
  rg -n 'AGENT_RUNTIME_CGROUP_PARENT|AGENT_RUNTIME_WORKSPACE_HOST_ROOT|AGENT_RUNTIME_LOG_HOST_ROOT|AGENT_RUNTIME_CGROUP_HOST_ROOT' "README.md" "README_zh-CN.md"
  rg -n 'docker compose build --pull agent-runtime|cargo build --manifest-path agent-runtime/Cargo.toml --locked --release --bins' "README.md" "README_zh-CN.md"
  rg -n 'offline|离线|legacy|旧工作区|SHA-256' "README.md" "README_zh-CN.md"
  ```

  Expected RED: the documentation/static checks execute and fail exact content assertions because quick-start/upgrade sections do not list required base inputs, the source Runtime build path, or the collision-safe storage-key migration warning.

- [ ] **Step 2: Write exact mirrored deployment guidance**

  In both READMEs, use Docker Compose v2 spelling `docker compose`. List all base inputs by exact name: `AGENT_RUNTIME_TOKEN`, `AGENT_RUNTIME_CGROUP_PARENT`, `AGENT_RUNTIME_WORKSPACE_MAX_BYTES`, `AGENT_RUNTIME_WORKSPACE_FS_MAX_BYTES`, `AGENT_RUNTIME_LOG_FS_MAX_BYTES`, `AGENT_RUNTIME_WORKSPACE_HOST_ROOT`, `AGENT_RUNTIME_LOG_HOST_ROOT`, and `AGENT_RUNTIME_CGROUP_HOST_ROOT`. State that the host cgroup and bounded mount roots must already exist and pass `scripts/validate-agent-runtime-host.sh`; do not instruct installation or automatic creation.

  Add Rust 1.95+ beside Go 1.26+ in both source-build system-requirement lists; these are version floors, not installation instructions.

  For controlled Fetch, list `AGENT_FETCH_ENABLE=true`, `AGENT_FETCH_POLICY_VERSION`, `AGENT_FETCH_EXTRA_DENY_CIDRS`, `AGENT_FETCH_DNS_SERVERS`, `AGENT_FETCH_AUDIT_FS_MAX_BYTES`, `AGENT_FETCH_AUDIT_HOST_ROOT`, and `AGENT_FETCH_HMAC_SECRET_FILE`, plus `--profile agent-fetch -f docker-compose.yml -f docker-compose.fetch.yml`. Keep production default off.

  Distinguish upgrades:

  - Current checked-in Compose uses `build:` for Runtime/Broker: pull source, run `docker compose build --pull agent-runtime` (and `agent-fetch-broker` only when overlay enabled), then `docker compose up -d`; `docker compose pull` does not update a source-built Runtime.
  - An operator-maintained GHCR `image:` deployment pulls matching Bot/Runtime/Broker tags, then runs `docker compose pull` and `docker compose up -d`.
  - Direct source verification includes `go build ./...` and `cargo build --manifest-path agent-runtime/Cargo.toml --locked --release --bins`; production Runtime execution remains Linux-only.

  Add the storage note: new Runtime releases use full lowercase SHA-256 namespace directory keys and do not open legacy lossy directories automatically; because the old map could collide, stop Runtime, back up the workspace volume, and perform an explicit offline operator-reviewed migration only when old data must be retained. Do not prescribe an automatic rename algorithm.

- [ ] **Step 3: Run GREEN static documentation assertions**

  ```powershell
  rg -n 'AGENT_RUNTIME_CGROUP_PARENT|AGENT_RUNTIME_WORKSPACE_HOST_ROOT|AGENT_RUNTIME_LOG_HOST_ROOT|AGENT_RUNTIME_CGROUP_HOST_ROOT' "README.md" "README_zh-CN.md"
  rg -n 'docker compose build --pull agent-runtime|cargo build --manifest-path agent-runtime/Cargo.toml --locked --release --bins' "README.md" "README_zh-CN.md"
  rg -n 'SHA-256|offline|离线|旧工作区' "README.md" "README_zh-CN.md"
  $legacyCompose = rg -n 'docker-compose (up|pull)' "README.md" "README_zh-CN.md"
  if ($LASTEXITCODE -eq 0) { throw "legacy docker-compose quick-start command remains: $($legacyCompose -join '; ')" }
  if ($LASTEXITCODE -ne 1) { throw "README legacy-command scan failed with exit code $LASTEXITCODE" }
  ```

  Expected GREEN: first three commands find mirrored guidance; the last command has no matches in quick-start/upgrade command blocks.

- [ ] **Step 4: Render base and Fetch Compose with concrete test-only inputs**

  Run from Windows or Linux PowerShell; rendering does not claim Linux enforcement evidence:

  ```powershell
  $composeTempRoot = [System.IO.Path]::GetTempPath()
  if (-not (Test-Path -LiteralPath $composeTempRoot)) { throw "OS temporary directory is unavailable" }
  $composeSecret = Join-Path $composeTempRoot "csust-got-agent-fetch-hmac-render-test.key"
  [System.IO.File]::WriteAllText($composeSecret, "test-only-hmac-key-2026-08-30-with-32-bytes")
  $env:AGENT_RUNTIME_TOKEN = "test-only-runtime-token-2026-08-30"
  $env:AGENT_RUNTIME_CGROUP_PARENT = "csust-agent.slice"
  $env:AGENT_RUNTIME_WORKSPACE_MAX_BYTES = "1073741824"
  $env:AGENT_RUNTIME_WORKSPACE_FS_MAX_BYTES = "1073741824"
  $env:AGENT_RUNTIME_LOG_FS_MAX_BYTES = "268435456"
  $env:AGENT_RUNTIME_WORKSPACE_HOST_ROOT = "/srv/csust-got/workspaces"
  $env:AGENT_RUNTIME_LOG_HOST_ROOT = "/srv/csust-got/runtime-logs"
  $env:AGENT_RUNTIME_CGROUP_HOST_ROOT = "/sys/fs/cgroup/csust-agent.slice/commands"
  docker compose --file "docker-compose.yml" config --quiet
  $env:AGENT_FETCH_ENABLE = "true"
  $env:AGENT_FETCH_POLICY_VERSION = "policy-v1"
  $env:AGENT_FETCH_EXTRA_DENY_CIDRS = "198.51.100.0/24"
  $env:AGENT_FETCH_DNS_SERVERS = "1.1.1.1:53"
  $env:AGENT_FETCH_AUDIT_FS_MAX_BYTES = "268435456"
  $env:AGENT_FETCH_AUDIT_HOST_ROOT = "/srv/csust-got/fetch-audit"
  $env:AGENT_FETCH_HMAC_SECRET_FILE = $composeSecret
  try {
      docker compose --profile agent-fetch --file "docker-compose.yml" --file "docker-compose.fetch.yml" config --quiet
  } finally {
      Remove-Item -LiteralPath $composeSecret -Force
  }
  ```

  Expected GREEN: both commands exit 0 with no missing-variable diagnostic. These values are render-only and must not be used as production secrets or host-path evidence.

- [ ] **Step 5: Close commit group G through the orchestrator**

  After Tasks 11–12 are GREEN, the orchestrator may commit with suggested title `fix(deploy): align agent runtime verification guidance` and a brief body covering the production probe, Compose inputs, source/image upgrades, and offline legacy migration. Workers do not commit.

### Task 13: Integrated Verification and Final Review Receipt

**Files:**
- Verify only: all files listed in the File Map
- Do not modify: product code, tests, configuration, documentation, or generated lockfiles during this task

**Interfaces:**
- Consumes: GREEN outputs and commit groups A–G from Tasks 1–12.
- Produces: one final code identity with Windows-local, native-Linux, deployment, race, and independent review receipts; no new API.

**Recommended executor:** `normal-task`

This is a verification-only task: it writes no implementation and therefore has no RED/implementation pair. Every behavior-changing task above already records its own RED before implementation and identical focused GREEN afterward.

- [ ] **Step 1: Run the Windows-local/cross-platform gate**

  ```powershell
  $unformatted = gofmt -l "orm/agentv3.go" "orm/agentv3_test.go" "chatv2/agentv3_context.go" "chatv2/agentv3_trace.go" "chatv2/agentv3_trace_test.go" "chatv2/chatv2.go" "chatv2/chatv2_test.go" "config/config.go" "config/config_test.go" "main.go"
  if ($unformatted) { throw "gofmt required: $($unformatted -join ', ')" }
  go test ./orm ./chatv2 ./config -count=1
  go test -race -short ./... -count=1
  go test ./... -count=1
  go build ./...
  cargo fmt --manifest-path "agent-runtime/Cargo.toml" -- --check
  cargo test --manifest-path "agent-runtime/Cargo.toml" --all-targets
  cargo test --manifest-path "agent-runtime/Cargo.toml" --all-targets --features c7-test-support
  cargo build --manifest-path "agent-runtime/Cargo.toml" --locked --bins
  ```

  Expected GREEN: all commands exit 0; race detector is silent; formatting is clean; no verification command writes source files; no dependency or version-floor file changes appear.

- [ ] **Step 2: Run targeted native-Linux Runtime gates**

  From PowerShell 7 on native Linux:

  ```powershell
  if ([string]::IsNullOrWhiteSpace($env:AGENT_RUNTIME_TEST_CGROUP_ROOT)) { throw "AGENT_RUNTIME_TEST_CGROUP_ROOT must name the provisioned delegated cgroup test subtree" }
  go test ./chatv2 -run 'TestAppendAgentV3TraceJSONLTightensPermissions$' -count=1 -v
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test linux_exec_helper -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test linux_seccomp -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test linux_cgroup -- --ignored --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test fetch_broker authenticated_idle_peer_ -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test runtime_fetch_proxy command_bound_fetch_output_hashes_namespace_once_and_preserves_identity -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" trace_sink_forced_payload_newline_interleave_is_indivisible -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" trace_sink_concurrent_records_are_indivisible -- --nocapture
  ```

  Expected GREEN: Go owner modes are 0700/0600; high FD is closed; required FDs survive; the real delegated-cgroup ignored suite passes against the provisioned subtree; authenticated-idle is bounded; command-bound Fetch preserves original identity and writes under exactly one namespace hash; forced and concurrent Runtime trace records parse. Missing delegated-cgroup configuration fails the gate rather than being reported as passed.

- [ ] **Step 3: Run native-Linux deployment acceptance**

  In the already-provisioned target-host environment:

  ```powershell
  & "/usr/bin/bash" "scripts/validate-agent-runtime-host.sh"
  & "/usr/bin/bash" "scripts/test-agent-runtime-compose.sh"
  & "/usr/bin/bash" "scripts/agent-runtime-attack-matrix.sh"
  ```

  Expected GREEN: each exits 0; attack matrix reports `fail=0 skipped=0`; cleanup leaves no containers, networks, temporary cgroups, or test artifacts identified by the scripts. Do not weaken tests or install a missing prerequisite to manufacture a pass.

- [ ] **Step 4: Verify explicit exclusions and repository cleanliness**

  ```powershell
  git diff --check
  git status --short
  git diff -- "chatv2/rich_message.go" "chatv2/agentv3_builtin_skills.go" "chatv2/mcp.go" "chat/mcpo.go" "agent-runtime/src/fetch_policy/redirect.rs"
  rg -n 'dangerous_command_reason|bash_path_escape_reason' "agent-runtime/src" "agent-runtime/tests"
  ```

  Expected: `git diff --check` exits 0; status lists only intended File Map changes before orchestrator commits; the scoped diff command emits nothing; removed command blacklists remain absent. Confirm by diff review that there is no rich-message change, stable-prefix redesign, MCP/MCPO routing change, `/skills` removal, quota addition, ptrace/process-vm change, redirect-header change, force-reset, or Broker drain protocol.

- [ ] **Step 5: Let the orchestrator create/verify commit groups and freeze one final identity**

  The orchestrator stages only each group's listed files, reviews the staged diff, and creates semantic commits with the suggested titles after that group is GREEN. After the final group, it records `git rev-parse HEAD` and does not edit, format, regenerate, or amend any file before independent acceptance reviews. Implementation workers never perform these Git operations.

- [ ] **Step 6: Obtain independent same-identity acceptance reviews**

  The orchestrator—not this plan's executor subagents—dispatches the configured Oracle lane and primary Reviewer lane against the exact final `HEAD` recorded in Step 5. Both receipts must name the same code identity and cover the approved spec. Any accepted repair invalidates both receipts: apply the smallest in-scope change, rerun the affected task plus Steps 1–4, create a new authorized commit, record the new identity, and obtain both reviews again.

## Commit Groups

| Group | Tasks | Suggested semantic commit | Orchestrator staging boundary |
|---|---|---|---|
| A | 1–3 | `fix(agent-runtime): harden namespace filesystem operations` | Identity/gate/scan modules; Runtime handlers/state constructors; Fetch binding/registry/session/response/output path; lifecycle, guardian, C7 fixtures; `agent-runtime/tests/runtime_fetch_proxy.rs`; related Rust tests only. |
| B | 4 | `fix(agent-runtime): close inherited fds before rlimits` | Exec helper, sandbox closure, helper test support, Linux helper tests only. |
| C | 5 | `fix(agent-runtime): bound authenticated fetch request head` | Fetch broker session and broker tests only. |
| D | 6–7 | `fix(agentv3): linearize redis state rebuilds` | ORM Agent v3 state/tests and context rebuild call sites only. |
| E | 8 | `fix(agentv3): serialize trace record writes` | Go/Rust trace sinks, trace tests, and required AppState construction only. |
| F | 9–10 | `fix(agentv3): fail fast on invalid startup config` | Chat startup/main handling plus custom override diagnostics/tests only. |
| G | 11–12 | `fix(deploy): align agent runtime verification guidance` | Runtime Dockerfile, two deployment scripts, and bilingual READMEs only. |

## Requirement Coverage Matrix

| Approved spec requirement | Plan coverage |
|---|---|
| 1. Lossy Runtime workspace/jail identifiers | Task 1 updates every `BindingContext`/lifecycle/guardian/C7 fixture and plaintext integration assertion; its end-to-end Fetch test proves original token/audit identity plus exactly one SHA-256 output layer; Task 12 migration note; Task 13 Linux/integration gates. |
| 2. Read/grep bound output but not work | Task 3; Task 13 Rust gates. |
| 3. Descriptor above lowered nofile can survive | Task 4; Task 13 native-Linux helper suite. |
| 4. Reset races active commands/filesystem work | Task 2 deterministic barrier tests cover real read/grep/write/edit endpoints and one two-phase Bash request: 409/non-deletion after real lease acquisition before supervisor start, another 409 after `CommandHandle::wait`/binding drain, lease drop only after second release, then reset 200; consumed by Task 3; Task 13 integration. |
| 5. Go/Rust JSONL interleave, trace regression, broad Go modes | Tasks 6 and 8; Task 8 uses controllable writers/barriers to force old payload/newline interleaving and reruns the same JSONL parse assertion in GREEN; Task 13 race/Linux checks. |
| 6. Authenticated Fetch peer stalls before Request | Task 5; Task 13 targeted broker test. |
| 7. Summary/snapshot rebuild overwrites newer state | Tasks 6–7 deterministic conflict tests. |
| 8. Memory TTL divergence | Task 7 equal-TTL and stale-ID tests. |
| 9. Invalid enabled Agent v3 Runtime fails only on request | Task 9 startup matrix and fatal main handling. |
| 10. Existing malformed `custom.yaml` is silent | Task 10 observed-log tests. |
| 11. Compose quick-start/source upgrade mismatch | Task 12 static and rendered Compose checks. |
| 12. Production-default-off probe depends on absent Python | Task 11 production image/static/attack-matrix checks. |
| Go/Rust/deployment/full-suite verification | Task 13, with Windows-local and native-Linux evidence explicitly separated. |
| Same-final-identity Oracle and primary Reviewer | Task 13 Steps 5–6, owned by the orchestrator. |
| Explicit exclusions | Global Constraints, Task 11 test-only Python boundary, Task 13 scoped diff/exclusion review. |
| Plan-critic blocker 1: BindingContext/C7/integration identity closure | File Map, Task 1 Files/Interfaces/Steps 1–4, Wave 1 completion rule, commit group A, and Task 13 targeted native-Linux integration command. |
| Plan-critic blocker 2: deterministic endpoint/reset lease proof | File Map and Task 2 define per-`AppState` cloneable hook maps with immediate unarmed return and release/cancel/Drop cleanup; table-driven read/grep/write/edit plus one Bash request prove the same lease at pre-supervisor `LeaseAcquired` and post-drain `BashWaitReturned`; Wave 2 rule and commit group A retain the dependency boundary. |
| Plan-critic blocker 3: behavioral RED and forced trace interleave | Global RED rule; compile-safe seams/adapters in Tasks 1–4 and 6–9; Task 8's controllable writers/barriers and identical RED/GREEN JSONL parser assertions. |
