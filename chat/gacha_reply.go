package chat

import (
	"csust-got/log"
	"csust-got/util/gacha"
	"go.uber.org/zap"
	"gopkg.in/telebot.v3"
)

// GachaReplyHandler reply a gpt msg determined by the gacha result
func GachaReplyHandler(ctx telebot.Context) {
	msg := ctx.Message()

	// only apply to text message
	var text string
	if len(msg.Text) > 0 {
		text = msg.Text
	} else if len(msg.Caption) > 0 {
		text = msg.Caption
	}
	if len(text) == 0 {
		return
	}

	result, err := gacha.PerformGaCha(ctx.Chat().ID)
	if err != nil {
		log.Error("[GaCha]: perform gacha failed", zap.Error(err))
		return
	}
	ctx.Message().Text = "/chat " + text
	switch result {
	case 3:
		return
	case 4:
		err = CustomModelChat(ctx)
		if err != nil {
			log.Error("[ChatGPT]: get a answer failed", zap.Error(err))
		}
	case 5:
		err = GPTChat(ctx)
		if err != nil {
			log.Error("[ChatGPT]: get a answer failed", zap.Error(err))
		}
	}
}
