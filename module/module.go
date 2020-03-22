package module

import (
	"csust-got/module/preds"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
)

type Module interface {
	HandleUpdate(context Context, update tgbotapi.Update, bot *tgbotapi.BotAPI)
}
type HandleFunc func(update tgbotapi.Update, bot *tgbotapi.BotAPI)
type StatefulHandleFunc func(ctx Context, update tgbotapi.Update, bot *tgbotapi.BotAPI)
type trivialModule struct {
	handleUpdate StatefulHandleFunc
}

func (t trivialModule) HandleUpdate(ctx Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	t.handleUpdate(ctx, update, bot)
}

// Stateful warps a stateful function to a Module.
func Stateful(f StatefulHandleFunc) Module {
	return trivialModule{handleUpdate: f}
}

// WithPredicate warps a Module with specified Predicate.
// The method `handleUpdate` will only be invoked when the Predicate is true.
func WithPredicate(m Module, p preds.Predicate) Module {
	return Stateful(func(ctx Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) {
		if p.ShouldHandle(update) {
			m.HandleUpdate(ctx, update, bot)
		}
	})
}

// Stateless creates a 'stateless' module.
// If your state is tiny(which can be captured in a closure), use this.
func Stateless(handleFunc HandleFunc, condFunc preds.Predicate) Module {
	handler := Stateful(func(_ Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) {
		handleFunc(update, bot)
	})
	return WithPredicate(handler, condFunc)
}
