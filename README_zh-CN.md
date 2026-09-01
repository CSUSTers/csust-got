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

现代化的 CSUST Telegram 机器人，使用 Go 语言开发

[English](README.md) | [中文](README_zh-CN.md)

## 特性

- 🤖 AI 聊天对话（支持多种模型）
- 🎲 抽卡系统
- 🎭 各种娱乐功能
- 🔧 灵活的配置系统
- 🎯 正则表达式触发器
- 🛡️ 完善的权限管理
- 🔗 MCP (Model Context Protocol) 支持

## 系统要求

- Go 1.27+
- Rust 1.95+（仅在从源码构建或验证 Agent Runtime 时需要）
- Redis
- Docker Engine 与 Docker Compose v2（推荐）

## 快速部署

### 使用 Docker Compose（推荐）

请先安装 Docker Engine 与 Docker Compose v2。

克隆项目：

```bash
git clone git@github.com:CSUSTers/csust-got.git
cd csust-got
```

启动仓库中已提交的 Compose 部署前，请在部署环境中设置以下必需的基础输入：

`AGENT_RUNTIME_TOKEN`、`AGENT_RUNTIME_CGROUP_PARENT`、`AGENT_RUNTIME_WORKSPACE_MAX_BYTES`、`AGENT_RUNTIME_WORKSPACE_FS_MAX_BYTES`、`AGENT_RUNTIME_LOG_FS_MAX_BYTES`、`AGENT_RUNTIME_WORKSPACE_HOST_ROOT`、`AGENT_RUNTIME_LOG_HOST_ROOT` 和 `AGENT_RUNTIME_CGROUP_HOST_ROOT`。

主机上的聚合/委派 cgroup，以及受限的工作区和 Runtime 日志挂载根目录必须预先存在。Compose 不会创建它们。完整主机验证属于下文受控 Fetch 的预检；只有这些基础输入时，不要声称主机已通过验证。

然后启动基础部署。Fetch 保持禁用：

```bash
docker compose up -d
```

### 从源码构建

```bash
# 克隆项目
git clone git@github.com:CSUSTers/csust-got.git
cd csust-got

# 安装依赖
make deps

# 构建
make build

# 运行
./got
```

如需直接验证源码，请使用受支持的 Linux 构建环境。生产用 Agent Runtime 构建仅支持 Linux：

```bash
go build ./...
cargo build --manifest-path agent-runtime/Cargo.toml --locked --release --bins
```

## 升级

已提交的 Compose 文件会从源码构建 Runtime；启用 Fetch 覆盖文件时也会从源码构建 Broker。请先更新源码检出，再重新构建并启动基础部署：

```bash
git pull --ff-only
docker compose build --pull agent-runtime
docker compose up -d
```

对于受控 Fetch 部署，请使用覆盖文件重建两个从源码构建的服务：

```bash
git pull --ff-only
docker compose --profile agent-fetch -f docker-compose.yml -f docker-compose.fetch.yml build --pull agent-runtime agent-fetch-broker
docker compose --profile agent-fetch -f docker-compose.yml -f docker-compose.fetch.yml up -d
```

`docker compose pull` 不会更新从源码构建的 Runtime 或 Broker。对于由运维人员维护镜像的部署，请将仅限该部署的 `build:` 条目替换为匹配的 Bot、Runtime，以及启用 Fetch 时的 Broker 镜像标签。拉取这些匹配的镜像标签后，再启动同一部署：

```bash
docker compose --profile agent-fetch -f docker-compose.yml -f docker-compose.fetch.yml pull
docker compose --profile agent-fetch -f docker-compose.yml -f docker-compose.fetch.yml up -d
```

## 配置

请在 `config.yaml` 中修改配置。

- `token`: 修改为您的机器人 token
- `redis.pass`: 修改 Redis 密码
- `redis.conf` 中的 `requirepass`: 修改 Redis 密码（需要和上面一致）

## Agent v3 Runtime 与受控 Fetch

仓库默认配置为 `agent_v3.enable: false` 和 `agent_v3.runtime.fetch_enabled: false`。如需开放 Agent v3 的 CLI Fetch，必须显式启用 Agent v3、其 Runtime 和 Fetch 开关：

```yaml
agent_v3:
  enable: true
  runtime:
    enable: true
    fetch_enabled: true
```

