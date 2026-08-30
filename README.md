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
- 🎲 Gacha System
- 🎭 Entertainment Features
- 🔧 Flexible Configuration System
- 🎯 Regular Expression Triggers
- 🛡️ Comprehensive Permission Management
- 🔗 MCP (Model Context Protocol) Support

## System Requirements

- Go 1.26+
- Rust 1.95+ (required only to build or verify the Agent Runtime from source)
- Redis
- Docker Engine with Docker Compose v2 (recommended)

## Quick Deployment

### Using Docker Compose (Recommended)

Install Docker Engine with Docker Compose v2 first.

Clone the project:

```bash
git clone git@github.com:CSUSTers/csust-got.git
cd csust-got
```

Before starting the checked-in Compose deployment, set these required base inputs in the deployment environment:

`AGENT_RUNTIME_TOKEN`, `AGENT_RUNTIME_CGROUP_PARENT`, `AGENT_RUNTIME_WORKSPACE_MAX_BYTES`, `AGENT_RUNTIME_WORKSPACE_FS_MAX_BYTES`, `AGENT_RUNTIME_LOG_FS_MAX_BYTES`, `AGENT_RUNTIME_WORKSPACE_HOST_ROOT`, `AGENT_RUNTIME_LOG_HOST_ROOT`, and `AGENT_RUNTIME_CGROUP_HOST_ROOT`.

The host aggregate/delegated cgroup and the bounded workspace and Runtime-log mount roots must already exist. Compose does not create them. Validate the host before starting the stack:

```bash
scripts/validate-agent-runtime-host.sh
```

Then start the base deployment. Fetch remains disabled:

```bash
docker compose up -d
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

For direct source verification, use a supported Linux builder. The production Agent Runtime build is Linux-only:

```bash
go build ./...
cargo build --manifest-path agent-runtime/Cargo.toml --locked --release --bins
```

## Upgrade

The checked-in Compose files build the Runtime from source, and build the Broker from source when the Fetch overlay is enabled. Update the source checkout first, then rebuild before starting the base deployment:

```bash
git pull --ff-only
docker compose build --pull agent-runtime
docker compose up -d
```

For a controlled-Fetch deployment, rebuild both source-built services with the overlay:

```bash
git pull --ff-only
docker compose --profile agent-fetch -f docker-compose.yml -f docker-compose.fetch.yml build --pull agent-runtime agent-fetch-broker
docker compose --profile agent-fetch -f docker-compose.yml -f docker-compose.fetch.yml up -d
```

`docker compose pull` does not update a source-built Runtime or Broker. For an operator-maintained image deployment, replace the deployment-specific `build:` entries with matching Bot, Runtime, and, when Fetch is enabled, Broker image tags. Pull those matching image tags and then start the same deployment:

```bash
docker compose --profile agent-fetch -f docker-compose.yml -f docker-compose.fetch.yml pull
docker compose --profile agent-fetch -f docker-compose.yml -f docker-compose.fetch.yml up -d
```

## Configuration

Please modify the configuration in `config.yaml`.

- `token`: Change to your bot token
- `redis.pass`: Change Redis password
- `requirepass` in `redis.conf`: Change Redis password (must match the above)

## Agent v3 Runtime and Controlled Fetch

The repository defaults are `agent_v3.enable: false` and `agent_v3.runtime.fetch_enabled: false`. To expose Agent v3 CLI Fetch, explicitly enable Agent v3, its Runtime, and its Fetch gate:

```yaml
agent_v3:
  enable: true
  runtime:
    enable: true
    fetch_enabled: true
```

These bot configuration gates do not, by themselves, activate production egress. The base `docker-compose.yml` keeps Fetch disabled. Controlled Fetch requires the `docker-compose.fetch.yml` overlay and its `agent-fetch` profile. Keep it off unless the operator sets `AGENT_FETCH_ENABLE=true` and supplies `AGENT_FETCH_POLICY_VERSION`, `AGENT_FETCH_EXTRA_DENY_CIDRS`, `AGENT_FETCH_DNS_SERVERS`, `AGENT_FETCH_AUDIT_FS_MAX_BYTES`, `AGENT_FETCH_AUDIT_HOST_ROOT`, and `AGENT_FETCH_HMAC_SECRET_FILE`.

The host aggregate/delegated cgroup and all bounded mount roots, including the Fetch audit root, must already exist and pass `scripts/validate-agent-runtime-host.sh`. Neither Compose nor the validator creates host paths or migrates data. Start controlled Fetch only with the overlay and profile:

```bash
docker compose --profile agent-fetch -f docker-compose.yml -f docker-compose.fetch.yml up -d
```

Model and MCP tools are direct model tool calls using registered schemas. Runtime Fetch instead runs `/usr/local/bin/fetch` inside the Bash environment through the `bash` tool, conceptually `bash(command="fetch GET ...")`. An MCP/MCPO tool named `fetch` is a separate capability and does not pass through the Runtime Egress Broker. If the Runtime Broker must be the only web-egress policy boundary, remove or disable fetch-like MCP tools for that Agent.

Shell direct IPv4 and IPv6 access is denied; the Bash CLI reaches the Runtime proxy through an inherited Unix FD, and the Broker alone has public egress, enforcing SSRF, DNS, redirect, budget, and audit controls. MCP fetch does not use this path.

PRoot creates and executes a temporary loader, so the Runtime `/tmp` must be executable. The base Compose deployment supplies `/tmp:rw,exec,nosuid,nodev,size=64m,mode=1777` for this deployment-only compatibility requirement; it does not add a Runtime readiness gate.

Before enabling Fetch in production, run these recommended deployment checks on the target native Linux host:

```bash
bash scripts/validate-agent-runtime-host.sh
bash scripts/test-agent-runtime-compose.sh
bash scripts/agent-runtime-attack-matrix.sh
```

The validator, Compose/static test, and attack matrix are deployment checks, not Runtime gates: the Runtime does not dynamically consume or gate on their receipt. Each command should exit 0, the attack matrix should report `fail=0 skipped=0`, and cleanup should leave zero residue. The host validator accepts equivalent nftables reject rendering such as `iifname "br-agent-fetch" reject with icmp port-unreachable`, while still requiring the exact bridge match, required deny sets, hook/priority/policy, and a reject verdict immediately after the expected match. Do not compensate for PRoot issues by using `privileged`, Docker `seccomp=unconfined`, AppArmor unconfined, or `SYS_PTRACE` unless that risk is separately accepted. A target-native Linux production receipt is still pending, so do not enable production Fetch until these conditions are met. See the [Agent Runtime Fetch Egress design](docs/superpowers/specs/2026-08-25-agent-runtime-fetch-egress-design.md) for the full threat model and deployment contract.

The Agent Runtime workflow publishes `ghcr.io/csusters/agent-runtime:<tag>` and `ghcr.io/csusters/agent-fetch-broker:<tag>`. The `dev` branch publishes `dev` and `latest-dev`; a published release publishes its release tag and `latest`.

### Runtime workspace storage and legacy migration

New Runtime releases use the complete namespace as a full lowercase SHA-256 namespace directory key. They do not automatically open legacy lossy workspace directories. The old mapping could collide, so automatic migration could attach data to the wrong namespace.

If old workspaces must be retained, stop the Runtime, back up the workspace volume, then perform an explicit offline migration reviewed by an operator. Do not use an automatic rename algorithm.

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

- **Language**: Go 1.26+
- **Framework**: [telebot.v3](https://github.com/tucnak/telebot)
- **Database**: Redis
- **AI**: OpenAI API Compatible Interface
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
