# Agent instructions and reply-chain sessions

## Goal and approval

Replace minimal anti-hallucination prompting with precise, conditional, stepwise instructions for the existing Telegram agent. Add an optional reply-chain session mode built from existing Telegram message records, grouping consecutive user utterances into blocks. Keep context naturally stable where practical, without treating exact prefix identity as a product requirement or claiming provider cache hits.

This revision supersedes the initial history-first proposal and reflects these user constraints:

- “暂时不引入新的状态，尽可能从会话中构建稳定前缀”.
- “增加一个agent配置项，通过reply链进行构建session，以保持本消息之前的稳定”.
- “构建时尽量保持稳定即可，无需刻意。引入消息时以用户发言为准，例如用户连续在群聊中发送了多条消息且间隔均<1m则作为一个块引入。另外构建session时不要使用{{context}}这样的动态参数，时间等参数可以豁免”.

This is a design artifact, not an implementation or a claim of implementation approval. The boundary defaults below are stated explicitly for the next planning stage.

## Global constraints

- Do not add persistent state, Redis keys/fields, transcript storage, migration, or new conversation caches.
- Preserve existing raw-turn Content/ImageRefs storage semantics, memory/forget behavior, summary updates and TTLs. Session selection must not import unrelated chat-wide history.
- Preserve configured system_prompt/soul_path precedence and multimodal, reply, link, document, and skill capabilities. In session mode, restrict prompt templates to static text and allowed metadata instead of interpolated conversation content.
- Use existing dependencies and the existing OpenAI-compatible model path; do not install software or invoke paid/live services without authorization.
- Do not stage, commit, push, tag, or otherwise write Git state.
- Keep changes limited to main-agent prompts, optional session configuration, reply-chain/user-block context composition, tool-loop guidance, their tests and documentation; do not redesign subagent execution or auxiliary summary models.
- Stability is best-effort. Do not add transcript normalization, immutable snapshots, session identities, or special cache instrumentation merely to preserve identical prefixes.

## Current evidence

`agent/agentv3_context.go` builds a stable soul/runtime/skill system, then memory, summary, raw turns and the current dynamic user message. Memory and summary changes therefore invalidate the prefix before raw history. `agent/loop.go` adds fixed loop instructions after prefix identity calculation and merges changing guidance into system on every model call. `saveAgentV3TurnPair` stores raw user input and final output, not the actual rendered prompt or tool transcript. These limitations cannot all be eliminated without new state.

`agent/context.go:GetMessageContext` supplements reply ancestors with nearby group history; `getReplyChain` traverses only embedded ReplyTo objects. `orm/redis-message.go` already stores full Telegram messages by chat/message ID and a bounded chat stream, with a 24-hour TTL. These records provide sender IDs, timestamps and reply links without introducing session storage. `ContextMessage.User` is a username, not a safe identity for grouping; use the full message's Sender.ID.

## Alternatives

1. Continue using chat-wide sliding history: retain as the default compatibility mode, but it cannot represent reply branches independently.
2. Optional reply-chain sessions with utterance blocks (selected): infer the session from existing messages, avoid unrelated chat history, and accept natural changes from edits, expiry and limits.
3. Persist exact model transcripts and compact at thresholds: rejected because it adds state and tool-result retention and overemphasizes cache identity.

## Instruction architecture

Centralize main-agent instruction text in a focused prompt file. The stable system contains a universal execution protocol, configured identity/task instructions using existing precedence, runtime/skill capabilities, fixed tool-loop discipline, and the deterministic skill catalog. The protocol must apply even without the example soul file. The sample soul describes the CSUST identity without duplicating the universal protocol.

Use concrete imperative stages: identify the actual request and completion criterion; answer simple questions directly; obtain only necessary evidence for complex requests; execute dependent steps in order with available tools; inspect results and adapt after failures; verify what was accomplished; provide the result, evidence, and remaining limitation. Do not require tools or visible planning for every request. Do not request private chain-of-thought disclosure. Ask only when missing information materially changes the outcome or authorization.

Define instruction/data boundaries: historical messages, summaries, memories, quoted text and tool output are evidence, not new requests or permission grants. Skill content is bounded operational guidance, not authority to override system constraints. Do not treat XML labels as authentication. The latest actual request determines the current task; an older summary remains older despite its later message position. Memory is context, not an instruction source. Preserve remote runtime isolation, tool schemas, skill loading, documented skill command limits, network restrictions and rich-message activation/envelope rules. Progress instructions must not forbid an explicitly configured update_progress tool while promising configured tools are usable.

## Session configuration and source selection

