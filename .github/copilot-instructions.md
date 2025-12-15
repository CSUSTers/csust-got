This repo is a modern Telegram bot for CSUST built with Go 1.25+, featuring AI chat, message search (MeiliSearch), image generation (Stable Diffusion), gacha systems, and comprehensive permission controls.

## Architecture Overview

### Core Components
- **Entry Point**: `main.go` - Initializes all services, registers handlers, and configures middleware chain
- **Bot Framework**: `gopkg.in/telebot.v3` - All commands registered via `bot.Handle()`
- **Configuration**: `config.yaml` → structs in `config/` → global `config.BotConfig`
- **Data Layer**: `orm/` - Redis-based persistence (NOT a SQL ORM); stores chat state, user lists, caches
- **Queue System**: `store/` - Background task processing (message deletion, SD generation)
- **Feature Packages**: `chat/`, `sd/`, `meili/`, `restrict/`, `base/`, `inline/`

### Middleware Pipeline
All requests flow through this ordered chain (see `main.go:116-119`):
```go
loggerMiddleware → skipMiddleware → blockMiddleware → fakeBanMiddleware →
rateMiddleware → noStickerMiddleware → shutdownMiddleware →
messagesCollectionMiddleware → messageStoreMiddleware → contentFilterMiddleware →
byeWorldMiddleware → mcMiddleware
```
**Key Insight**: Middleware order matters! `blockMiddleware` must run before permission checks.

### Handler Registration Patterns
1. **Static Commands**: `bot.Handle("/hello", base.Hello)` in `registerBaseHandler()`
2. **Dynamic Chat Commands**: Generated from `config.yaml` chat configs in `registerChatConfigHandler()`
3. **Regex Triggers**: Initialized via `initChatRegexHandlers()` and matched in `customHandler()`
4. **Event Handlers**: `OnUserJoined`, `OnSticker`, `OnPhoto`, etc. in `registerEventHandler()`

### Command Scope Helpers (util/utils.go)
- `util.PrivateCommand(handler)` - Only in private chats
- `util.GroupCommand(handler)` - Only in group chats
- `util.GroupCommandCtx(handler)` - Group-only with context tracking
- `whiteMiddleware` - Whitelist enforcement (only for sensitive commands like `/sd`)

### Redis Key Patterns (orm/redis.go)
- `wrapKey(key)` - Adds global prefix
- `wrapKeyWithChat(key, chatID)` - `prefix:key:c<chatID>`
- `wrapKeyWithUser(key, userID)` - `prefix:key:u<userID>`
- `wrapKeyWithChatMember(key, chatID, userID)` - Combined scoping

### Async Task Queues (store/)
- `TaskQueue[T]` interface: `Push()`, `Cancel()`, `fetch()`, `process()`
- Example: `ByeWorldQueue` for delayed message deletion
- Background goroutines: `go sd.Process()`, `store.InitQueues(bot)`

### Chat Config System (config/chat.go)
- Multi-model AI support with templates (Go `text/template`)
- Triggers: command, regex, reply-to-bot, gacha probability
- Output formats: markdown/html with configurable quote/collapse styles
- Streaming typewriter effect with rate-limited edits

## Development Workflows

### Adding a New Command
1. Define handler: `func MyHandler(ctx telebot.Context) error { ... }`
2. Register in `main.go`: `bot.Handle("/mycommand", MyHandler)` or use scope helper
3. Add config if needed: struct in `config/`, read in `readConfig()`, validate in `checkConfig()`
4. Write tests: `myfeature/handler_test.go` using `github.com/stretchr/testify`

### Adding a New Chat Configuration
1. Add entry to `config.yaml` under `chats:` with model, triggers, prompts
2. Restart bot - `registerChatConfigHandler()` auto-registers command triggers
3. Test with command trigger first, then enable regex/reply/gacha triggers
4. Use `{{.ContextXml}}` in templates for conversation history with preserved URLs

### Build & Test Commands
```bash
make deps      # Download dependencies
make build     # Build executable → ./got
make test      # Run tests with race detector
make fmt       # Format with gofmt + golangci-lint
make deploy    # Build with version info from git
make run       # Deploy + execute
```

### Testing Patterns
- Use `require.*` from testify for assertions
- Table-driven tests: `tests := []struct { args, want }{ ... }`
- See `base/encode_test.go`, `inline/*_test.go` for examples

## Project-Specific Conventions

### Error Handling
- Use `util.SendMessageWithError()` / `util.SendReplyWithError()` for Telegram sends
- Log errors via `log.Error()` from `csust-got/log` (zap wrapper)
- Middleware: return `nil` to halt chain silently, return `error` to trigger `OnError` handler

### Message Utilities (util/utils.go)
- `SendMessage()` / `SendReply()` - Auto-log errors, return message
- `DeleteMessage()` - Safe deletion with error logging
- `GetName(user)` - Format full name from Telegram user
- `CanRestrictMembers(chat, user)` - Check admin permissions

### Config Loading (config/config.go)
- `InitConfig("config.yaml", "BOT")` - Loads file + env vars with `BOT_*` prefix
- Viper merges: `config.yaml` < `custom.yaml` < env vars
- Special lists: `orm.LoadWhiteList()`, `orm.LoadBlockList()` from Redis

