# Agent v3 Review Remediation Design

**Date:** 2026-08-30

## Goal

Fix the defects confirmed by the full Agent v3 review without changing intentional product contracts or adding unrelated capabilities. The remediation covers Go context persistence and tracing, the Rust Runtime and Fetch broker trust boundaries, startup configuration validation, and deployment verification documentation.

## Confirmed Scope

1. Runtime workspace and jail paths use lossy, path-significant namespace/run identifiers.
2. Runtime read and grep bound returned output but not scanned input or blocking work.
3. The exec helper can inherit non-CLOEXEC descriptors above the lowered `RLIMIT_NOFILE`.
4. Runtime reset can delete a namespace while commands are still using it.
5. Go and Rust JSONL trace records can interleave; Go `trace:last` can regress and trace files are created with broad permissions.
6. An authenticated Fetch peer can hold a broker task indefinitely before sending its Request frame.
7. Go summary and memory snapshot rebuilds can overwrite newer concurrent state.
8. Memory item, active-set, and snapshot TTLs can diverge.
9. An enabled Agent v3 chat can defer an invalid Runtime configuration failure until its first request.
10. An existing but malformed `custom.yaml` is ignored without a diagnostic.
11. Docker quick-start and source-build upgrade instructions do not match required Compose inputs.
12. The production-default-off attack-matrix probe invokes Python that is absent from the production Runtime image.

The following reviewed areas are intentionally excluded because current code, tests, or later contracts establish the behavior: turn-scoped rich-skill activation, volatile memory/tool definitions outside the stable prefix, direct MCP/MCPO egress, generic read-only `/skills` access, logical Bash workspace quotas, ptrace/process-vm isolation, redirect-hop response-header forwarding rules, and Broker process-level graceful drain.

## Chosen Approach

Use targeted linearization at each trust boundary:

- collision-resistant filesystem identities for Runtime paths;
- a namespace use/reset gate for destructive lifecycle operations;
- bounded, cooperatively cancellable filesystem scans;
- fail-closed descriptor cleanup before lowering limits;
- Redis optimistic transactions for shared Go state;
- process-local serialized JSONL sinks with complete-record writes;
- one absolute Fetch handshake/request-head deadline;
- startup-time validation and deployment contract corrections.

This retains the existing data model and protocols. A generation-based workspace model, a new memory schema, distributed filesystem locks, force-reset, and single-writer services are out of scope.

## Runtime Identity and Reset

Validate non-empty namespace and run identifiers with bounded lengths before any filesystem or command operation. Derive workspace directory names from the full namespace using a fixed lowercase SHA-256 key rather than lossy character replacement. Derive each jail path from that namespace key plus the Runtime-generated command ID; caller-provided `run_id` remains logical/audit identity and never selects a path.

Add a Runtime-owned namespace gate. Read, grep, write, edit, Bash, and command-bound Fetch output hold a shared use lease for their lifetime. Reset obtains a non-blocking exclusive lease; if any operation is active it returns an HTTP conflict without deleting anything. New operations wait while the short reset operation owns the namespace. The existing singleton Runtime deployment is the supported coordination boundary; cross-process Runtime replicas remain unsupported.

Changing the storage key intentionally stops automatically opening legacy lossy directories. Because the previous mapping was many-to-one, automatic migration could assign collided data to the wrong namespace. Deployment notes will require an offline, explicit backup/migration when old workspaces must be retained.

## Bounded Read and Grep

Read only enough bytes to produce the configured output plus a truncation sentinel. Grep uses fixed maximum scanned bytes, directory entries, and elapsed time in addition to the existing output limit. Workspace traversal continues to originate from `cap_std::Dir`, opens final files no-follow, and never reconstructs an ambient path for traversal.

Blocking scan workers receive a cancellation flag checked between entries and bounded read chunks. Dropping or timing out the async wrapper signals cancellation. A kernel call already blocked on an unsupported remote filesystem cannot be preempted; the production contract remains a bounded local workspace filesystem.

## Exec Descriptor Cleanup

Capture the inherited hard descriptor limit and close inherited descriptors before applying the command's lower `RLIMIT_NOFILE`. Use `close_range` in ranges around retained control/status descriptors, with a bounded per-FD fallback only for `ENOSYS`. Any cleanup failure aborts exec initialization. Tests must cover a non-CLOEXEC descriptor above 256 while preserving the required descriptors.

## Fetch Request-Head Deadline

Keep the pre-auth admission permit and its original absolute deadline through the first authenticated `ClientFrame::Request`. Authentication does not reset the clock. A peer that authenticates but stalls before Request is closed at the same bounded deadline without quota, audit, DNS, or connector activity.

## Redis State Consistency

Keep existing Redis keys and add compare-and-set writers based on `WATCH`/`MULTI`:

- summary rebuild reads the expected summary version before loading turns and retries the complete read/compute/CAS sequence on conflict;
- memory snapshot rebuild reads its expected version before listing items and retries the complete sequence on conflict;
- `trace:last` writes only when `(FinishedAt, RunID)` is newer than the stored value.

Memory mutation rebuilds also clean stale active IDs and refresh all remaining item, active-set, and snapshot TTLs to the same configured lifetime. Retries are bounded and return an error rather than committing stale content.

## Trace Writes and Permissions

Marshal outside the write lock, append the newline to the payload, then serialize each process's complete-record writes through a shared sink lock. Existing single-process deployment contracts do not require cross-process file locking. Go creates trace directories and files as owner-only and tightens existing targets before writing. Runtime keeps its existing audit semantics while making JSONL records indivisible within the process.

## Startup and Deployment Contracts

Configuration validation rejects a globally enabled Agent v3 setup when an enabled v3 chat cannot use the configured Runtime. Request-time checks remain defensive. `custom.yaml` stays optional and ignored by Git/Docker, but an existing unreadable or malformed file produces an explicit diagnostic instead of disappearing silently.

Update English and Chinese quick-start/upgrade documentation to list required Compose inputs and distinguish pulled images from source-built Runtime targets. Replace the production-image Python network probe with the already shipped `agent-runtime-net-probe`; test-only and fixture Python usage remains unchanged.

## Verification

- Go: deterministic Redis conflict tests, memory TTL tests, concurrent trace tests, permissions tests on Unix, config validation tests, race detector, full suite.
- Rust: namespace collision/escape tests, unique jail tests, active-reset tests, bounded/cancelled scan tests, high-FD Linux tests, authenticated-idle Fetch tests, concurrent trace parsing, all-target and feature suites.
- Deployment: Compose config checks with required inputs, static attack-matrix checks, README command review, existing host/security scripts where the current platform supports them.
- Final acceptance: the same final code identity is reviewed independently by the configured Oracle and primary Reviewer lanes.

## Non-Goals

No rich-message contract change, stable-prefix redesign, MCP routing change, `/skills` capability removal, per-namespace disk quota, force-reset, log rotation service, Broker shutdown protocol, or unrelated cleanup.
