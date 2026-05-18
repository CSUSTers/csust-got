# Telego Migration Design

## Goal and Non-Goals

Migrate the bot framework from `gopkg.in/telebot.v3` to `github.com/mymmrac/telego` and fully remove the telebot dependency from runtime code, tests, and `go.mod`.

This migration must not change bot behavior. Command semantics, message formatting, permission checks, rate limits, message deletion behavior, AI chat triggers, inline query behavior, media handling, Redis message persistence, and error handling must remain equivalent to the current telebot implementation.

The migration will not add features, redesign commands, restructure unrelated packages, change config shape, change Redis key formats, change AI prompt behavior, or introduce new retry, timeout, panic recovery, or shutdown semantics unless Telego requires a specific replacement. Any required behavioral difference must be documented and confirmed before implementation.

## Framework Version Background

As of 2026-05-18, this repository uses `gopkg.in/telebot.v3`. The latest stable version for that module path is `v3.3.8`, published by the Go module proxy with timestamp `2024-08-06T09:46:17Z`. That is about 650 days, or roughly 1 year and 9 months, before this migration design date.

Telebot's latest V3.3 GitHub release notes list support for Telegram Bot API 6.6 through 7.1. The `v3.3.8` source contains 7.1-era objects such as boosts, reactions, and giveaways, but it does not represent the Bot API 7.2+ surface as a maintained baseline. Therefore, for this repository's current dependency, the migration should treat Telebot v3 as a Bot API 7.1-era framework.

Telegram's official changelog lists Bot API 10.0 on 2026-05-08 as the current latest Bot API. Compared with Bot API 7.1, the current dependency is behind every official Bot API release from 7.2 through 10.0: 22 changelog entries in total. Important missing areas for implementation context include business account updates and `business_connection_id`, Stars and paid media, private chat topics and message drafts, checklists, managed bots, guest mode, live photos, newer poll media, newer chat administration permissions, and multiple message/reaction management APIs.

`gopkg.in/telebot.v4` currently has beta tags, including `v4.0.0-beta.8` from 2026-05-08, but that is a beta major version and is not the dependency used by this repository. It should not be treated as the current stable baseline for this migration unless the project explicitly decides to evaluate beta Telebot instead of migrating to Telego.

Telego `github.com/mymmrac/telego` latest is `v1.9.0`, published with timestamp `2026-05-13T22:12:22Z`. Its README declares supported Telegram Bot API `v10.0`, and the downloaded source contains Bot API 10.0-era types and methods such as guest-mode fields and `answerGuestQuery`. This makes Telego a suitable target for a current Bot API baseline.

Sources used for this background:

- Telegram official Bot API changelog: `https://core.telegram.org/bots/api-changelog`
- Telebot releases: `https://github.com/tucnak/telebot/releases`
- Telego releases: `https://github.com/mymmrac/telego/releases`
- Module metadata verified with `go list -m -json gopkg.in/telebot.v3@latest`, `go list -m -json gopkg.in/telebot.v4@latest`, and `go list -m -json github.com/mymmrac/telego@latest`

## Current Telebot Coupling

The current codebase depends on telebot beyond `main.go`. Telebot types and methods are used across command handlers, middleware, utility helpers, Redis message storage, inline mode, sticker download and conversion, voice sending, chat context reconstruction, AI streaming edits, and tests.

Important coupling points:

- `config.Config.Bot` stores `*telebot.Bot` globally.
- `util` sends, replies, edits, deletes, downloads files, checks admins, and checks member permissions through telebot APIs.
- `main.go` relies on telebot middleware chaining, endpoint constants, command registration, event registration, and `Context`.
- `entities.CommandTakeArgs` and `chatv2.extractInput` rely on telebot message shape, including `Message.Payload`.
- `chat/context.go`, `chatv2/memory.go`, `orm/redis-message.go`, and tests use telebot message JSON and field names.
- Complex media paths in `base/sticker.go`, `base/get_voice.go`, `chat`, and `chatv2` depend on telebot file, document, voice, photo, sticker, and send option types.

Because of this, replacing imports directly would risk behavior drift. The migration needs an explicit project-owned Telegram boundary.

## Chosen Architecture

Use Telego natively, with a small project-owned Telegram boundary package, tentatively `telegram/`.

The boundary package is not a telebot compatibility layer. It is the project's stable Telegram integration layer. It centralizes Telego-specific update extraction, command payload parsing, send option mapping, file handling, and bot API calls so that behavior differences are handled once and tested directly.

