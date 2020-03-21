package module

import tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"

type Module interface {
	HandleUpdate(update tgbotapi.Update, bot *tgbotapi.BotAPI)
	ShouldHandle(update tgbotapi.Update) bool
}

type HandleFunc func(update tgbotapi.Update, bot *tgbotapi.BotAPI)
type HandleCondFunc func(update tgbotapi.Update) bool
type trivialModule struct {
	handleUpdate HandleFunc
	shouldHandle HandleCondFunc
}

func (t trivialModule) HandleUpdate(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	t.handleUpdate(update, bot)
}

func (t trivialModule) ShouldHandle(update tgbotapi.Update) bool {
	return t.shouldHandle(update)
}

// Stateless creates a 'stateless' module.
// If your state is tiny(which can be captured in a closure), use this.
func Stateless(handleFunc HandleFunc, condFunc HandleCondFunc) Module {
	return trivialModule{
		handleUpdate: handleFunc,
		shouldHandle: condFunc,
	}
}
