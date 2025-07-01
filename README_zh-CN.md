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

现代化的长沙理工大学 Telegram 机器人，使用 Go 语言开发

[English](README.md) | [中文](README_zh-CN.md)

## 特性

- 🤖 AI 聊天对话（支持多种模型）
- 🔍 消息搜索（基于 MeiliSearch）
- 🎨 Stable Diffusion 图像生成
- 🎲 抽卡系统
- 🎭 各种娱乐功能
- 📊 Prometheus 监控
- 🔧 灵活的配置系统
- 🎯 正则表达式触发器
- 🛡️ 完善的权限管理
- 🔗 MCP (Model Context Protocol) 支持

## 系统要求

- Go 1.24+
- Redis
- Docker & Docker Compose（推荐）

## 快速部署

### 使用 Docker Compose（推荐）

您需要先安装 Docker。

克隆项目：

```bash
git clone git@github.com:CSUSTers/csust-got.git
cd csust-got
```

然后使用 Docker Compose 运行：

```bash
docker-compose up -d
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

## 升级

克隆最新版本：

```bash
docker-compose pull
docker-compose up -d
```

## 配置

请在 `config.yaml` 中修改配置。

- `token`: 修改为您的机器人 token
- `redis.pass`: 修改 Redis 密码
- `redis.conf` 中的 `requirepass`: 修改 Redis 密码（需要和上面一致）

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
search - <keyword> 搜索历史消息
search - -id <chat_id> <keyword> 搜索指定群组消息
search - -p <page> <keyword> 搜索指定页码
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

### Stable Diffusion

``` text
sd - <prompt> 生成图片
sdcfg - 配置SD服务器
sdcfg - set <key> <value> 设置配置
sdcfg - get <key> 获取配置
sdlast - 获取上次使用的prompt
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

- **语言**: Go 1.24+
- **框架**: [telebot.v3](https://github.com/tucnak/telebot)
- **数据库**: Redis
- **搜索**: MeiliSearch
- **监控**: Prometheus
- **AI**: OpenAI API 兼容接口
- **图像生成**: Stable Diffusion WebUI
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
golangci-lint run
```

## 许可证

本项目采用 [MIT License](LICENSE) 许可证。

---

**注意**: 本项目仅供学习交流使用。