Core units:

- `telegram.Bot`: wraps `*telego.Bot`, stores bot identity such as ID and username, and exposes project-level methods for send, reply, edit, delete, file download, admin lookup, member lookup, restrict, forward, chat action, sticker set lookup, and inline query answer.
- `telegram.Context`: wraps `*telegohandler.Context` plus the current `telego.Update`, and exposes the project-level view needed by handlers: message, query, chat, sender, recipient, text, command payload, reply/send helpers, notify helpers, and bot access.
- `telegram.SendOptions`: project-level send options for parse mode, reply target, allow-without-reply behavior, link preview suppression, and reply markup or other existing options.
- `telegram` tests: verify that the wrapper maps Telego updates and params into the same behavior expected from the current telebot usage.

All business code should import Telego types directly only when it represents Telegram data, such as `telego.Message`, `telego.User`, `telego.Chat`, `telego.Document`, `telego.Sticker`, `telego.PhotoSize`, or `telego.MessageEntity`. High-frequency API calls and option construction should go through `telegram/`.

## Telego API Difference Review Requirements

For any API where telebot and Telego may implement the same Telegram Bot API method differently, implementation must check Telego source or official documentation before choosing behavior. If the behavior cannot be confirmed, implementation must stop and ask for confirmation with the known options.

Mandatory comparison points:

- Command payload parsing: telebot populates `Message.Payload` for registered commands. Telego message structs do not provide that field. The project must parse `/cmd`, `/cmd arg`, `/cmd@Bot`, `/cmd@Bot arg`, caption commands, and non-command text itself.
- Reply behavior: telebot `SendOptions{ReplyTo, AllowWithoutReply}` must map to Telego `ReplyParameters` without losing current reply and fallback behavior.
- Parse mode and escaping: `RawTgText`, MarkdownV2 escaping, HTML escaping, and default parse mode behavior must be preserved.
- Link previews: telebot `NoPreview` behavior must map to Telego link preview params.
- File upload and download: file ID, URL, disk file, reader upload, Telegram file lookup, and download stream behavior must be checked for sticker, document, voice, and image paths.
- Admin and member permissions: `AdminsOf`, `ChatMemberOf`, `CanRestrictMembers`, and restrict behavior must map to Telego chat member variants and permission fields.
- Inline query answers: `QueryResponse`, article results, parse mode, result text, empty response, and error propagation must remain equivalent.
- Chat actions: typing, choosing sticker, and uploading document actions must map to the same Telegram action strings.
- Message edit: current streaming placeholder edit behavior and fallback to sending a new reply must be preserved.
- Delete: message delete errors must continue to log and not change control flow where current code ignores deletion errors.
- Handler concurrency: Telego handlers process updates concurrently. Existing shared state and goroutines must be reviewed so this does not introduce new behavior.

The implementation plan must include a checklist for these items and mark each as confirmed by code, test, or documented Telego behavior.

## Handler and Middleware Design

`main.go` will initialize Telego with the existing token, proxy, API URL, long polling timeout, and bot identity logging. It will create an update channel through Telego long polling and pass updates to `telegohandler.BotHandler`.

The existing middleware order must be preserved:

```text
logger -> skip -> block -> fakeBan -> rate -> noSticker -> shutdown -> messageStore -> contentFilter -> byeWorld -> mc
```

Each middleware receives `telegram.Context` and returns the same control behavior as today:

- Return `nil` after stopping the chain when current telebot middleware stops silently.
- Return the handler error when current behavior forwards an error to the bot error handler.
- Continue to the next middleware when current behavior calls `next(ctx)`.

Handler registration must preserve current matching behavior:

- Static commands stay registered before the generic text/photo custom handler.
- Dynamic chat commands are registered from active chat config as today.
- `customHandler` retains the same internal order: decode command handling, regex triggers, reply-to-bot trigger, then silent return.
- Inline query handling remains independent from message-only restrictions.
- Media and service handlers remain equivalent to the current event handlers.

Telego uses predicate-ordered first-match routing, so the migration must make route ordering explicit and test it. In particular, tests must verify whether command messages should or should not reach `customHandler`, whether photo captions still trigger AI logic, and whether inline queries bypass message-only middleware checks.

## Type Migration Design

Primary type mapping:

