package chat

import (
	"csust-got/log"
	"csust-got/util/gacha"
	"go.uber.org/zap"
	"gopkg.in/telebot.v3"
)

// GachaReplyHandler reply a gpt msg determined by the gacha result
func GachaReplyHandler(ctx telebot.Context) {
	result, err := gacha.PerformGaCha(ctx.Chat().ID)
	if err != nil {
		log.Error("[GaCha]: perform gacha failed", zap.Error(err))
	}
	ctx.Message().Text = "/chat " + ctx.Message().Text
	switch result {
	case 3:
		return
	case 4:
		err = Cust(ctx)
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
