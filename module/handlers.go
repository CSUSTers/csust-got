package module

import (
	"csust-got/context"
	"csust-got/util"
	"github.com/go-telegram-bot-api/telegram-bot-api"
)

type InteractFunc func(message *tgbotapi.Message) tgbotapi.Chattable

func InteractModule(f InteractFunc) Module {
	return Stateful(func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) {
		msg := update.Message
		resultMedia := f(msg)
		util.SendMessage(bot, resultMedia)
	})
}