这些机器人配置开关本身不会启用生产环境的出站访问。基础 `docker-compose.yml` 保持 Fetch 禁用。受控 Fetch 需要 `docker-compose.fetch.yml` 覆盖文件及其 `agent-fetch` profile。除非运维人员设置 `AGENT_FETCH_ENABLE=true`，并提供 `AGENT_FETCH_POLICY_VERSION`、`AGENT_FETCH_EXTRA_DENY_CIDRS`、`AGENT_FETCH_DNS_SERVERS`、`AGENT_FETCH_AUDIT_FS_MAX_BYTES`、`AGENT_FETCH_AUDIT_HOST_ROOT` 和 `AGENT_FETCH_HMAC_SECRET_FILE`，否则请保持禁用。

首次运行 validator 前，必须提供完整的 Runtime 与受控 Fetch 主机约定。即使基础 Fetch 默认关闭，validator 仍会无条件要求 `AGENT_FETCH_AUDIT_HOST_ROOT`、`AGENT_FETCH_HMAC_SECRET_FILE`、`AGENT_FETCH_AUDIT_FS_MAX_BYTES`、`AGENT_FETCH_DNS_SERVERS` 和 `AGENT_FETCH_EXTRA_DENY_CIDRS`，以及上文列出的 Runtime 输入。`AGENT_FETCH_ENABLE` 和 `AGENT_FETCH_POLICY_VERSION` 不是 validator 输入。主机上的聚合/委派 cgroup 和全部受限挂载根目录（包括 Fetch 审计根目录）必须预先存在。Compose 和 validator 都不会创建主机路径或迁移数据。

请以 root 身份在目标原生 Linux 主机上运行只读 validator，然后仅使用覆盖文件和 profile 启动受控 Fetch：

```bash
bash scripts/validate-agent-runtime-host.sh
```

```bash
docker compose --profile agent-fetch -f docker-compose.yml -f docker-compose.fetch.yml up -d
```

模型和 MCP 工具是通过已注册 schema 直接发起的模型工具调用。Runtime Fetch 则在 Bash 环境中运行 `/usr/local/bin/fetch`，并通过 `bash` 工具调用，概念上为 `bash(command="fetch GET ...")`。名为 `fetch` 的 MCP/MCPO 工具是独立能力，不经过 Runtime Egress Broker。若要求 Runtime Broker 成为唯一的 Web 出站策略边界，请为该 Agent 移除或禁用 fetch 类 MCP 工具。

Shell 的直接 IPv4 和 IPv6 访问会被拒绝，Bash CLI 通过继承的 Unix FD 访问 Runtime 代理；只有 Broker 可以公共出站，并执行 SSRF、DNS、重定向、预算和审计控制。MCP fetch 不走这一路径。

PRoot 会创建并执行临时 loader，因此 Runtime 的 `/tmp` 必须可执行。基础 Compose 部署通过 `/tmp:rw,exec,nosuid,nodev,size=64m,mode=1777` 满足这一仅限部署的兼容性要求；它不会新增 Runtime readiness gate。

在生产环境启用 Fetch 前，请在目标原生 Linux 主机上运行以下推荐的部署检查：

```bash
bash scripts/validate-agent-runtime-host.sh
bash scripts/test-agent-runtime-compose.sh
bash scripts/agent-runtime-attack-matrix.sh
```

validator、Compose/static 测试和攻击矩阵都是部署检查，而不是 Runtime gate：Runtime 不会动态读取或根据其回执进行 gate。每个命令应以 0 退出，攻击矩阵应报告 `fail=0 skipped=0`，且清理后不应留下任何残留物。主机 validator 接受等价的 nftables reject 渲染，例如 `iifname "br-agent-fetch" reject with icmp port-unreachable`；但它仍要求精确的 bridge 匹配、所需 deny set、hook/priority/policy，以及紧跟预期匹配后的 reject verdict。不要为规避 PRoot 问题而使用 `privileged`、Docker `seccomp=unconfined`、AppArmor unconfined 或 `SYS_PTRACE`，除非已单独接受其风险。目标原生 Linux 的生产启用回执仍在等待中，在满足这些条件前请勿启用生产 Fetch。完整威胁模型和部署约定见 [Agent Runtime Fetch Egress 设计](docs/superpowers/specs/2026-08-25-agent-runtime-fetch-egress-design.md)。

