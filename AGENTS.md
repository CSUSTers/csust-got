This repo is a modern Telegram bot for CSUST built with Go 1.27+, featuring AI chat and comprehensive permission controls.

## Architecture Overview

### Core Components
- **Entry Point**: `main.go` - Initializes all services, registers handlers, and configures middleware chain
- **Bot Framework**: `gopkg.in/telebot.v3` - All commands registered via `bot.Handle()`
- **Configuration**: `config.yaml` → structs in `config/` → global `config.BotConfig`
- **Data Layer**: `orm/` - Redis-based persistence (NOT a SQL ORM); stores chat state, user lists, caches
- **Queue System**: `store/` - Background task processing (message deletion)
- **Feature Packages**: `agent/` (package `agentv3`), `restrict/`, `base/`, `inline/`

### Middleware Pipeline
All requests flow through this ordered chain (see `main.go:108-111`):
```go
loggerMiddleware → skipMiddleware → blockMiddleware → fakeBanMiddleware →
rateMiddleware → noStickerMiddleware → shutdownMiddleware →
messageStoreMiddleware → byeWorldMiddleware → mcMiddleware
```
**Key Insight**: Middleware order matters! `blockMiddleware` must run before permission checks.

### Handler Registration Patterns
1. **Static Commands**: `bot.Handle("/hello", base.Hello)` in `registerBaseHandler()`
2. **Dynamic Agent Commands**: Generated from top-level `agents:` entries in `config.yaml` by `registerAgentConfigHandler()`
3. **Regex Triggers**: Initialized via `initAgentRegexHandlers()` and matched in `customHandler()`
4. **Event Handlers**: `OnUserJoined`, `OnSticker`, `OnPhoto`, etc. in `registerEventHandler()`

### Command Scope Helpers (util/utils.go)
- `util.PrivateCommand(handler)` - Only in private chats
- `util.GroupCommand(handler)` - Only in group chats
- `util.GroupCommandCtx(handler)` - Group-only with context tracking

### Redis Key Patterns (orm/redis.go)
- `wrapKey(key)` - Adds global prefix
- `wrapKeyWithChat(key, chatID)` - `prefix:key:c<chatID>`
- `wrapKeyWithUser(key, userID)` - `prefix:key:u<userID>`
- `wrapKeyWithChatMember(key, chatID, userID)` - Combined scoping

### Async Task Queues (store/)
- `TaskQueue[T]` interface: `Push()`, `Cancel()`, `fetch()`, `process()`
- Example: `ByeWorldQueue` for delayed message deletion
- Background goroutines: `store.InitQueues(bot)`

### Agent Configuration (`config/agent.go`)
- Top-level `agents:` entries define models, templates, command/regex/reply triggers, filters, and output settings.
- Each enabled entry is compiled at startup by the `agentv3` package.
- Agent options support configured tools, per-agent `ToolServerConfig` MCP or MCPO servers, subagents, and skill configuration.
- Global `agent_v3` settings control the Runtime, memory, prompt cache, trace output, built-in skills, and SearXNG.

## Development Workflows

### Adding a New Command
1. Define handler: `func MyHandler(ctx telebot.Context) error { ... }`
2. Register in `main.go`: `bot.Handle("/mycommand", MyHandler)` or use scope helper
3. Add config if needed: struct in `config/`, read in `readConfig()`, validate in `checkConfig()`
4. Write tests: `myfeature/handler_test.go` using `github.com/stretchr/testify`

### Adding an Agent Configuration
1. Add an entry under top-level `agents:` in `config.yaml` with its model, prompts, and triggers.
2. Enable `agent_v3` and the entry's `agent` settings as required. Startup compiles enabled agents and `registerAgentConfigHandler()` registers command triggers.
3. Test a command trigger first, then enable regex or reply triggers.
4. Templates receive parsed message context with preserved link entities.

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

### AI Agent Module (`agent/`, package `agentv3`)
`main.go` imports the package as `agentv3 "csust-got/agent"`. There is one Agent v3 runtime path and no legacy chat fallback or global MCPO client.

#### Agent Request Flow
1. `handleAgentConfig()` selects a compiled enabled agent, then `agentv3.Chat()` runs `ProcessFilters()`.
2. The agent parses input and message context, preserving Telegram link entities and reply history, then renders configured templates and the Agent v3 stable prefix.
3. Eino creates the model and executes the compiled agent. Its tools can include fixed remote Runtime tools, configured MCP or MCPO `ToolServerConfig` entries, built-in tools, subagents, and skills.
4. Agent v3 manages chat-scoped memory, prompt-cache state, trace data, and built-in SearXNG guidance. Runtime tools call the configured remote Runtime rather than a global MCPO service.
5. `StreamToTelegram()` delivers streamed or final responses with the configured Markdown or HTML format and optional rich output.

#### MCP and MCPO
MCP and MCPO servers are configured per agent, subagent, or skill with `ToolServerConfig`. `McpManager` builds those tool connections during Agent v3 startup and closes them with the agent package. MCPO is one supported server type, not a global configuration or startup client.

#### Extending the Agent
1. Add filters in `agent/filter.go` and register their configuration handling there.
2. Update Agent v3 context/template construction in `agent/agentv3_context.go` when a new prompt field is necessary.
3. Update `agent/format.go`, `agent/streaming.go`, or `agent/rich_message.go` for output behavior.
4. Add an MCP or MCPO server through the relevant agent, subagent, or skill `ToolServerConfig` entry.

## Pull Request Guidelines
1. **Base branch**: Always create PRs against `dev` (not `master`)
2. **Pre-commit**: Run `make build && make fmt && make test`
3. **Code review**: No style complaints (gofmt/golangci-lint handles it)

## Files to Ignore
- `dict/` - Dictionary files, not part of core logic
