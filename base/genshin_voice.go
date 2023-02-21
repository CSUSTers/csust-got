package base

import (
	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"csust-got/util"
	"encoding/json"
	"fmt"
	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// v1 版本的api返回的json数据结构
type genShinVoice struct {
	Audio     string `json:"audio"`
	Character string `json:"character"`
	Topic     string `json:"topic"`
	Text      string `json:"text"`
}

// v2 版本的api返回的json数据结构
type genShinVoiceV2 struct {
	AudioURL     string `json:"audioURL"`
	FileName     string `json:"fileName"`
	Language     string `json:"language"`
	NpcNameCode  string `json:"npcNameCode"`
	NpcNameLocal string `json:"npcNameLocal"`
	Sex          string `json:"sex"`
	Text         string `json:"text"`
	Topic        string `json:"topic"`
	Type         string `json:"type"`
}

// GetVoice (v1版本)从api服务器拿到语音的url以及其他信息，并发送为tg的voice信息
func GetVoice(ctx Context) error {
	m := ctx.Message()
	data := genShinVoice{}
	serverAddress := config.BotConfig.GenShinConfig.ApiServer
	resp, err := http.Get(serverAddress + "/GenShin/GetVoice")
	if err != nil {
		log.Error("api server error", zap.Error(err))
		util.SendReply(m.Chat, "凯瑟琳: \n 异常……", m)
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		log.Error("api server response", zap.Int("status", resp.StatusCode), zap.String("body", string(body)))
		util.SendReply(m.Chat, "凯瑟琳: \n 重试……", m)
		return err
	}
	err = json.Unmarshal(body, &data)
	if err != nil {
		log.Error("json serialization failed", zap.Error(err), zap.String("body", string(body)))
		util.SendReply(m.Chat, "凯瑟琳: \n 超时……", m)
		return err

	}
	audioCaption := fmt.Sprintf("%s \n\n #%s  %s", data.Text, data.Character, data.Topic)
	voice := Voice{File: FromURL(data.Audio), Caption: audioCaption}
	_, err = voice.Send(config.BotConfig.Bot, m.Chat, nil)
	return err
}

// GetVoiceV2 (v2版本)增加了查询功能
func GetVoiceV2(ctx Context) error {
	arg, ok := parseVoiceArgs(ctx)
	m := ctx.Message()
	if !ok {
		err := SendErrVoice(m.Chat, "指令解析失败")
		log.Error("指令解析失败", zap.Error(err))
		return err
	}
	data := genShinVoiceV2{}
	serverAddress := config.BotConfig.GenShinConfig.ApiServer
	resp, err := http.Get(serverAddress + "/GenShin/GetVoice/v2" + arg)

	if err != nil {
		log.Error("连接语音api服务器失败", zap.Error(err))
		err := SendErrVoice(m.Chat, "连接语音api服务器失败")
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		log.Error("语音api服务器返回异常", zap.Int("status", resp.StatusCode), zap.String("body", string(body)))
		err := SendErrVoice(m.Chat, "没有找到对应的语音，参数："+arg)
		return err
	}
	err = json.Unmarshal(body, &data)
	if err != nil {
		log.Error("语音api服务器json反序列化失败", zap.Error(err), zap.String("body", string(body)))
		err := SendErrVoice(m.Chat, "语音api服务器json反序列化失败")
		return err

	}
	err = SendVoice(m.Chat, data)
	return err
}

func parseVoiceArgs(ctx Context) (arg string, ok bool) {
	command := entities.FromMessage(ctx.Message())
	var args []string
	if command.Argc() > 0 {
		args = command.MultiArgsFrom(0)
	}

	// 输入的 arg 形如 "角色=凯亚 文本=任务 性别=女 主题=birthday 类型=Fetter"
	// 需要转换为 "/GenShin/GetVoice/v2?character=凯亚&text=任务&sex=true&topic=birthday&type=Fetter"
	arg, args = argsMapper(args)

	// fallback到默认预设，将最后的参数当作文本查询
	if len(args) != 0 {
		return arg + "text=" + url.QueryEscape(args[len(args)-1]), true
	}
	return arg, true
}

// argsMapper 将输入的参数转换为api服务器的url参数
func argsMapper(args []string) (arg string, argsNew []string) {
	// 构建一个(分隔符-为了goland汉语语法检查器happy)字典映射
	m := map[string]string{
		"角色=": "character=",
		"性别=": "sex=",
		"主题=": "topic=",
		"类型=": "type=",
	}

	arg = "?"
	argsNew = args
	for _, v := range args {
		// flag 用于移除未被用到的参数
		flag := 0
		// 如果 map 中包含前三个字符
		vRune := []rune(v)
		if _, ok := m[string(vRune[0:3])]; ok {
			flag = 1
			// 在args列表中移除这条记录
			argsNew = deleteSlice(argsNew, v)
			// 替换前三个字符
			v = m[string(vRune[0:3])] + url.QueryEscape(string(vRune[3:]))
			arg += v + "&"
		}
		// 如果v中包含"="
		if strings.Contains(v, "=") {
			if flag == 0 {
				// 在args列表中移除这条记录
				argsNew = deleteSlice(argsNew, v)
			}
		}
	}
	return arg, argsNew
}

// SendErrVoice 发送凯瑟琳错误信息
func SendErrVoice(chat *Chat, errStr string) error {
	voice := Voice{File: FromURL(config.BotConfig.GenShinConfig.ErrAudioAddr), Caption: " …异常…\n #凯瑟琳 #异常 \n" + errStr}
	_, err := voice.Send(config.BotConfig.Bot, chat, nil)
	return err
}

// SendVoice 发音频消息
func SendVoice(chat *Chat, v genShinVoiceV2) error {
	audioCaption := fmt.Sprintf("%s \n\n #%s  %s", v.Text, v.NpcNameLocal, v.Topic)
	voice := Voice{File: FromURL(v.AudioURL), Caption: audioCaption}
	_, err := voice.Send(config.BotConfig.Bot, chat, nil)
	return err
}

// deleteSlice 删除slice中的某个元素
func deleteSlice(a []string, subSlice string) []string {
	ret := make([]string, 0, len(a))
	for _, val := range a {
		if val != subSlice {
			ret = append(ret, val)
		}
	}
	return ret
}
