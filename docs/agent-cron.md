# Agent cron

Cron lets an interactive agent schedule one-time or recurring, self-contained work. A dedicated
enabled top-level agent runs it and the bot reports the final result to the original
Telegram chat and forum topic. It is off while `agent_v3.cron.runner_agent` is empty.

## Configuration

Uncomment and enable the example `cron-runner` agent in `config.yaml`, give it the intended model,
tools and permissions, and set `agent_v3.cron.runner_agent: cron-runner`. It needs no
command/regex/reply triggers. The name must identify exactly one enabled, compiled
agent; invalid configuration fails startup even if no agents are enabled.

| Setting | Default | Meaning |
| --- | --- | --- |
| `runner_agent` | empty | Dedicated top-level runner name; empty means not deployed |
| `poll_interval_minutes` | 1 | Poll interval, 1–60 minutes |
| `timezone` | `Asia/Shanghai` | IANA timezone captured on each new task |
| `max_concurrency` | 4 | Shared Redis execution concurrency, 1–64 |
| `chat_cooldown_seconds` | 60 | Minimum interval between chat execution claims |
| `max_tasks_per_chat` | 20 | Atomic chat task quota |
| `max_prompt_bytes` | 16384 | UTF-8 prompt byte limit |
| `max_manual_retries` | 2 | Limited explicit retries per occurrence |
| `run_timeout` | `10m` | Execution limit; the runner's timeout can shorten it |

Runtime namespace stays `bot_username:tg:chat_id`. Tasks from another bot/chat are
not visible. Renaming a bot does not migrate its tasks. Cron tasks in the same chat
are mutually exclusive, but normal chat requests are **not** serialized with cron.

## Tools

When configured, the fixed agent tool surface automatically includes `delegate`
and `cron_tasks`. No model-supplied chat, topic, creator, runner, model or namespace
override is accepted. These are taken from trusted current-turn context.

```json
{
  "cron": "0 9 * * 1-5",
  "prompt": "## Context\nPrepare the daily status of the project's public releases.\n## Steps\nCheck the specified release source, compare the previous saved status in /workspace, and save today's status.\n## Goal\nReturn a short summary with source links and changes."
}
```

Pass that object to `delegate`. The three headings must occur exactly once, in
Context / Steps / Goal order, with nonempty content. Write all necessary context
and concrete sources into the prompt; do not use references like “the above”.
Prompts are data, not Go templates. Identical normalized cron/timezone/until/prompt
for the same creator and chat is deduplicated atomically. Creation returns `task_id`,
`version`, `cron`, `timezone`, `until`, `next_run_at`, and `deduplicated`.
The optional `until` field limits scheduled occurrences and enables automatic cleanup;
omit it to keep the task's existing unlimited/retained behavior. An empty string
on creation is invalid.

`cron_tasks` accepts:

```json
{"action":"list","limit":20}
{"action":"get","task_id":"TASK_ID"}
{"action":"update","task_id":"TASK_ID","expected_version":1,"cron":"0 10 * * 1-5"}
{"action":"update","task_id":"TASK_ID","expected_version":2,"until":"2026-11-30 10:00"}
{"action":"update","task_id":"TASK_ID","expected_version":3,"until":""}
{"action":"retry","task_id":"TASK_ID","expected_version":2,"run_id":"RUN_ID"}
{"action":"delete","task_id":"TASK_ID","expected_version":3}
```

List uses an optional cursor (default 20, maximum 50 entries). Get includes the
bounded latest result and report status. Queries are chat-scoped; **only the task
creator** may update/delete/retry, with a current `expected_version`. Update accepts
cron, prompt and/or until and keeps the captured timezone. Active execution, queued
retry or pending/claimed report prevents update. There is no pause/disable action.
Prompt-only and until-only updates preserve the existing next occurrence (even if
overdue), rather than restarting a relative delay. Omit until on update to retain
it, set a nonempty value to replace it, or use `"until":""` to clear it. Explicit
JSON null is invalid; management actions other than update reject the field.
A completed one-time task without until requires an explicit
new `cron` to schedule it again. A task retaining its final failed report for
automatic cleanup cannot be updated. Terminal tasks have `next_run_at: null` in
tool JSON; tasks without a cutoff have `until: null`, not a year-0001 timestamp.

