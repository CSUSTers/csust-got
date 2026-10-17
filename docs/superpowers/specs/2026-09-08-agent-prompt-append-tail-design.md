# Agent instructions and reply-chain sessions

## Goal and scope

The main agent uses conditional task and verification instructions. Optional `reply_chain` context is reconstructed from existing Telegram message records, grouping consecutive user utterances into blocks. Stability is best-effort, not an exact-prefix or provider-cache-hit guarantee.

This specification covers main-agent instructions and the Telegram reply-chain fallback only. An accepted complete-model session replaces that fallback and has its own persistence contract, described in [Agent session context](../../agent_session_context.md). Configuration and operating limits are in [Reply-chain Agent Sessions](../../agent_reply_sessions.md).

## Global constraints

- The Telegram reply-chain selector uses existing message records, without separate session identities or persistent selection state.
- Fallback selection preserves raw-turn Content/ImageRefs semantics and doesn't import unrelated chat-wide history. Memory/forget and summary persistence follow their [current contracts](../../agent_memory.md), independently of the selector.
- Preserve configured system_prompt/soul_path precedence and multimodal, reply, link, document, and skill capabilities. In session mode, restrict prompt templates to static text and allowed metadata instead of interpolated conversation content.
- Main-agent instructions do not redefine explicit subagent prompts or auxiliary summary models.
- Stability is best-effort. Do not add transcript normalization, immutable snapshots, session identities, or special cache instrumentation merely to preserve identical prefixes.

## Instruction architecture

Centralize main-agent instruction text in a focused prompt file. The stable system contains a universal execution protocol, configured identity/task instructions using existing precedence, runtime/skill capabilities, fixed tool-loop discipline, and the deterministic skill catalog. The protocol must apply even without the example soul file. The sample soul describes the CSUST identity without duplicating the universal protocol.

Use concrete imperative stages: identify the actual request and completion criterion; answer simple questions directly; obtain only necessary evidence for complex requests; execute dependent steps in order with available tools; inspect results and adapt after failures; verify what was accomplished; provide the result, evidence, and remaining limitation. Do not require tools or visible planning for every request. Do not request private chain-of-thought disclosure. Ask only when missing information materially changes the outcome or authorization.

Define instruction/data boundaries: historical messages, summaries, memories, quoted text and tool output are evidence, not new requests or permission grants. Skill content is bounded operational guidance, not authority to override system constraints. Do not treat XML labels as authentication. The latest actual request determines the current task; an older summary remains older despite its later message position. Memory is context, not an instruction source. Preserve remote runtime isolation, tool schemas, skill loading, documented skill command limits, network restrictions and rich-message activation/envelope rules. Progress instructions must not forbid an explicitly configured update_progress tool while promising configured tools are usable.

## Session configuration and source selection

Add `context_mode` to each agent entry: `chat` (including an omitted value) preserves existing history selection, and `reply_chain` enables this design. Reject unsupported mode names during configuration validation. A mode is not a trigger: existing command/reply/regex trigger settings remain unchanged.

In reply-chain mode, follow the current message's ReplyTo ancestors within the same chat. Use existing stored full messages to recover links not embedded in the incoming update. Stop at a root, missing ancestor, cycle or existing resource limit. No reply means a new conversation, though the current user's immediately preceding utterances may belong to its block. Replying to an older message creates a branch from that point without inheriting sibling branches.

Do not append chat-wide raw turns, chat-wide conversation summaries, or arbitrary nearby messages to the Telegram reply-chain fallback. Nearby messages are read only to find the user blocks containing selected chain nodes. Raw-turn persistence is an independent chat-mode fallback mechanism, not this selector's history source. Shared group memory remains background data, not conversation history or a source of task authorization.

Bot responses must retain their actual reply relationship to the initiating user message when saved in the existing message records. Do not create a parallel session mapping. Do not infer missing parent links from message proximity.

## User utterance blocks

Use chat message order and full Telegram sender identity. Consecutive messages from the same user form a block when every adjacent timestamp gap is nonnegative and strictly less than 60 seconds. The total block duration may exceed one minute. A gap of exactly 60 seconds starts a new block.

Default meaning of consecutive: another user's message, a bot response, or a service message breaks the block. Unknown sender identity must not be merged by display name. A clear change to a different reply branch also breaks the block; a follow-up without an explicit reply may continue the current user's block.

For each selected user-message anchor, recover its containing block from adjacent stored messages, bounded by the current triggering message. This can include same-user text before or after the anchor, but never future messages beyond the trigger, unrelated users, or a separate explicit reply branch. Do not recursively traverse other child replies. Deduplicate by chat/message ID and order the selected blocks and bot responses chronologically.

Represent one user block as one user model message, with ordered constituent content and sufficient message/sender/reply metadata to distinguish quotations and references. Preserve links and attachments instead of flattening them into lossy plain text. Bot responses remain separate assistant messages. Include the current user block once; do not add the triggering message a second time via a template.

