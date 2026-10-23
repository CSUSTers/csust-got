# Agent v3 Runtime and State Safety Contracts

## Goal

This specification covers Go context persistence and tracing, the Rust Runtime and Fetch broker trust boundaries, startup configuration validation, and deployment verification. See the [Fetch isolation contract](2026-08-25-agent-runtime-fetch-egress-design.md) for protocol and production enablement requirements.

The supported coordination boundary is one Runtime process. This contract doesn't introduce distributed filesystem locks, force-reset or a new memory schema. MCP/MCPO egress remains separate from Fetch, and generic read-only `/skills` access remains available.

## Runtime Identity and Reset

Validate non-empty namespace and run identifiers with bounded lengths before any filesystem or command operation. Derive workspace directory names from the full namespace using a fixed lowercase SHA-256 key rather than lossy character replacement. Derive each jail path from that namespace key plus the Runtime-generated command ID; caller-provided `run_id` remains logical/audit identity and never selects a path.

A Runtime-owned namespace gate protects read, grep, write, edit, Bash, and command-bound Fetch output with a shared use lease for their lifetime. Reset obtains a non-blocking exclusive lease; if any operation is active it returns an HTTP conflict without deleting anything. New operations wait while the short reset operation owns the namespace. Cross-process Runtime replicas remain unsupported.

Legacy lossy workspace directories aren't opened automatically. Their mapping was many-to-one, so retaining them requires an offline, explicitly authorized backup and migration with verified namespace ownership. See the README deployment instructions before upgrading or rolling back workspace data.

## Bounded Read and Grep

Read only enough bytes to produce the configured output plus a truncation sentinel. Grep uses fixed maximum scanned bytes, directory entries, and elapsed time in addition to the existing output limit. Workspace traversal continues to originate from `cap_std::Dir`, opens final files no-follow, and never reconstructs an ambient path for traversal.

Blocking scan workers receive a cancellation flag checked between entries and bounded read chunks. Dropping or timing out the async wrapper signals cancellation. A kernel call already blocked on an unsupported remote filesystem cannot be preempted; the production contract remains a bounded local workspace filesystem.

## Exec Descriptor Cleanup

Capture the inherited hard descriptor limit and close inherited descriptors before applying the command's lower `RLIMIT_NOFILE`. Use `close_range` in ranges around retained control/status descriptors, with a bounded per-FD fallback only for `ENOSYS`. Any cleanup failure aborts exec initialization. Tests must cover a non-CLOEXEC descriptor above 256 while preserving the required descriptors.

## Fetch Request-Head Deadline

Keep the pre-auth admission permit and its original absolute deadline through the first authenticated `ClientFrame::Request`. Authentication does not reset the clock. A peer that authenticates but stalls before Request is closed at the same bounded deadline without quota, audit, DNS, or connector activity.

## Redis State Consistency

Redis writers use `WATCH`/`MULTI` without changing the existing keys:

- summary rebuild reads the expected summary version before loading turns and retries the complete read/compute/CAS sequence on conflict;
- memory snapshot rebuild reads its expected version before listing items and retries the complete sequence on conflict;
- `trace:last` writes only when `(FinishedAt, RunID)` is newer than the stored value.

Memory mutation rebuilds also clean stale active IDs. Retries are bounded and return an error rather than committing stale content. Memory items, active-set, snapshot and epoch are persistent keys, not rolling-summary TTL data; legacy memory TTLs are cleared on the current read/write paths. The [memory contract](../../agent_memory.md) defines capacity, deletion epochs, session invalidation, migration and rollback.

## Trace Writes and Permissions

Marshal outside the write lock, append the newline to the payload, then serialize each process's complete-record writes through a shared sink lock. Existing single-process deployment contracts do not require cross-process file locking. Go creates trace directories and files as owner-only and tightens existing targets before writing. Runtime keeps its existing audit semantics while making JSONL records indivisible within the process.

## Startup and Deployment Contracts

Configuration validation rejects a globally enabled Agent v3 setup when an enabled v3 chat cannot use the configured Runtime. Request-time checks remain defensive. `custom.yaml` stays optional and ignored by Git/Docker, but an existing unreadable or malformed file produces an explicit diagnostic instead of disappearing silently.

English and Chinese quick-start/upgrade instructions list required Compose inputs and distinguish pulled images from source-built Runtime targets. Production network probes use the shipped `agent-runtime-net-probe`, not an assumed Python installation; test-only fixtures may use Python.

## Verification

- Go: deterministic Redis conflict tests, memory TTL tests, concurrent trace tests, permissions tests on Unix, config validation tests, race detector, full suite.
- Rust: namespace collision/escape tests, unique jail tests, active-reset tests, bounded/cancelled scan tests, high-FD Linux tests, authenticated-idle Fetch tests, concurrent trace parsing, all-target and feature suites.
- Deployment: Compose config checks with required inputs, static attack-matrix checks, README command review, existing host/security scripts where the current platform supports them.

## Non-Goals

No rich-message contract change, stable-prefix redesign, MCP routing change, `/skills` capability removal, per-namespace disk quota, force-reset, log rotation service, Broker shutdown protocol, or unrelated cleanup.
