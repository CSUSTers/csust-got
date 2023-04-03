package chat

import (
	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"csust-got/util"
	"encoding/json"
	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
	"io"
	"net/http"
	"net/url"
)

type chatCustModel struct {
	Text string `json:"text"`
}

// Cust 自定义的大语言模型
func Cust(ctx Context) error {
	if client == nil {
		return nil
	}

	_, arg, err := entities.CommandTakeArgs(ctx.Message(), 0)
	if err != nil {
		log.Error("[ChatGPT] Can't take args", zap.Error(err))
		return ctx.Reply("嗦啥呢？")
	}
	if len(arg) == 0 {
		return ctx.Reply("您好，有什么问题可以为您解答吗？")
	}
	if len(arg) > config.BotConfig.ChatConfig.PromptLimit {
		return ctx.Reply("TLDR")
	}

	msg, err := util.SendReplyWithError(ctx.Chat(), "正在思考...", ctx.Message())
	if err != nil {
		return err
	}
	err = generateRequestCust(arg, msg)
	return err

}

func generateRequestCust(arg string, msg *Message) error {
	serverAddress := config.BotConfig.GenShinConfig.ApiServer + "/Chat" + "?text=" + url.QueryEscape(arg)
	log.Info(serverAddress)

	data := chatCustModel{}
	resp, err := http.Get(serverAddress)
	if err != nil {
		log.Error("连接chat api服务器失败", zap.Error(err))
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		log.Error("chat api服务器返回异常", zap.Int("status", resp.StatusCode), zap.String("body", string(body)))
		return err
	}
	err = json.Unmarshal(body, &data)
	if err != nil {
		log.Error("chat api服务器json反序列化失败", zap.Error(err), zap.String("body", string(body)))
		return err
	}
	_, err = util.EditMessageWithError(msg, data.Text)

	if err != nil {
		log.Error("[ChatGPT] Can't edit message", zap.Error(err))
	}
	return err
}
