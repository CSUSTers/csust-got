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

csust new telegram bot in go

## Deploy

You need to install Docker first.

Clone the project.

```bash
git clone git@github.com:CSUSTers/csust-got.git
```

Then run it with docker-compose.

```bash
docker-compose up -d
```

## Upgrade from old version

Clone the newest version.

```bash
docker-compose pull
docker-compose up -d
```

## Configuration

Please change configuration in `config.yaml`.

Modify the `token` to your bot's token.

Please modify `redis.pass` in `config.yaml`,and also please modify `requirepass` in `redis.conf`.

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
ban_myself - 把自己ban掉rand[40,120]秒
ban - 我就是要滥权！【Admin】
ban_soft - 软禁！使某人失去快乐~【Admin】
fake_ban - [duration] 虚假(真实)的ban
fake_ban_myself - 虚假的ban自己
kill - 虚假(真实)的kill
hitokoto - [type:ab..kl] 一言
hitowuta - 一诗
hito_netease - 一键网抑
forward - [msgID] 让bot转发一条历史消息(可能消息已经被删了)
shutdown - 拔掉bot的电源
boot - 将bot开机
sleep - 该睡觉了
no_sleep - 别睡了
run_after - <duration> <msg> 提醒自己多久之后做什么事
hoocoder - <text> Hoo编码
decode - _[decoding]_[encoding] <text> 解个码
getvoice - 角色=<character> 性别=<sex> 主题=<topic> 类型=<type> <text> 
getvoice_old - getvoice的旧版入口，没有查询功能，数据来源于mys爬虫
chat - <text> 聊会天呗
qiuchat - <text> 聊会天呗
genvoice - <text> 生成原神语音
provoice - <text> 使用自定义ssml生成语音
search - [-id <chat_id>] <key word> 搜索历史记录
gacha_setting - 设置一个json格式的配置
gacha - 抽卡，按照你的配置
bye_world - [duration] 向美好世界说声再见
hello_world - 向美好世界问声好
iwant - f=<format> 我要Sticker
setiwant - f=<format> vf=<format> sf=<format> 设置我要Sticker
```

## attachment

Located in `attachment` folder.

### voiceGen

VoiceGen is a api server to search or generate genshin impact npc's voice for the bot.