Agent Runtime 工作流会发布 `ghcr.io/csusters/agent-runtime:<tag>` 和 `ghcr.io/csusters/agent-fetch-broker:<tag>`。`dev` 分支发布 `dev` 和 `latest-dev`，已发布的 release 发布其 release tag 和 `latest`。

### Skills、原生 SearXNG 与 Fetch User-Agent

新的 skill 来源默认关闭：

```yaml
agent_v3:
  skills:
    root: ""
    runtime_global: false
    searxng:
      enable: false
```

`agent_v3.skills.root` 是 Bot-local root，`runtime_global` 控制可选的
Runtime-global snapshot。三个来源按固定优先级
`builtin > bot-local > runtime-global` 合并。同一来源中 malformed 或重复的
skill 会令拥有该来源的进程启动失败。跨来源重名不会失败，优先级较高的 skill
获胜，并记录不含 skill 正文的 shadow warning。

每个 filesystem skill 必须恰好位于 `<root>/<canonical-name>/SKILL.md`，名称
匹配 `^[a-z0-9][a-z0-9-]{0,63}$`。loader 会忽略 root 中的普通文件，例如
`README.md`，但 malformed 的直接子目录会使该来源失败。它不递归、不跟随
symlink，也不要求 YAML/frontmatter。`SKILL.md` 必须是非空 UTF-8，单个上限
64 KiB；每个来源最多 128 个 skill、最多 1 MiB 正文。description 取第一条
非空、非 ATX heading 的 prose line，去除首尾空白并限制为 200 个 Unicode rune。

snapshot 在启动后不可变。要刷新内容，必须重启拥有它的 Bot 或 Runtime。模型
只能通过 `load_skill` 获取正文，不能用 generic `read` 或 `grep` 读取
`/skills`。generic read-only Runtime `/skills` 文件系统和 scripts 仍供运维人员
与 Runtime 使用，但它们不授权或激活 skill，也不会注册 tool schema。

#### Runtime-global 上线与挂载

先升级并启动能提供已认证 `GET /v1/skills` 的 Runtime，然后才在 Bot 中启用
`agent_v3.skills.runtime_global: true`。Bot 只会在启动时获取并验证一次完整
snapshot。请求失败或 snapshot 无效会令 Bot 启动失败，重启是唯一的刷新方式。

已提交的 Compose 部署只挂载 Runtime skill root：

```text
./skills:/runtime/skills:ro
```

它没有挂载 Bot-local root。当 `agent_v3.skills.root` 非空时，运维人员必须单独
把该目录挂载到 Bot 可读的配置路径。Runtime 挂载不会被复制或自动共享给 Bot。

#### 原生 SearXNG

只有 `agent_v3.skills.inject_builtin` 和
`agent_v3.skills.searxng.enable` 都为 true 时，Agent v3 才会加入 `searxng`
builtin skill，并为一个固定配置实例注册以下且仅以下原生 tools：

- `searxng_web_search`
- `searxng_search_suggestions`
- `searxng_instance_info`

模型每一轮都必须先成功调用 `load_skill("searxng")`，才可使用其中任何 tool。
激活前不会进行任何 HTTP I/O，包括 DNS、连接和读取凭据。若 MCPO tool 同名，
原生 tool 获胜并记录 warning。MCPO 保留，其他 tools 仍可用。

配置位于 `agent_v3.skills.searxng`：`enable`、`base_url`、`username_env`、
`password_env`、`timeout`、`max_response_bytes`、`max_results`、
`max_result_chars`、`default_language`、`default_safesearch`、
`default_response_format` 和 `user_agent`。已提交的默认值是 10s timeout、
1,048,576 response bytes、10 个结果、每个结果 2,000 个字符、`zh-CN`、
safesearch `1`、response format `text` 和 user agent `csust-got-agent-v3`。
`agent_v3.skills.mode` 为 `system_prompt`，`inject_builtin` 默认是 true。启用时，
`base_url` 必须是没有 userinfo、query 或 fragment 的绝对 `http` 或 `https` URL。
`username_env` 和 `password_env` 可选，但必须成对命名，且必须是合法环境变量名。
限制分别为 1ms 到 30s、1 byte 到 5 MiB、1 到 20 个结果、1 到 16,384 个且不超过
response limit 的 result characters、非空且无控制字符的 64-rune language、
safesearch 0、1 或 2，以及非空、无控制字符且最多 512 bytes 的 user agent。
response format 只能是 `text` 或 `json`。