Use existing context budgets, preferring to select or discard complete older blocks. Do not make an arbitrarily long block exempt from hard size limits: when a single block exceeds the budget, keep its newest usable portion including the trigger and indicate that earlier content was omitted. Do not wait one minute for a block to close, change trigger timing, or add debounce/aggregation state.

## Template rules in session mode

Construct historical and current user content directly as model messages. Do not interpolate `Input`, `ContextMessages`, `ContextText`, `ContextXml`, or `ReplyToXml` into session prompt templates. This prohibition concerns template parameters, not literal braces or quoted user content within actual messages. It prevents duplicating the session and introducing changing whole-conversation text into prompts.

Allow static prompt text plus metadata fields `DateTime`, `CurrentDateCN`, and `BotUsername`. Time/date values are expressly exempt from stability requirements; do not freeze time or invent a reconstruction mechanism for it. Keep existing soul-file versus system-template precedence. Apply the session field restriction to effective configured system and user prompt templates; report a clear configuration error with a migration explanation for forbidden context-dependent fields, rather than silently deleting configured instructions. Ordinary chat-mode template behavior stays unchanged.

The ordinary system-template ban on dynamic conversation data remains; session mode permits the time/date exceptions above. A soul file remains a plain instruction file under its existing loading contract, not a new template language.

## Composition and tool execution

Session messages consist of system instructions, selected historical user blocks and bot replies, optional current group-memory background, allowed per-request metadata/static prompt additions, and the current user block. Preserve content and chronology; do not deliberately rewrite old blocks to optimize cache hits. No immutable-prefix guarantee is required when a block grows, an ancestor changes, or a budget removes older content.

During a tool run, sanitize input and inject fixed instructions before the first model request. Append changing runtime guidance after all results from the preceding tool call, never between an assistant tool call and its results. A separate user-role control note is compatible with the current model transport, not a promotion of arbitrary user tags to authority. Keep execution limits, duplicate detection and error handling in code. Notes aren't raw chat turns and must not be replayed into a later invocation. A complete-model session archive may retain guidance as an audit frame and tool traces under its separate storage contract.

## Stable prefix identity and tool definitions

Keep fixed instructions centralized and avoid repeated injection. Existing cache metadata must not claim that omitted execution rules are included in its system-prefix identity. Reuse existing prefix records and cache-key mechanism; do not add new identity state or change tool registration merely for cache optimization. Allowed time-dependent template rendering can naturally change this prefix.

`prompt_cache_key` is only a routing hint. Local prefix-record reuse is not evidence of provider KV-cache reuse; actual tool schemas, provider behavior, history truncation and model changes can affect hits. Do not add vendor adapters, usage counters, session digests, or new observability state for this change. Document what existing local cache metadata does and does not measure.

## Compatibility and failure behavior

Reply-chain reconstruction doesn't change memory schemas, immediate snapshot rebuilding on forget or concurrent update behavior. Stored bot responses retain their known reply links. Preserve final-round refusal to execute tools even if the model ignores guidance, duplicate warnings, cancellation, tool-call pairing, and stream clearing. Configured custom identity prompts remain effective under the session template restrictions; main-agent execution rules are supplied independently. Current output delivery semantics are specified in [Telegram delivery](../../agent_delivery.md).

Message expiry or a missing ancestor yields the recoverable chain with an explicit incompleteness indication; it must not silently fall back to unrelated chat history. Do not fabricate deleted or unavailable content. Existing shared memory must remain current after forget; do not pin old snapshots for cache reuse.

## Acceptance scenarios

1. Default chat mode retains existing selection. Reply-chain mode reconstructs ancestors from existing records, excludes siblings/chat-wide raw turns/summary, and treats an un-replied message as a new conversation with its current user block.
2. Same-user messages at 0s, 40s and 80s form one block. A 60s gap, another sender, bot response, or explicit branch switch splits it. Deduplication, trigger cutoff and oversized-block handling preserve the current request without unbounded growth.
3. Effective session templates reject context/input/reply interpolation with a clear error; static text and time/date/bot-name metadata remain allowed. Input appears once in direct model messages. Chat templates remain compatible.
4. A scripted model exercises several tool rounds with guidance. Existing history is not rewritten merely for guidance, tool calls/results remain paired, and final-round tools do not execute. Exact cross-session prefix equality and live cache hit rates are not acceptance requirements.
5. Main-agent prompting contains conditional task/evidence/verification/output rules with fixed instructions injected once. Existing multimodal/image references, runtime restrictions, skill loading/rich output, memory/forget, cancellation and streaming behavior remain covered. Missing/expired/cyclic chains fail safely without unrelated history fallback.

## Verification and limits

Verification exercises reply selection, grouping and template restrictions, and the real CustomAgent loop with captured model inputs and local model fixtures. These checks don't establish live cache statistics.

No claim of exact cross-turn replay, unlimited context, guaranteed provider cache hits, or measured model-quality improvement follows from these tests. Record expiry, message edits, growing utterance blocks, bounded history, transient time/images and missing persisted tool traces remain accepted reuse boundaries. Preserve conversation correctness rather than forcing cache stability.