- `telebot.Bot` -> `telego.Bot` through `telegram.Bot`
- `telebot.Context` -> `telegram.Context`
- `telebot.Message` -> `telego.Message`
- `telebot.User` -> `telego.User`
- `telebot.Chat` -> `telego.Chat`
- `telebot.Query` -> `telego.InlineQuery`
- `telebot.MessageEntity` -> `telego.MessageEntity`
- `telebot.File` -> `telego.File` and `telego.InputFile`
- `telebot.SendOptions` -> `telegram.SendOptions`, then Telego params

Important field changes:

- `Message.ID` -> `Message.MessageID`
- `Message.Sender` -> `Message.From`
- `Message.Chat *Chat` -> `Message.Chat Chat`
- `Message.ReplyTo` -> `Message.ReplyToMessage`
- `Message.AlbumID` -> `Message.MediaGroupID`
- `Message.Photo *Photo` -> `Message.Photo []PhotoSize`
- `Message.Payload` -> project-level command parser result
- `Message.Time()` -> conversion from `Message.Date`
- `Message.Private()` -> helper comparing `Message.Chat.Type` with `telego.ChatTypePrivate`

Because Telego's `Chat` is a value on `Message`, project helpers should avoid pointer nil checks that existed only because telebot used `*Chat` on messages. For update types without a message, `telegram.Context.Chat()` should return `nil` or an explicit optional pointer derived from the update branch.

Redis message persistence needs special care. The migration must either preserve the stored JSON contract where downstream code expects it, or update read/write code together with tests so stored messages remain usable. No Redis key names or retention semantics may change.

## Module Migration Order

1. Create `telegram/` boundary layer and tests without changing business behavior.
2. Migrate command parsing and entity reconstruction logic.
3. Migrate bot initialization, middleware, handler registration, and error handling in `main.go`.
4. Migrate simple command and restriction modules: `base/helper.go`, `base/hello.go`, `base/bye_world.go`, `restrict/`, and `util/gacha/`.
5. Migrate complex media and AI paths: `base/sticker.go`, `base/get_voice.go`, `chat/`, `chatv2/`, `inline/`, `store/`, and `orm/redis-message.go`.
6. Remove telebot from `go.mod` and tests, run `go mod tidy`, and verify no telebot references remain.

This order keeps the high-risk media and AI streaming code behind a tested boundary before it is migrated.

## Testing and Verification Plan

Focused tests:

- `telegram.Context` extraction for message, photo, inline query, service message, nil-message update, chat, sender, and recipient.
- Command parsing for `/cmd`, `/cmd arg`, `/cmd@Bot`, `/cmd@Bot arg`, caption command, empty args, and non-command text.
- Send option mapping for MarkdownV2, HTML, raw text, reply, allow-without-reply, no-preview, document, voice, file ID, file URL, disk file, and reader file.
- Middleware continue/stop behavior for block, fake ban, rate, no sticker, shutdown, bye world, and mc-dead.
- Handler registration behavior for static commands, dynamic chat commands, regex triggers, reply-to-bot triggers, photo captions, and inline queries.
- Existing chat context tests, chatv2 tests, util tests, and media helper tests migrated rather than deleted.

Final verification commands:

```bash
GOCACHE=/private/tmp/codex-gocache go test ./...
GOCACHE=/private/tmp/codex-gocache go build -o /private/tmp/csust-got-build .
git diff --check
rg "gopkg\\.in/telebot\\.v3|telebot\\." .
```

The final `rg` check must have no runtime or test dependency references. Historical documentation references are allowed only if they are explicitly about this migration and do not imply an active dependency.

## Open Questions and Confirmed Decisions

Confirmed:

- The final state must fully remove telebot.
- The migration should use Telego `v1.9.0`, which corresponds to Telegram Bot API 10.0, unless a newer compatible version is selected during implementation.
- The current `gopkg.in/telebot.v3` stable baseline is `v3.3.8`, published on 2024-08-06 and corresponding to a Bot API 7.1-era surface.
- A project-owned `telegram/` boundary layer is acceptable.
- The migration must not change bot behavior.
- Any ambiguous Telego-vs-telebot API behavior must be checked before implementation and confirmed if unclear.
- Implementation must wait until this design is reviewed, then an implementation plan is written and approved.

Open during implementation planning:

- Exact package name: `telegram` is preferred for clarity, but `tg` is shorter. The plan should choose one and use it consistently.
- Whether Redis-stored message JSON must remain byte-compatible or only read/write-compatible after migration. Behavior should default to read/write-compatible unless existing external consumers require byte compatibility.
- Whether final validation should include a live bot smoke test in addition to unit and build verification.
