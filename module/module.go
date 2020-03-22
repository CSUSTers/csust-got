package module

import (
	"csust-got/module/preds"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
)

type Module interface {
	HandleUpdate(context Context, update tgbotapi.Update, bot *tgbotapi.BotAPI)
}

type HandleFunc func(update tgbotapi.Update, bot *tgbotapi.BotAPI)
type trivialModule struct {
	handleUpdate HandleFunc
	shouldHandle preds.Predicate
}

func (t trivialModule) HandleUpdate(_ Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	if t.shouldHandle.ShouldHandle(update) {
		t.handleUpdate(update, bot)
	}
}

// Stateless creates a 'stateless' module.
// If your state is tiny(which can be captured in a closure), use this.
func Stateless(handleFunc HandleFunc, condFunc preds.Predicate) Module {
	return trivialModule{
		handleUpdate: handleFunc,
		shouldHandle: condFunc,
	}
}
