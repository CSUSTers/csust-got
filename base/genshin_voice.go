package base

import (
	"csust-got/config"
	"csust-got/log"
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
	data := genShinVoice{}
	serverAddress := config.BotConfig.GenShinConfig.ApiServer
	resp, err := http.Get(serverAddress)
	if err != nil {
		log.Error("api server error", zap.Error(err))
		err := ctx.Reply("凯瑟琳: \n 异常……", nil)
		return err
	}
	if resp.StatusCode != 200 {
		log.Error("api server response", zap.Int("status", resp.StatusCode))
	} else {
		body, _ := io.ReadAll(resp.Body)
		err := json.Unmarshal(body, &data)
		if err != nil {
			log.Error("api server response", zap.Int("status", resp.StatusCode))
			return err
		}
	}
	audioCaption := fmt.Sprintf("%s \n\n #%s  %s", data.Text, data.Character, data.Topic)
	voice := Voice{File: FromURL(data.Audio), Caption: audioCaption}
	_, err = voice.Send(ctx.Bot(), ctx.Recipient(), nil)
	return err
}