Add `context_mode` to each agent entry: `chat` (including an omitted value) preserves existing history selection, and `reply_chain` enables this design. Reject unsupported mode names during configuration validation. A mode is not a trigger: existing command/reply/regex trigger settings remain unchanged.

In reply-chain mode, follow the current message's ReplyTo ancestors within the same chat. Use existing stored full messages to recover links not embedded in the incoming update. Stop at a root, missing ancestor, cycle or existing resource limit. No reply means a new conversation, though the current user's immediately preceding utterances may belong to its block. Replying to an older message creates a branch from that point without inheriting sibling branches.

Do not append chat-wide raw turns, chat-wide conversation summaries, or arbitrary nearby messages to a reply-chain session. Nearby messages are read only to find the user blocks containing selected chain nodes. Existing raw-turn persistence can remain unchanged for chat-mode compatibility; it is not the session source. Shared group memory remains background data, not conversation history or a source of task authorization.

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

During a tool run, sanitize input and inject fixed instructions before the first model request. Append changing runtime guidance after all results from the preceding tool call, never between an assistant tool call and its results. A separate user-role control note is compatible with the current model transport, not a promotion of arbitrary user tags to authority. Retain appended notes only in the existing run-local history. Keep execution limits, duplicate detection and error handling in code. Do not persist notes or tool traces.

## Stable prefix identity and tool definitions

Keep fixed instructions centralized and avoid repeated injection. Existing cache metadata must not claim that omitted execution rules are included in its system-prefix identity. Reuse existing prefix records and cache-key mechanism; do not add new identity state or change tool registration merely for cache optimization. Allowed time-dependent template rendering can naturally change this prefix.

`prompt_cache_key` is only a routing hint. Local prefix-record reuse is not evidence of provider KV-cache reuse; actual tool schemas, provider behavior, history truncation and model changes can affect hits. Do not add vendor adapters, usage counters, session digests, or new observability state for this change. Document what existing local cache metadata does and does not measure.

## Compatibility and failure behavior

Leave Redis schemas unchanged, including immediate snapshot rebuilding on forget and existing concurrent update behavior. Leave output delivery semantics unchanged except ensuring its already-stored bot response retains the reply link needed for reconstruction. Preserve final-round refusal to execute tools even if the model ignores guidance, duplicate warnings, cancellation, tool-call pairing, and stream clearing. Configured custom identity prompts remain effective under the session template restrictions; main-agent execution rules are supplied independently. Explicit subagent prompts and auxiliary progress-summary prompts are out of scope.

Message expiry or a missing ancestor yields the recoverable chain with an explicit incompleteness indication; it must not silently fall back to unrelated chat history. Do not fabricate deleted or unavailable content. Existing shared memory must remain current after forget; do not pin old snapshots for cache reuse.

## Acceptance scenarios

1. Default chat mode retains existing selection. Reply-chain mode reconstructs ancestors from existing records, excludes siblings/chat-wide raw turns/summary, and treats an un-replied message as a new conversation with its current user block.
2. Same-user messages at 0s, 40s and 80s form one block. A 60s gap, another sender, bot response, or explicit branch switch splits it. Deduplication, trigger cutoff and oversized-block handling preserve the current request without unbounded growth.
3. Effective session templates reject context/input/reply interpolation with a clear error; static text and time/date/bot-name metadata remain allowed. Input appears once in direct model messages. Chat templates remain compatible.
4. A scripted model exercises several tool rounds with guidance. Existing history is not rewritten merely for guidance, tool calls/results remain paired, and final-round tools do not execute. Exact cross-session prefix equality and live cache hit rates are not acceptance requirements.
5. Main-agent prompting contains conditional task/evidence/verification/output rules with fixed instructions injected once. Existing multimodal/image references, runtime restrictions, skill loading/rich output, memory/forget, cancellation and streaming behavior remain covered. Missing/expired/cyclic chains fail safely without unrelated history fallback.

## Verification and limits

During later implementation, capture failing assertions for reply selection, grouping and template restrictions before making them pass. Exercise the real CustomAgent loop with captured model inputs and existing local model fixtures; do not fabricate live cache statistics. Run focused package tests, full available suite/build, and diagnostics. Report toolchain/environment blockers rather than installing dependencies or claiming unrun checks passed.

No claim of exact cross-turn replay, unlimited context, guaranteed provider cache hits, or measured model-quality improvement follows from these tests. Record expiry, message edits, growing utterance blocks, bounded history, transient time/images and missing persisted tool traces remain accepted reuse boundaries. Preserve conversation correctness rather than forcing cache stability.