Before retry, get the latest version and run ID, explain possible earlier side
effects, and obtain an explicit user request. A failed execution is queued for a
new run. A successful execution whose report failed returns `mode: report_only`
and only resends stored output. Both consume a version and a bounded retry budget;
stale/replayed requests fail. Failure messages identify the task/run; no inline
Telegram callback is required. Retry requires the task to remain stored: a final
delivered or policy-suppressed report for a task with until causes immediate cleanup.

## Scheduling and safety

The `cron` field accepts these schedules:

| Syntax | Meaning / example |
| --- | --- |
| `m h dom mon dow` | Original five-field cron, e.g. `0 9 * * 1-5` |
| `@at <duration>` | Once after a delay, e.g. `@at 10m` or `@at 1h30m` |
| `@at <datetime>` | Once at RFC3339 with offset/Z, or local `YYYY-MM-DD HH:MM` |
| `@at HH:MM` | Once at the next strictly future occurrence of this local time |
| `@at tomorro HH:MM` | Once on the next local calendar day, e.g. `@at tomorro 09:00` |
| `@daily HH:MM` | Daily, e.g. `@daily 09:05` |
| `@month <1-31> HH:MM` / `@monthly <1-31> HH:MM` | Monthly on this day; skip months without it |
| `@week <0-7\|weekday> HH:MM` / `@weekly <0-7\|weekday> HH:MM` | Weekly, e.g. `@weekly 1 09:00` or `@weekly Wednesday 09:00`; 0/7 is Sunday |
| `@every <weekday> HH:MM` | Weekly calendar schedule, e.g. `@every Wed 09:00`; weekday must be a name, not a number |
| `@every <duration>` | Fixed delay, e.g. `@every 90s` |

Times are 24-hour `HH:MM`, without seconds. `tomorro` is the exact accepted spelling;
`tomorrow`, bare `@daily`, and other macros are not supported. Weekly aliases require
both a weekday and `HH:MM` (e.g. `@every Wed` and `@weekly Wed` are invalid); there
is no default time. Names are case-insensitive, with exactly three-letter or full
English forms (`Sun`/`Sunday` through `Sat`/`Saturday`). Numeric weekdays 0–7 remain
supported with `@week`/`@weekly`, but not `@every`. Weekday names are not supported
in original five-field cron or monthly aliases.
Durations use Go syntax (`90s`, `1h30m`, `1ms`), must be positive whole milliseconds
within its duration range, and do not support `d`/`w` units. The reference clock's
nanoseconds are preserved; the millisecond Redis index does not allow early claims.

All local dates/times use the timezone captured on the task, even after configuration
changes. An RFC3339 offset determines the absolute instant independently of that
timezone. New absolute deadlines at or before now are rejected. Once resolved,
`@at` is stored as an absolute UTC RFC3339Nano deadline, never reinterpreted as a
relative delay on restart. Equal absolute deadlines deduplicate; the same `@at 10m`
at different request times does **not** promise idempotency.

Explicit local dates and `tomorro` reject nonexistent DST times. Repeated local
times choose the earliest strictly future instant and execute once only. Bare
`@at HH:MM` skips a nonexistent local time to the next date where it exists.
Tomorrow means the next calendar date, not a 24-hour delay. Daily/monthly/weekly
aliases canonicalize to five numeric fields and retain cron's gap/fold behavior and dedup.

