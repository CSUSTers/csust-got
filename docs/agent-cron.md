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
Prompts are data, not Go templates. Identical normalized cron/timezone/prompt for
the same creator and chat is deduplicated atomically. Creation returns `task_id`,
`version`, `cron`, `timezone`, `next_run_at`, and `deduplicated`.

`cron_tasks` accepts:

```json
{"action":"list","limit":20}
{"action":"get","task_id":"TASK_ID"}
{"action":"update","task_id":"TASK_ID","expected_version":1,"cron":"0 10 * * 1-5"}
{"action":"retry","task_id":"TASK_ID","expected_version":2,"run_id":"RUN_ID"}
{"action":"delete","task_id":"TASK_ID","expected_version":3}
```

List uses an optional cursor (default 20, maximum 50 entries). Get includes the
bounded latest result and report status. Queries are chat-scoped; **only the task
creator** may update/delete/retry, with a current `expected_version`. Update accepts
only cron and/or prompt and keeps the captured timezone. Active execution, queued
retry or pending/claimed report prevents update. There is no pause/disable action.
Prompt-only updates preserve the existing deadline (even if overdue), rather than
restarting a relative delay. A completed one-time task rejects prompt-only updates;
provide an explicit new `cron` to schedule it again. A terminal one-time task has
`next_run_at: null` in tool JSON, not a year-0001 timestamp.

Before retry, get the latest version and run ID, explain possible earlier side
effects, and obtain an explicit user request. A failed execution is queued for a
new run. A successful execution whose report failed returns `mode: report_only`
and only resends stored output. Both consume a version and a bounded retry budget;
stale/replayed requests fail. Failure messages identify the task/run and give this
conversational retry path; no inline Telegram callback is required.

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
| `@week <0-7> HH:MM` / `@weekly <0-7> HH:MM` | Weekly, e.g. `@weekly 1 09:00`; 0/7 is Sunday |
| `@every <duration>` | Fixed delay, e.g. `@every 90s` |

Times are 24-hour `HH:MM`, without seconds. `tomorro` is the exact accepted spelling;
`tomorrow`, weekday names, bare `@daily`, and other macros are not supported.
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
aliases canonicalize to five fields and retain cron's gap/fold behavior and dedup.

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

`@every` is **fixed-delay**, not fixed-rate or minute-step cron: its first deadline
is creation/rescheduling time plus the duration; subsequent deadlines are completion
or interrupted-lease recovery time plus the duration. Manual execution retry moves
the next deadline too; report-only retry does not. Poll interval, chat cooldown,
concurrency limits and report blocking still apply: `@every 30s` does not guarantee
starting every 30 seconds.

One-time tasks that were never claimed keep their original deadline across downtime
and run once when eligible. Success, failure, skip, and interrupted-lease recovery
all end ordinary scheduling: no next deadline or due-index entry remains. Results,
reports and eligible explicit retries remain available; retries do not create a new
ordinary occurrence. Reports say “无下一次计划（一次性已结束）”. Completed tasks remain
stored and **still count toward the chat quota** until explicitly deleted. There is
no automatic cleanup or automatic execution retry.

Before execution and delivery, the bot checks static and live Redis whitelist/block
lists, creator fake-ban, chat shutdown, and enabled source/runner agent filters.
Redis policy failure is fail-closed. Forbidden execution is skipped; forbidden
delivery is suppressed. Background turns, including nested subagents and skills,
cannot invoke either cron tool. They use fresh synthetic context, no reply-chain
lookup, and do not save synthetic turns into ordinary chat history.

Generation and reporting are independent: final visible text (not reasoning) is
saved before Telegram sending. Text is plain, capped at 12000 runes plus an explicit
truncation marker. Reports split into safe chunks; confirmed message IDs survive
failed delivery so subsequent attempts skip those chunks. Reports carry task/run
status and the next schedule. Telegram delivery failure never reruns generation.
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

## Compatibility and rollback

Existing five-field records need no migration. Old bot binaries cannot process new
`@at`/`@every` records: do not mix old/new bots under the same Redis coordinator.
Rollback is not simply replacing the binary; first arrange compatible handling of
retained one-time/interval tasks, pending reports and retries without discarding
results or duplicating effects. No deployment, production-data migration, deletion
or rollback is performed by the tests or this feature.
