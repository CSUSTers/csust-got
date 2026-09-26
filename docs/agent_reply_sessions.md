# Reply-chain Agent Sessions

By default, an agent uses `context_mode: chat` (or omits `context_mode`) and keeps the existing chat-history behaviour. Set `context_mode: reply_chain` on one agent to build its model context only from the triggering Telegram reply chain.

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

The session follows reply ancestors stored during the normal 24-hour Telegram-message TTL. Consecutive messages from the same non-bot author join one user block only when IDs are adjacent and the gap is under 60 seconds. Missing parents, expired records, and conservative block breaks are marked as incomplete; unrelated replies and future/album sibling messages are not imported.

`message_context` remains a soft message-count limit for selected reply blocks. The rendered session text also has a hard UTF-8 byte budget of `agent_v3.context_cache.max_raw_tokens * 4` (24,000 bytes when defaults have not been initialized). Older complete blocks are removed first; the current trigger remains, with an omission marker if it must be shortened. File IDs, document/sticker hints, entity links, and retained image references stay in their own selected block. Images are encoded only after this text budget is decided and only when both existing image feature gates are enabled.

Reply-chain turns still save raw turns and summaries for normal chat compatibility, but they do not read those sources as reply-session history. The model receives the selected user blocks and bot blocks in reply order; the current trigger is the final user block. Group memory remains an independently loaded shared background context; it is neither reply history nor a permission source.

## Stable prefix and loop guidance

The stable agent-v3 prefix contains the execution protocol, optional soul,
runtime and skill rules, loop rules, and the available-skill catalog. It is
unchanged by reply-session history, memory, summaries, tool results, and loop
guidance. Per-round execution-limit guidance is appended only to the local model
input as a user-role `<agent_runtime_guidance>` message after prior tool results;
it is not persisted as a chat turn or a new permission source.

With `agent_v3.context_cache.enable: true`, local `cache_hit` means the stored
stable-prefix record reused the same hash and version. It does not prove a
provider key-value cache hit. `prompt_cache_key` is a provider hint derived from
that stable version, so tests verify its stable construction and Redis record,
not a live provider-cache hit. This design adds no observation state and does
not reorder registered tools.