Original five numeric fields: minute, hour, day-of-month, month, day-of-week. Supported:
`*`, lists, inclusive ranges, positive steps; Sunday is 0 or 7. No seconds/year,
names, `?`, `L`, `W`, or `#`. Restricted day-of-month and day-of-week use OR;
wildcard fields (including `*/n`) follow the parser's wildcard matching semantics.
Next time is strictly later, at minute precision. Nonexistent DST minutes are
skipped and repeated minutes are distinct instants. Unsatisfiable schedules fail.

New tasks first execute at the next occurrence, not immediately. Downtime or
congestion merges missed occurrences into at most one due run; there is no backlog
replay. Expired execution leases become interrupted failures, never automatic model
replays. A calendar-cron task's next normal occurrence remains scheduled independently
of queued manual retry; a normal occurrence that becomes due takes priority.

`@every <duration>` is **fixed-delay**, not fixed-rate or minute-step cron: its first deadline
is creation/rescheduling time plus the duration; subsequent deadlines are completion
or interrupted-lease recovery time plus the duration. Manual execution retry moves
the next deadline too; report-only retry does not. Poll interval, chat cooldown,
concurrency limits and report blocking still apply: `@every 30s` does not guarantee
starting every 30 seconds. By contrast, `@every <weekday> HH:MM` is weekly calendar
cron, using the captured timezone and the same gap/fold rules as five-field cron.

One-time tasks that were never claimed keep their original deadline across downtime
and run once when eligible. Success, failure, skip, and interrupted-lease recovery
all end ordinary scheduling: no next deadline or due-index entry remains. Without `until`, results,
reports and eligible explicit retries remain available; retries do not create a new
ordinary occurrence. Reports say “无下一次计划（一次性已结束）”. Completed tasks remain
stored and **still count toward the chat quota** until explicitly deleted. There is
no automatic cleanup for tasks without `until`, and no automatic execution retry.
Tasks with `until` instead follow the final-report retention and cleanup rules below.

### Optional cutoff and automatic cleanup

Pass `until` alongside any supported cron expression, including five-field cron,
`@daily`, weekly/monthly aliases, fixed-delay `@every`, and one-time `@at`:

```json
{
  "cron": "@daily 09:00",
  "until": "2026-10-31T09:00:00+08:00",
  "prompt": "## Context\nCheck the project's public releases.\n## Steps\nRead the specified release source and summarize changes.\n## Goal\nReturn a short summary with source links."
}
```

`until` accepts RFC3339 with an explicit offset or `Z` (including fractional
seconds), or local `YYYY-MM-DD HH:MM` in the task's captured timezone. It is an
absolute cutoff, not an execution count, relative duration, runtime limit or report
deadline. Local DST gaps are rejected; folds choose the earliest strictly future
instance, as for an explicit local `@at` date. Values normalize to UTC on storage
and output. Creation and explicitly setting until require a future cutoff. A new
or rescheduled task with no occurrence at or before its effective cutoff is rejected
without consuming quota. An until-only update cannot exclude its pending occurrence.

The scheduled instant is inclusive: an occurrence **at** until is allowed; one
after it is not. Late execution after downtime or congestion may still run the
saved eligible occurrence once, even if the wall clock has passed until. No missed
backlog is replayed, and a running occurrence is not cancelled at the cutoff.
After success, failure, policy skip or interrupted-lease recovery, the ordinary
next occurrence is computed using the existing calendar/fixed-delay rules and
discarded if beyond until. A bounded one-time task also cleans up after its sole
occurrence and final report, even when that occurrence precedes until.

The final result and report remain stored while delivery is pending or claimed,
including partially sent rich reports. Once the entire report is delivered, or
delivery is suppressed by policy, the task and its indexes are deleted atomically
and chat quota is released. It no longer appears in list/get and cannot be retried.
Automatic cleanup does not undo external effects or messages already sent.