SearXNG 不支持多实例、failover、fanout、cache、HTML fallback、proxy 或 browser
solver。它不会新增 generic URL reader。本工作不会移除 MCPO。

#### Fetch Broker User-Agent

在最终 wire-budget 检查前，如 caller 没有提供 `User-Agent`，Fetch Broker 会加入
以下默认值：

```text
Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36
```

caller 的 `User-Agent` 名称按大小写不敏感匹配。若 caller 提供多个值，最后一个
caller 值获胜，且不会加入默认值。这不会加入更广泛的 browser headers 或 browser
emulation：不会注入 `Accept`、`Accept-Language`、Client Hints、cookies 或其他
browser 行为。

host validator、Compose/static checks 和 attack matrix 只是推荐的部署证据，绝不
成为 Bot 或 Runtime 的基础启动、readiness 或 activation gate。主机条件不满足时，
运维人员仍可运行基础部署，但不得声称这些部署检查已通过。

### Runtime 工作区存储与旧工作区迁移

新版 Runtime 使用完整命名空间的全小写 SHA-256 值作为命名空间目录键，不会自动打开旧版有损工作区目录。旧映射可能发生碰撞，因此自动迁移可能会将数据关联到错误的命名空间。

如需保留旧工作区，请停止 Runtime、备份工作区卷，然后执行经运维人员审查的显式离线迁移。不要使用自动重命名算法。

## 命令列表

### 基础功能

``` text
say_hello - 我是一只只会嗦hello的咸鱼
hello_to_all - 大家好才是真的好
recorder - <msg> 人类的本质就是复读机，Bot也是一样的
info - 获取机器人信息
id - 获取用户ID（私聊）
cid - 获取群组ID
```

### 搜索功能

``` text
google - <Key Words> 咕果搜索...
bing - <Key Words> 巨硬搜索...
bilibili - <Key Words> 在B站搜索...
github - <Key Words> 在github搜索...
```

### AI 聊天

``` text
chat - <text> 聊会天呗
think - <text> 深度思考模式
summary - 总结回复的内容（需要回复消息使用）
```

### 管理功能

``` text
ban_myself - 把自己ban掉rand[40,120]秒
ban - 我就是要滥权！【Admin】
ban_soft - 软禁！使某人失去快乐~【Admin】
fake_ban - [duration] 虚假(真实)的ban
fake_ban_myself - 虚假的ban自己
kill - 虚假(真实)的kill
no_sticker - 启动(反向)流量节省模式
shutdown - 拔掉bot的电源
boot - 将bot开机
```

### 娱乐功能

``` text
hitokoto - [type:ab..kl] 一言
hitowuta - 一诗
hito_netease - 一键网抑
mc - MC 小游戏
reburn - 重生（MC小游戏）
gacha_setting - 设置一个json格式的抽卡配置
gacha - 抽卡，按照你的配置
```

### 语音相关

``` text
getvoice - 角色=<character> 性别=<sex> 主题=<topic> 类型=<type> <text> 
```

### 工具功能

``` text
forward - [msgID] 让bot转发一条历史消息(可能消息已经被删了)
sleep - 该睡觉了
no_sleep - 别睡了
run_after - <duration> <msg> 提醒自己多久之后做什么事
hoocoder - <text> Hoo编码
decode - _[decoding]_[encoding] <text> 解个码
bye_world - [duration] 向美好世界说声再见
hello_world - 向美好世界问声好
iwant - f=<format> 我要Sticker
setiwant - f=<format> vf=<format> sf=<format> 设置我要Sticker
```

## 技术栈

- **语言**: Go 1.27+
- **框架**: [telebot.v3](https://github.com/tucnak/telebot)
- **数据库**: Redis
- **AI**: OpenAI API 兼容接口
- **容器化**: Docker & Docker Compose

## 开发

### 本地开发

```bash
# 安装依赖
make deps

# 运行测试
make test

# 构建
make build

# 代码检查
golangci-lint run --fix=false
```

## 许可证

本项目采用 [MIT License](LICENSE) 许可证。

---

**注意**: 本项目仅供学习交流使用。