### Code Style Notes
- **Do NOT** add package-level comments or excess inline comments (CI enforced)
- Prefer `zap.String()`, `zap.Int64()` for structured logging
- Use `lo` (samber/lo) for functional patterns: `lo.Map`, `lo.Filter`, `lo.FlatMap`

## Key Integration Points

### AI Chat Module (chat/)
The chat module is the core AI conversation system with MCP (Model Context Protocol) support via **mcpo provider**.

#### Chat Request Flow (chat/chat.go)
1. **Filter Check**: `ProcessFilters()` validates whitelist/permissions → return `FilterDeny` stops processing
2. **Context Extraction**: `GetMessageContext()` retrieves message history with entities (preserves URLs in links)
3. **Template Rendering**: 
   - `promptData` struct with: `DateTime`, `Input`, `ContextMessages`, `ContextXml`, `ReplyToXml`, `BotUsername`
   - `SystemPromptTemplate.Execute(data)` → system prompt
   - `PromptTemplate.Execute(data)` → user prompt
4. **Image Processing** (if enabled): 
   - Auto-resize via `FeatureSetting.ImageResize()` (default 512x512, keeps ratio)
   - JPEG encode + base64 + prepend `data:image/jpeg;base64,`
   - Append as `MultiContent` with `ChatMessagePartTypeImageURL`
5. **MCP Tool Injection**: If `UseMcpo && config.McpoServer.Enable` → `request.Tools = mcpo.GetToolSet("")`
6. **Streaming Response**: `CreateChatCompletionStream()` → `streamProcessor.process()`
7. **Output Formatting**: `formatOutput()` extracts `<think>` reason tags, applies format config

#### MCP Integration (chat/mcpo.go)
**mcpo** is the MCP provider that exposes tools to AI models:
- **Initialization**: `InitMcpoClient()` in `main.go` → fetches OpenAPI specs from `config.McpoServer.Url`
- **Tool Discovery**: 
  - GET `/{tool}/openapi.json` → parse OpenAPI 3.1 spec
  - Convert paths to OpenAI function definitions: `POST /path` → `toolName_path`
  - Store in `mcpTools` map, group by tool in `toolSets`
- **Tool Execution**: 
  - AI returns `ToolCall` with function name + JSON args
  - `McpoTool.Call(ctx, param)` → POST to tool URL with JSON body
  - Add `Authorization: Bearer {ApiKey}` if configured
  - Return result to AI for next completion
- **Tool Sets**: Use `GetToolSet(setName)` to filter tools (e.g., `"mcpo_weather"` for weather tools only)

#### Streaming Output (chat/streaming.go)
- **streamProcessor**: Manages real-time message updates with rate limiting
- **Ticker Pattern**: 
  - `startStreamingTicker()` → goroutine sends updates every `EditInterval` (default 1s)
  - `updateStreamingMessage()` → finds last sentence delimiter, sends partial text
  - Stops at `<-sp.done` channel signal
- **Tool Call Handling**: 
  - Accumulates `ToolCall` chunks in `currentToolCallsChunks`
  - Merges chunks by index, executes when complete
  - Appends tool results to `messages`, retries completion
- **Final Output**: `formatOutput()` applies markdown/html escaping + quote/collapse/block styles

#### Message Context (chat/context.go)
- **Entity Preservation**: `getMessageTextWithEntities()` reconstructs URLs from Telegram entities
  - Problem: `msg.Text` only has visible text, loses URLs in `[title](url)` links
  - Solution: Parse `msg.Entities` (offset+length+type+url) and reconstruct markdown/html
- **Context Formatting**:
  - `FormatContextMessages()` → plain text with `User: message\n`
  - `FormatContextMessagesWithXml()` → XML tags with message IDs and reply chains
  - Thread reconstruction: Follows `ReplyTo` chain to build conversation tree

#### Filters (chat/filter.go)
- **Filter Interface**: `ProcessIncoming()` → `FilterAllow` or `FilterDeny`
- **Built-in Filters**:
  - `whitelistFilter`: Check chat ID or sender ID against config list
- **Execution**: Runs in order defined in `config.Filters.Filters`, first deny stops chain
- **Extension Points**: `ProcessOutgoing()` modifies response, `ProcessPromptData()` alters template data

#### Adding New Chat Features
1. **New Filter Type**: Implement `Filter` interface in `filter.go`, add case in `createFilter()`
2. **New Template Variable**: Add field to `promptData` struct, populate in `Chat()` function
3. **New Output Format**: Add case in `formatText()` with markdown/html escaping logic
4. **New MCP Tool**: Deploy OpenAPI-compliant HTTP server, add to `mcpo_server.tools` list

### MeiliSearch Indexing (meili/)
1. Middleware: `messageStoreMiddleware` enqueues messages
2. Background: Queue processor pushes to MeiliSearch
3. Search: `/search [-id chatID] [-p page] keyword` with pagination

### Stable Diffusion (sd/)
1. Queue: `ch <- context` (buffered, size 10)
2. Worker: `go sd.Process()` consumes queue
3. HTTP/3: Custom `mixRoundTripper` tries QUIC first, falls back to TCP
4. Rate limiting: `busyUser` map tracks per-user concurrency

## Pull Request Guidelines
1. **Base branch**: Always create PRs against `dev` (not `master`)
2. **Pre-commit**: Run `make build && make fmt && make test`
3. **Code review**: No style complaints (gofmt/golangci-lint handles it)

## Files to Ignore
- `dict/` - Dictionary files, not part of core logic