If final delivery fails or exhausts its automatic budget, the task remains available
until the result's existing 24-hour report window expires. Existing explicit retries
remain available within that window and their budgets: report-only retry sends only
unconfirmed parts without rerunning the model or extending the window; execution
retry retains the original scheduled instant, even after until. At expiry, an
unclaimed execution retry cannot start and is cleared with the task. Active execution
or report leases are not deleted out from under a worker; completion or lease
recovery resumes the lifecycle. A completed execution retry creates its own result
and report window, subject to the remaining retry budget.

Retained tasks still count toward quota. Cleanup of expired final reports uses the
existing report poller and Redis indexes, not key TTLs: it resumes after downtime
when the poller and Redis are available. Without until, completed tasks and results
continue to be retained until explicit deletion.

Before execution and delivery, the bot checks static and live Redis whitelist/block
lists, creator fake-ban, chat shutdown, and enabled source/runner agent filters.
Redis policy failure is fail-closed. Forbidden execution is skipped; forbidden
delivery is suppressed. Background turns, including nested subagents and skills,
cannot invoke either cron tool. They use fresh synthetic context, no reply-chain
lookup, and do not save synthetic turns into ordinary chat history.

Generation and reporting are independent: final output (not reasoning) is saved
before Telegram sending, capped at 12000 runes plus an explicit truncation marker.
Ordinary text, older results, failures, and skipped runs keep their plain-text
report layout. `cron_tasks get` always returns the original bounded response in
`latest_result.text`, including any envelope and surrounding text; its fields
are unchanged. Only a successful run with rich enabled and a successful
`load_skill(name="rich-message")` in that same turn may save a rich delivery
marker, and only if its bounded original text still contains the complete,
nonempty first envelope selected by the generation-time resolver. Truncation
through the envelope or a different selected body keeps the plain layout.
Rich reports send task/run status and next schedule as plain metadata first,
then only the envelope's Markdown body via a separate `sendRichMessage` in the
original chat/topic/reply context (normally two messages; long metadata may
require multiple 1800-rune chunks). The saved format fixes the delivery layout:
later configuration changes cannot upgrade old results or downgrade a rich retry.
Any text outside the envelope remains available through `get` but is not sent
in the rich body. A damaged stored rich envelope fails before any report send;
it is never reclassified as plain during delivery.
Confirmed message IDs form one cursor across metadata chunks and the rich body;
failed delivery resumes only unsent parts, including on a `report_only` retry,
without rerunning generation. Rich API failure does not fall back to plain text.
Runner HTTP/image downloads and response-body reads use the execution context, so a
run deadline or shutdown cancels stalled network work. The model outcome is captured
and persisted before bounded trace finalization; slow telemetry cannot turn a
completed run into a retryable execution failure. Background trace records retain
counts, hashes, timings, and safe metadata but never prompt, model-output, or tool
payload previews. `delegate` and `cron_tasks` payloads are never content-captured,
including from ordinary interactive turns.

The outbox holds one result per task. Pending/claimed reports block replacement by
a new run. Automatic delivery is limited to three attempts, then each accepted
manual report-only retry grants one attempt, with a total cap of
`3 + max_manual_retries`. Reports expire 24 hours after result completion; expired
reports cannot be manually resent. Only the latest bounded result is retained.

Deleting a task removes its stored task/indexes and prevents late completion from
recreating it. It cannot undo remote side effects or a Telegram message already in
flight. Lease fencing protects persisted state, **not exactly-once external effects**.
A crash after Telegram accepts a chunk but before its receipt is persisted can
duplicate that chunk. A retry may repeat earlier external effects.
Older binaries cannot resume partially delivered rich reports using the new
layout marker; drain or safely handle these reports before rolling back.

`agent.Init` validates configuration and captures static policy before the existing
Redis list caches are loaded. `agent.StartCron` runs only after bot identity exists.
Interrupt/SIGTERM stops the bot; `agent.Close` cancels and waits for scheduler work
before closing MCP resources. Persistence retry is bounded and never regenerates
the model response. Live Telegram/LLM/Runtime smoke testing requires separately
authorized test credentials; automated tests use local HTTP fixtures.
The local scheduler reserves capacity before each claim; a rejected cooldown/rate
claim releases that capacity and polling continues to later candidates in the same
batch. Redis remains authoritative for shared cross-process concurrency.
Candidate discovery selects at most one eligible task per chat per poll and scans
past duplicates, so one chat's task backlog cannot fill the candidate batch and
hide other eligible chats. Background tool errors are also redacted before being
returned to the model, including through the production error-wrapper path.

