# Agent V3 Rich Messages Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow agent-v3 chat configs with `agent.rich: true` to send Telegram Bot API 10.1 Rich Messages while keeping the existing `format: md|html` text pipeline unchanged.

**Architecture:** Rich Message is an agent-v3-only delivery capability gated by per-chat `config.AgentConfig.Rich`, not by `ChatOutputFormatConfig.Format`. When enabled, agent-v3 exposes a built-in rich-message skill through `load_skill`; the model must call `load_skill(name="rich-message")` immediately before a final `<telegram_rich_message>` envelope. Streaming and final delivery use `Bot.Raw` with `sendRichMessage.rich_message.markdown`; after successful rich delivery the previous placeholder is deleted. Authorized rich output stores bot-derived visible text in history; unauthorized envelopes follow the ordinary text pipeline.

**Tech Stack:** Go 1.26, `gopkg.in/telebot.v3` v3.3.8 `Bot.Raw`, Telegram Bot API 10.1 Rich Messages, `github.com/stretchr/testify`, existing `chatv2` agent-v3 runtime and streaming code.

---

## Current Protocol

The model may answer normally, or output exactly one rich envelope:

```text
<telegram_rich_message># Title

**Body**
- item
</telegram_rich_message>
```

The tag inner is raw Telegram Rich Markdown. It is not JSON, not HTML, and not an `InputRichMessage` object. First slice does not support `mode` fields, `fallback_text`, block AST payloads, media uploads, or `sendRichMessageDraft`.

The parser ignores text outside the first rich tag pair. If the closing tag has not arrived yet, the content after the opening tag is treated as the current rich-markdown body so streaming can edit Telegram incrementally.

## Implemented Task Summary

1. Config gate
   - `config.AgentConfig.Rich bool` maps `agent.rich`.
   - `ChatConfigSingle.IsAgentV3RichEnabled()` requires agent-v3 and `agent.rich`.

2. Formatting split
   - `splitOutputWithReason()` factors existing native/`<think>` reasoning extraction.
   - Legacy `FormatOutputWithReason()` output remains unchanged for text paths.

3. Rich parser and raw Telegram wrappers
   - `parseTelegramRichMessageEnvelope()` extracts raw markdown inner text.
   - `deriveTelegramRichFallback()` converts rich markdown to approximate visible text for history/fallback.
   - `sendTelegramRichMessage()` and `editTelegramRichMessage()` call `Bot.Raw` through `telegramRawCaller` and unmarshal `result` into `tb.Message`.

4. Agent-v3 prefix contract
   - `agentV3RichMessageSkillContract(true)` is available only when `agent.rich` is enabled.
   - The prefix advertises rich-message as loadable; the full contract is returned by `load_skill`.
   - `buildAgentV3PrefixHash()` includes the loadable skill block hash to prevent cache contamination across rich gate changes.

5. Delivery integration
   - Non-streaming sends raw rich only when gated, valid, and immediately authorized by `load_skill`; otherwise output uses the ordinary formatting pipeline.
   - Streaming suppresses placeholder edits while a rich envelope is still being assembled.
   - Finalization performs final raw rich send, deletes the previous placeholder, and returns visible text for persistence.

## Guardrails

- `format: md|html` continues to control only normal Telegram text parse mode and fallback text formatting.
- Rich markdown is not escaped as MarkdownV2 or HTML before raw rich API calls.
- `agent.rich` plus immediately preceding `load_skill(name="rich-message")` gates raw rich sending. Without that tool call, rich envelopes are ordinary output.
- Redis and agent-v3 history store derived visible text for authorized rich output. Unauthorized rich envelopes are ordinary text and are persisted like any other ordinary response.
- `sendRichMessageDraft` is intentionally out of scope for this slice.

## Verification

Run before handoff:

```powershell
go test ./chatv2 -run "TestParseTelegramRichMessageEnvelope|TestShouldSuppressPartialRichEnvelope|TestSendTelegramRichMessageUsesRawMethodAndPayload|TestEditTelegramRichMessageUsesEditMessageTextWithRichMessage|TestTelegramRichRawFailureIsReturned|TestResolveTelegramRichDelivery|TestUpdateMessageEditsCompleteRichEnvelopeAsRichMarkdown|TestUpdateMessageSuppressesPartialRichEnvelope|TestAgentV3RichMessageRulesAreGated|TestBuildAgentV3StablePrefixIncludesRichContractOnlyWhenProvided|TestBuildAgentV3PrefixHashSeparatesRichGate"
go test ./config ./chatv2 ./util
go test ./...
go build ./...
```

Also check that stale JSON-contract terms do not appear in rich implementation files:

```powershell
rg -n 'fallback_text|telegramRichMode|errTelegramRichInvalidJSON|RichMessage\.HTML|"mode"|"html"' chatv2 -g '*.go'
```

Expected rich-message implementation files have no hits; ordinary legacy text-format tests may still mention `html` or `mode` for unrelated features.
