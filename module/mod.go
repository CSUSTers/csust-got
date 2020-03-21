package module

import (
	"csust-got/module/conds"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
)

type Module interface {
	HandleUpdate(update tgbotapi.Update, bot *tgbotapi.BotAPI)
	ShouldHandle(update tgbotapi.Update) bool
}

type HandleFunc func(update tgbotapi.Update, bot *tgbotapi.BotAPI)
type trivialModule struct {
	handleUpdate HandleFunc
	shouldHandle conds.Predicate
}

func (t trivialModule) HandleUpdate(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	t.handleUpdate(update, bot)
}

func (t trivialModule) ShouldHandle(update tgbotapi.Update) bool {
	return t.shouldHandle.Test(update)
}

// Stateless creates a 'stateless' module.
// If your state is tiny(which can be captured in a closure), use this.
func Stateless(handleFunc HandleFunc, condFunc conds.Predicate) Module {
	return trivialModule{
		handleUpdate: handleFunc,
		shouldHandle: condFunc,
	}
}