## 部署与配置

The settings table above is under `agent_v3.cron` in `config.yaml`. This feature
adds tool arguments, **not** configuration keys; do not put `until` in config.yaml.

| 键名 / 路径 | 类型 | 默认值 | 不配置时的行为 | 需要挂载 / 环境变量 / TZ |
| --- | --- | --- | --- | --- |
| `delegate.until` | Optional tool string | Omitted | No cutoff; no automatic task cleanup | None |
| `cron_tasks` with `action: update`, `until` | Optional tool string | Omitted | Retain the saved cutoff; empty string clears it | None |
| `agent_v3.cron.timezone` | Configuration string | `Asia/Shanghai` | Capture this timezone on new tasks | No host `TZ` dependency; embedded tzdata is used |
| `agent_v3.cron.runner_agent` | Configuration string | Empty | Cron tools and scheduler are disabled | No new mount or environment variable |

No new config, mount, environment variable or background loop is required. Existing
tasks keep their captured timezone; changing the configuration does not reinterpret
their local schedules or stored absolute cutoff.

Execution must finish strictly before its lease's run deadline. Completion at or
after that instant is recorded as failed under the normal manual-retry policy,
even if context cancellation has not yet become observable. Deadline enforcement
requires no additional configuration, mounts, environment variables, TZ setting,
Redis schema change or data migration; existing stored outcomes are not rewritten.

Redis scope remains `<prefix>:cron:v1:<encoded-bot>:<encoded-platform>`. Stored JSON
gains an absolute `task.Until` timestamp (tool input/output uses lowercase `until`);
missing/zero values mean no cutoff. Task,
chat-task quota, dedup, due, retry and report indexes remain in that scope. The
reports sorted set also schedules final failed/exhausted reports for cleanup at
`FinishedAt + 24h`, without adding delivery attempts. Task keys have no new TTL;
cleanup removes indexes and quota atomically. Redis eviction/durability settings
still matter: a key without TTL is not protected from an `allkeys-*` eviction policy.

### 对已有部署的迁移步骤

1. Stop all old scheduler instances sharing the Redis coordinator; wait for or
   safely handle in-flight execution and reports. Do not mix old/new bot binaries.
2. Deploy all instances with until support before creating or updating bounded
   tasks. Existing records without until need no migration and retain their behavior.
3. Verify the dedicated runner and report poller are running. Failed final reports
   remain visible and occupy quota until their report window expires and polling
   can perform cleanup.

Old binaries ignore the additional JSON field: they may continue scheduling past
until or overwrite it. The field does not mechanically fence out an old consumer;
ordinary rolling deployment must not enable until while old schedulers remain.

### 回滚方式

Stop new until writes, then use the new version to finish/drain all bounded tasks
and their final reports. Confirm no bounded tasks, active executions, queued retries,
pending reports or partially delivered reports remain in the affected scopes.
Early cancellation, backup/archival or manual deletion requires the operator's
separate authorization and preservation of needed results. Do not strip until from
records as a migration. Stop all new instances before starting an older version.
If bounded work cannot be drained safely, keep the new version or leave cron
disabled; do not let an old scheduler consume those records.

Existing five-field records need no migration. Old bot binaries cannot process new
`@at`/`@every` records: do not mix old/new bots under the same Redis coordinator.
Rollback is not simply replacing the binary; first arrange compatible handling of
retained one-time/interval tasks, pending reports and retries without discarding
results or duplicating effects. No deployment, production-data migration, deletion
or rollback is performed by the tests or this feature.
