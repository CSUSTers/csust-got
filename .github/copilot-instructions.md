This repo is a modern Telegram bot for CSUST, developed in Go. It includes various features such as AI chat conversations, message search, image generation, and more.

## Architecture Overview
- **Entrypoint**: `main.go` initializes all services and registers bot handlers.
- **Core Framework**: The bot is built using `gopkg.in/telebot.v3`. New commands and features are added by registering handlers with `bot.Handle()`.
- **Configuration**: Global configuration is loaded from `config.yaml` into structs defined in the `config/` directory. The main configuration object is `config.BotConfig`.
- **Package Structure**: The project is organized by feature into packages (e.g., `chat`, `sd`, `meili`, `orm`).
- **Asynchronous Tasks**: Features like message indexing (`meili/`) and image generation (`sd/`) use a queue system (`store/`) for asynchronous background processing.
- **Data Persistence**: The `orm/` package is a data access layer for Redis, used for storing state, user lists, and cached data. It is not a traditional SQL ORM.

## Development Guidelines
1.  **Idiomatic Go**: Follow Go best practices and idiomatic patterns.
2.  **Adding Commands**: To add a new command, define a handler function and register it in `main.go` using `bot.Handle("/yourcommand", YourHandler)`. Consider adding middleware for permissions if necessary.
3.  **Configuration**: If a new feature requires configuration, add a new section to `config.yaml` and a corresponding struct in the `config/` package.
4.  **Dependencies**: Use `make deps` to install dependencies.
5.  **Testing**: Use the `github.com/stretchr/testify` framework for unit tests.
6.  **Build & Lint**: Before committing, ensure the code compiles with `go build .`, ensure the code is formatted by running `make fmt`, and ensure all tests pass with `make test`.
7.  **Directory Structure**: Maintain the existing feature-based package structure. Ignore any files in the `dict/` directory.

## Pull Request Guidelines
1. Always create pull requests based on the `dev` branch, unless otherwise specified.
2. Always merge pull requests into the branch they were created from.

## Code Review Guidelines
1. Follow Go best practices and idiomatic patterns.
2. Do NOT report code style issues, such as missing comments or formatting issues.
