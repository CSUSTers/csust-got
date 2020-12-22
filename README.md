# csust-got

[![Go Report](https://goreportcard.com/badge/github.com/csusters/csust-got)](https://goreportcard.com/report/github.com/csusters/csust-got)
[![codebeat badge](https://codebeat.co/badges/4d134b7f-e345-4378-b00d-7ab2177b94bc)](https://codebeat.co/projects/github-com-csusters-csust-got-master)

[![Branch test Build Status](https://travis-ci.org/CSUSTers/csust-got.svg?branch=test)](https://travis-ci.org/CSUSTers/csust-got)
![GitHub Workflow Status (branch)](https://img.shields.io/github/workflow/status/CSUSTers/csust-got/Test/master?label=master%20test)
![GitHub Workflow Status (branch)](https://img.shields.io/github/workflow/status/CSUSTers/csust-got/Test/dev?label=dev%20test)

![GitHub language count](https://img.shields.io/github/languages/count/csusters/csust-got)
![GitHub](https://img.shields.io/github/license/csusters/csust-got)
![GitHub code size](https://img.shields.io/github/languages/code-size/csusters/csust-got)
![GitHub repo size](https://img.shields.io/github/repo-size/csusters/csust-got)
![GitHub issues](https://img.shields.io/github/issues/csusters/csust-got)
![GitHub closed issues](https://img.shields.io/github/issues-closed/csusters/csust-got)

csust new telegram bot in go

## Deploy

You need to install Docker first.

Clone the project.

```bash
git clone git@github.com:CSUSTers/csust-got.git
```

Then run it with docker compose.

```bash
docker-compose up -d
```

## Upgrade from old version

Clone the newest version.

```bash
git pull
docker-compose up -d --build
```

## Configuration

Please change configuration in `docker-compose.yml`.

Modify the `TOKEN` to your bot's token, or just set environment variable `CSUST_BOT_TOKEN`.

Please modify `REDIS_PASSWORD` in `docker-compose.yml`,and also please modify `requirepass` in `config/redis.conf`.

## Commands

``` text
say_hello - 我是一只只会嗦hello的咸鱼
hello_to_all - 大家好才是真的好
recorder - <msg> 人类的本质就是复读机，Bot也是一样的
no_sticker - 启动(反向)流量节省模式
google - <Key Words> 咕果搜索...
bing - <Key Words> 巨硬搜索...
bilibili - <Key Words> 在B站搜索...
github - <Key Words> 在github搜索...
links - 这里有一些链接(加友链at管理)
ban_myself - 把自己ban掉rand[60,120]秒
ban - 我就是要滥权！【Admin】
ban_soft - 软禁！使某人失去快乐~【Admin】
fake_ban - [duration] 虚假(真实)的ban
fake_ban_myself - 虚假的ban自己(也不一定)
kill - 虚假(真实)的kill
hitokoto - [type] 一言
hitowuta - 一诗
hito_netease - 一键网抑
history - [msgID] 获取一条历史消息的链接
forward - [msgID] 让bot转发一条历史消息
shutdown - 拔掉bot的电源
boot - 将bot开机
sleep - 该睡觉了
no_sleep - 别睡了
run_after - <duration> <msg> 提醒自己多久之后做什么事
```
