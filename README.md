# csust-got

[![Go Report](https://goreportcard.com/badge/github.com/csusters/csust-got)](https://goreportcard.com/report/github.com/csusters/csust-got)
[![codebeat badge](https://codebeat.co/badges/4d134b7f-e345-4378-b00d-7ab2177b94bc)](https://codebeat.co/projects/github-com-csusters-csust-got-master)

![GitHub Workflow Status](https://img.shields.io/github/actions/workflow/status/CSUSTers/csust-got/test.yml?branch=master&label=Test%20%7C%20master)
![GitHub Workflow Status](https://img.shields.io/github/actions/workflow/status/CSUSTers/csust-got/test.yml?branch=dev&label=Test%20%7C%20dev)

![GitHub language count](https://img.shields.io/github/languages/count/csusters/csust-got)
![GitHub](https://img.shields.io/github/license/csusters/csust-got)
![GitHub code size](https://img.shields.io/github/languages/code-size/csusters/csust-got)
![GitHub repo size](https://img.shields.io/github/repo-size/csusters/csust-got)
![GitHub issues](https://img.shields.io/github/issues/csusters/csust-got)
![GitHub closed issues](https://img.shields.io/github/issues-closed/csusters/csust-got)

A modern Telegram bot for CSUST, developed in Go.

[English](README.md) | [中文](README_zh-CN.md)

## Features

- 🤖 AI Chat Conversations (supports multiple models)
- 🔍 Message Search (powered by MeiliSearch)
- 🎨 Stable Diffusion Image Generation
- 🎲 Gacha System
- 🎭 Entertainment Features
- 📊 Prometheus Monitoring
- 🔧 Flexible Configuration System
- 🎯 Regular Expression Triggers
- 🛡️ Comprehensive Permission Management
- 🔗 MCP (Model Context Protocol) Support

## System Requirements

- Go 1.24+
- Redis
- Docker & Docker Compose (recommended)

## Quick Deployment

### Using Docker Compose (Recommended)

You need to install Docker first.

Clone the project:

```bash
git clone git@github.com:CSUSTers/csust-got.git
cd csust-got
```

Then run with Docker Compose:

```bash
docker-compose up -d
```

### Build from Source

```bash
# Clone the project
git clone git@github.com:CSUSTers/csust-got.git
cd csust-got

# Install dependencies
make deps

# Build
make build

# Run
./got
```

## Upgrade

Pull the latest version:

```bash
docker-compose pull
docker-compose up -d
```

## Configuration

Please modify the configuration in `config.yaml`.

- `token`: Change to your bot token
- `redis.pass`: Change Redis password
- `requirepass` in `redis.conf`: Change Redis password (must match the above)

## Commands

### Basic Functions

``` text
say_hello - A simple greeting
hello_to_all - Greet everyone
recorder - <msg> Repeat messages
info - Get bot information
id - Get user ID (private chat)
cid - Get group ID
```

### Search Functions

``` text
google - <Key Words> Google search
bing - <Key Words> Bing search
bilibili - <Key Words> Search on Bilibili
github - <Key Words> Search on GitHub
search - <keyword> Search message history
search - -id <chat_id> <keyword> Search messages in specific group
search - -p <page> <keyword> Search with pagination
```

### AI Chat

``` text
chat - <text> Chat with AI
think - <text> Deep thinking mode
summary - Summarize replied content (reply to a message)
```

### Management Functions

``` text
ban_myself - Ban yourself for rand[40,120] seconds
ban - Ban command [Admin]
ban_soft - Soft ban [Admin]
fake_ban - [duration] Fake ban
fake_ban_myself - Fake ban yourself
kill - Fake kill
no_sticker - Enable traffic-saving mode
shutdown - Shutdown bot
boot - Boot up bot
```

### Entertainment Functions

``` text
hitokoto - [type:ab..kl] Random quotes
hitowuta - Random poems
hito_netease - NetEase style quotes
mc - Minecraft mini-game
reburn - Respawn (MC game)
gacha_setting - Set JSON gacha configuration
gacha - Draw cards according to your configuration
```

### Voice Related

``` text
getvoice - character=<character> gender=<sex> theme=<topic> type=<type> <text> 
```

### Stable Diffusion

``` text
sd - <prompt> Generate images
sdcfg - Configure SD server
sdcfg - set <key> <value> Set configuration
sdcfg - get <key> Get configuration
sdlast - Get last used prompt
```

### Utility Functions

``` text
forward - [msgID] Forward a historical message
sleep - Time to sleep
no_sleep - Don't sleep
run_after - <duration> <msg> Remind yourself to do something later
hoocoder - <text> Hoo encoding
decode - _[decoding]_[encoding] <text> Decode text
bye_world - [duration] Say goodbye to the world
hello_world - Say hello to the world  
iwant - f=<format> I want sticker
setiwant - f=<format> vf=<format> sf=<format> Set sticker format
```

## Tech Stack

- **Language**: Go 1.24+
- **Framework**: [telebot.v3](https://github.com/tucnak/telebot)
- **Database**: Redis
- **Search**: MeiliSearch
- **Monitoring**: Prometheus
- **AI**: OpenAI API Compatible Interface
- **Image Generation**: Stable Diffusion WebUI
- **Containerization**: Docker & Docker Compose

## Development

### Local Development

```bash
# Install dependencies
make deps

# Run tests
make test

# Build
make build

# Code check
golangci-lint run
```

## License

This project is licensed under the [MIT License](LICENSE).

---

**Note**: This project is for educational and communication purposes only.
