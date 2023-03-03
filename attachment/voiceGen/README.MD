# 原神语音查询api

## 是什么 & 版权声明
用于配合csust-got项目的api服务端，使用python构建，数据保存在SQLite中。

本项目的数据源来自 [mys-voice-genshin](https://github.com/CSUSTers/mys-voice-genshin) 和 [GenshinVoice](https://github.com/w4123/GenshinVoice) 在此感谢这两个项目。

另，本项目使用的语音素材及文本，版权均归于米哈游所有。本项目仅用于学习交流，除已获取版权方许可者外，禁止使用本项目进行商业用途。

## 如何用

推荐使用docker安装部署，请保证你的docker版本大于等于19.03.0。

在任一安装有docker的机器上，克隆此仓库，然后运行以下命令：

```bash
sudo chmod +x ./start_docker_container.sh
sudo ./start_docker_container.sh
```

如果一切正常，您将看到docker容器编译成功，并能在 `docker ps` 的结果中看到一个名为 `voice_gen` 的容器正在运行, 然后您可以通过 `http://localhost:37332` 访问本项目。

### Api 文档

目前有2个Api，详情如下：

#### 1. 获取语音列表（数据来自[米游社](https://github.com/CSUSTers/mys-voice-genshin)）

| URL | request | version | status |
| :--- | :--- | :--- | :--- |
| /GenShin/GetVoice | Get | 1.0 | true |

##### 请求参数说明

无参数

##### 返回参数说明

| 返回参数 | 参数类型 | 参数说明 |
| :--- | :--- | :--- |
| audio | string | 音频文件url |
| character | string | 语音角色 |
| text | string | 音频文本 |
| topic | string | 话题 |

##### 返回示例JSON

```json
{
"audio": "https://raw.githubusercontent.com/CSUSTers/mys-voice-genshin/main/res/audio/%E7%94%B3%E9%B9%A4/a988c557f092e60bc2f2262e1ae99538_8516534833961351507.ogg",
"character": "申鹤",
"text": "他的药草品质很好，为人也随和周到。相比起来，脖子上的小蛇倒是口无遮拦…放心，我没有动手。我还不至于和一条蛇置气。",
"topic": "关于白术…"
}
```

#### 2. 获取语音列表（数据来自[游戏解包](https://github.com/w4123/GenshinVoice)）

| URL | request | version | status |
| :--- | :--- | :--- | :--- |
| /GenShin/GetVoice/v2 | Get | 2.0 | true |

##### 请求参数说明

| 请求参数 | 类型 | 必填 | 参数说明 | 示例 |
| :--- | :--- | :--- | :--- | :--- |
| character | String | false | 查询的角色名称 |刻晴 |
| topic | String | false | 语音的话题（只有羁绊类的语音才有话题） |skill1_03   |
| text | String | false | 语音文本 |进不去！怎么想都进不去嘛！ |
| sex | Bool | false | 角色性别，女性为true，男性为 false |true |
| type | String | false | 语音类型，比如羁绊：Fetter；剧情对话：Dialog |Dialog |

##### 返回参数说明

| 返回参数 | 参数类型 | 参数说明 |
| :--- | :--- | :--- |
| audioURL | string | 语音文件url |
| fileName     | string   | 语音文件路径（本地访问用）                      |
| language     | string   | 语音语言                                        |
| npcNameCode  | string   | 角色在游戏中的代号（不同语言中相同），如“ganyu” |
| npcNameLocal | string   | 角色的本地化名字，如 “甘雨”                     |
| sex          | bool     | 角色性别，true为女性，false为男性               |
| text         | string   | 语音对应的文本                                  |
| topic        | string   | 语音的话题（只有羁绊类的语音才有话题）          |
| type         | string   | 语音类型，比如羁绊：Fetter；剧情对话：Dialog    |

##### 返回示例JSON

```json
{
"audioURL": "https://api.csu.st/file/VO_gameplay/VO_barbara/vo_barbara_battle_skill1_01.ogg",
"fileName": "Chinese\\VO_gameplay\\VO_barbara\\vo_barbara_battle_skill1_01.wem",
"language": "CHS",
"npcNameCode": "barbara",
"npcNameLocal": "芭芭拉",
"sex": "True",
"text": "我会保护大家！",
"topic": "skill1_01",
"type": "Fetter"
}
```

## 项目结构

``` plaintext
├── db/
│   ├── character.db # mys数据
│   ├── genshinVoice.db # 游戏解包数据
│   └── result.json # 来自 w4123/GenshinVoice 项目的数据文件
├── db_utils/
│   └── db_utils.py # 将json导入数据库的工具
└── ******** # 其他文件
```
