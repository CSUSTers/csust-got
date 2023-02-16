package base

import (
	"csust-got/config"
	"csust-got/log"
	"csust-got/util"
	"encoding/json"
	"fmt"
	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
	"io"
	"net/http"
)

type genShinVoice struct {
	Audio     string `json:"audio"`
	Character string `json:"character"`
	Topic     string `json:"topic"`
	Text      string `json:"text"`
}

// GetVoice 从api服务器拿到语音的url以及其他信息，并发送为tg的voice信息
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
