# Reply-chain Agent Sessions

By default, an agent uses `context_mode: chat` (or omits `context_mode`) and keeps the existing chat-history behaviour. Set `context_mode: reply_chain` on one agent to build its fallback model context only from the triggering Telegram reply chain.

This page describes the Telegram reply-chain context source used when a complete-model session isn't loaded. A restored complete session includes tool calls and results, not just visible Telegram messages, and replaces this fallback history. See [Agent session context](agent_session_context.md) for configuration, idle TTL, sensitive-data and shared-volume requirements.

```yaml
agents:
  - name: reply-assistant
    context_mode: reply_chain
    message_context: 10
    system_prompt: "You are a concise CSUST assistant. Today is {{ .CurrentDateCN }}."
    prompt_template: "Current date: {{ .CurrentDateCN }}; bot: {{ .BotUsername }}"
    # Keep the existing model, agent, triggers, filters, and format settings.
```

`context_mode` is not a trigger setting. Accepted values are `chat`, `reply_chain`, and the omitted default; other values stop startup validation. `soul_path` still takes precedence over `system_prompt` and is always loaded as plain text.

## Template migration

In `reply_chain` mode, `system_prompt` (when it is effective) and `prompt_template` may use only `DateTime`, `CurrentDateCN`, and `BotUsername`. Remove `Input`, `ContextMessages`, `ContextText`, `ContextXml`, and `ReplyToXml` interpolation rather than removing the real user message: selected reply-session messages are supplied directly to the model. Use `context_mode: chat` when a template genuinely needs the old chat interpolation surface.

## Session boundaries and limits

The limits in this section apply to the Telegram reply-chain fallback, not to a successfully restored complete-model session.

The session follows reply ancestors stored during the normal 24-hour Telegram-message TTL. Consecutive messages from the same non-bot author join one user block only when IDs are adjacent and the gap is under 60 seconds. Missing parents, expired records, and conservative block breaks are marked as incomplete; unrelated replies and future/album sibling messages are not imported.

`message_context` remains a soft message-count limit for selected reply blocks. The rendered session text also has a hard UTF-8 byte budget of `agent_v3.context_cache.max_raw_tokens * 4` (24,000 bytes when defaults have not been initialized). Older complete blocks are removed first; the current trigger remains, with an omission marker if it must be shortened. File IDs, document/sticker hints, entity links, and retained image references stay in their own selected block. Images are encoded only after this text budget is decided and only when both existing image feature gates are enabled.

Reply-chain turns still save raw turns and summaries for normal chat compatibility, but they do not read those sources as reply-session history. The model receives the selected user blocks and bot blocks in reply order; the current trigger is the final user block. Group memory remains an independently loaded shared background context; it is neither reply history nor a permission source.

## Continuing a complete-model session

Per-agent `session.save_context` defaults to true and `session.load_context` to false. Only an invocation selected by the actual reply branch forces both to true, even when explicitly disabled. A command or regex match that also replies to a message is still non-reply, including when one trigger entry configures several trigger types. These switches don't disable existing Telegram-message storage, raw turns, summaries, memory or traces.

Reply selection looks up the exact replied bot message within the same Redis prefix/bot/platform/chat scope. It allows different members and different agents in that chat, subject to the current filters and whitelist. Replying to an older answer forks from that answer, without importing sibling branches. Cross-agent replies reuse conversation history with the **current agent's** newly built system/stable prefix, memory and tool permissions; old system frames are archived but aren't replayed. A missing reply node never falls back to the latest node. Non-reply `load_context: true` selects the same agent's latest successfully committed node by commit order; `load_context: false` uses the existing context source and starts a **new DAG** if saving is enabled.

A complete session hit replaces the old Telegram reply-chain/raw-turn/summary history rather than appending it twice. Missing, damaged, unreadable or unsupported archives, broken ancestor chains and Redis failures fall back to the entire existing `context_mode` path, never a partially restored history. Only a successfully and completely loaded parent may be linked; failed loads start a new root when saving, using the actual fallback baseline. Existing fallback errors remain errors, and a Redis outage can also prevent saving the new root.

Only a complete top-level model turn whose final answer was actually delivered to Telegram successfully is saved. The archive preserves tool calls, matching results, reasoning and multimodal history in one incremental JSONL file per turn. A placeholder or partial stream isn't enough; cancelled turns, unfinished tool chains and failed final sends don't become reply targets.

Delegate/subagent calls **don't join interactive sessions**. Their internal executions don't load or save DAGs, establish session leases, or inherit the parent session's top-level recorder. Top-level delegate tool requests and returned results are still saved as ordinary top-level tool messages, without expanding the child's internal history. Background cron runs don't join interactive sessions either.

Global `agent_v3.session.directory` defaults to `data/agent-sessions`, and `ttl` defaults to `24h`. Nonempty TTL values must be positive Go duration strings such as `"24h"` or `"1h30m"`, not `"1d"`; malformed, zero, negative and overflowing values fail configuration checks. TTL applies to the **whole idle DAG**, not each node or Redis `EXPIRE`. Successful complete loads and successful commits refresh its activity; failed loads don't. Expired active DAGs are collected only daily at **02:00 in the process's local timezone**, independently of the cron runner/timezone. Startup and short recovery timers only resume registered intent/deleting work. TTL is an eligibility threshold, not an exact deletion deadline.

Sessions aren't isolated by member: group members can continue shared history that contains sensitive tool results or media. Restrict archive and backup access, use `0600` files or equivalent Windows ACLs, and mount the same sharedVolume with working cross-process locks on all instances sharing a Redis scope. Keep Redis metadata backed up too. There's no guarantee of automatic disk reconstruction after Redis loss, no general orphan scan, and unfamiliar files are retained. See the [deployment requirements](agent_session_context.md#数据敏感性与部署约束) before enabling this in a group.

## Stable prefix and loop guidance

The stable agent-v3 prefix contains the execution protocol, optional soul,
runtime and skill rules, loop rules, and the available-skill catalog. It is
unchanged by reply-session history, memory, summaries, tool results, and loop
guidance. Per-round execution-limit guidance is appended only to the local model
input as a user-role `<agent_runtime_guidance>` message after prior tool results;
it is not persisted as a chat turn or a new permission source. The complete-model archive may retain it as an audit frame, but must not replay old execution guidance into a later invocation.

With `agent_v3.context_cache.enable: true`, local `cache_hit` means the stored
stable-prefix record reused the same hash and version. It does not prove a
provider key-value cache hit. `prompt_cache_key` is a provider hint derived from
that stable version. Don't interpret the local record as evidence of a live
provider-cache hit.

Author's note: This page is for bot maintainers choosing reply-chain fallback; check templates and shared-history access before deploying it.
