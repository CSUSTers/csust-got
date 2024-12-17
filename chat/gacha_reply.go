package chat

import (
	"csust-got/log"
	"csust-got/util/gacha"
	"strings"

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
	if len(text) == 0 || strings.HasPrefix(text, "/") {
		return
	}

	result, err := gacha.PerformGaCha(ctx.Chat().ID)
	if err != nil {
		log.Error("[GaCha]: perform gacha failed", zap.Error(err))
		return
	}

	switch result {
	case 3:
		return
	case 4:
		// TODO: `ChatWith` a different prompt
	case 5:
		err = ChatWith(ctx, &ChatInfo{
			Text:    text,
			Setting: Setting{Stream: false, Reply: true},
		})
		if err != nil {
			log.Error("[ChatGPT]: get a answer failed", zap.Error(err))
		}
	}
}
