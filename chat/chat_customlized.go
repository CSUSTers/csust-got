package chat

import (
	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"csust-got/util"
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

// Deprecated: no longer used. if you want to use it, please refactor.
type chatCustModel struct {
	Text string `json:"text"`
}

// CustomChatInfo deprecated
type CustomChatInfo struct {
	ChatInfo
}

// WithText deprecated
func (c *CustomChatInfo) WithText(s string) *CustomChatInfo {
	c.ChatInfo.Text = s
	return c
}

// CustomModelChat 自定义的大语言模型
// Deprecated: no longer used. if you want to use it, please refactor.
func CustomModelChat(ctx tb.Context, info CustomChatInfo) error {
	if client == nil {
		return nil
	}

	log.Debug("[ChatGPT] CustomModelChat", zap.String("text", ctx.Message().Text))

	_, arg, err := entities.CommandTakeArgs(ctx.Message(), 0)
	if err != nil {
		log.Error("[ChatGPT] CustomModelChat Can't take args", zap.Error(err))
		return ctx.Reply("嗦啥呢？")
	}
	arg += util.GetAllReplyMessagesText(ctx.Message())
	log.Debug("[ChatGPT] CustomModelChat", zap.String("text", arg))

	if len(arg) > config.BotConfig.ChatConfig.PromptLimit {
		return ctx.Reply("TLDR")
	}

	msg, err := util.SendReplyWithError(ctx.Chat(), "正在思考...", ctx.Message())
	if err != nil {
		return err
	}
	err = generateRequestCustomModelChat(arg, msg)
	return err

}

// Deprecated: no longer used. if you want to use it, please refactor.
func generateRequestCustomModelChat(arg string, msg *tb.Message) error {
	serverAddress := config.BotConfig.GenShinConfig.ApiServer + "/Chat" + "?text=" + url.QueryEscape(arg)
	log.Info(serverAddress)

	data := chatCustModel{}
	resp, err := http.Get(serverAddress)
	if err != nil {
		log.Error("[ChatGPT] CustomModelChat 连接chat api服务器失败", zap.Error(err))
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		log.Error("[ChatGPT] CustomModelChat chat api服务器返回异常", zap.Int("status", resp.StatusCode), zap.String("body", string(body)))
		return err
	}
	err = json.Unmarshal(body, &data)
	if err != nil {
		log.Error("[ChatGPT] CustomModelChat chat api服务器json反序列化失败", zap.Error(err), zap.String("body", string(body)))
		return err
	}
	_, err = util.EditMessageWithError(msg, data.Text)

	if err != nil {
		log.Error("[ChatGPT] CustomModelChat Can't edit message", zap.Error(err))
	}
	return err
}
