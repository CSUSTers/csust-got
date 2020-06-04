package module

import (
	"csust-got/context"
	"csust-got/util"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
)

// InteractFunc is a function that did the receive-then-reply.
type InteractFunc func(message *tgbotapi.Message) tgbotapi.Chattable

// InteractModule make a module by a InteractFunc
// call the interact function when accepted a message, then send reply by its return value.
func InteractModule(f InteractFunc) Module {
	return Stateful(func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) {
		msg := update.Message
		resultMedia := f(msg)
		util.SendMessage(bot, resultMedia)
	})
}
