package module

import (
	"csust-got/context"
	"csust-got/module/preds"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
)

// HandleResult is the result type of HandleUpdate.
// it indecies whether and how the next module should be handled.
type HandleResult int

const (
	// NextOfChain indices that the next sequenced module should be executed.
	NextOfChain HandleResult = iota
	// NoMore breaks the sequenced module chain immediately, without execute deferred modules.
	NoMore
	// DoDeferred like NoMore, break the sequenced module chain, but deferred modules will be executed.
	DoDeferred
)

// Module is the basic abstract of this library.
// one module handles one update.
// modules can be composed by some combinator, like:
// Sequential, Parallel.
// modules can be made by some wrappers, like:
// Stateless, Stateful, InteractModule.
type Module interface {
	// HandleUpdate should handle a update, and return whether the next handler of chain should be handed.
	// Note your module might be executed 'parallel' default, which will ignore your returning value.
	// If you want to register a 'chain of filter', use `Chain` please.
	HandleUpdate(context context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) HandleResult
}

// HandleFunc is the 'stateless' handle function.
type HandleFunc func(update tgbotapi.Update, bot *tgbotapi.BotAPI)

// StatefulHandleFunc is 'stateful' handle function, which means, you can access Context though handling update.
type StatefulHandleFunc func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI)

// ChainedHandleFunc is 'filter' handle function, which means, it can interrupt following modules by its return value.
type ChainedHandleFunc func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) HandleResult

type trivialModule struct {
	handleUpdate ChainedHandleFunc
}

func (t trivialModule) HandleUpdate(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) HandleResult {
	return t.handleUpdate(ctx, update, bot)
}

// Stateful warps a stateful function to a Module.
func Stateful(f StatefulHandleFunc) Module {
	return trivialModule{handleUpdate: func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) HandleResult {
		f(ctx, update, bot)
		return NextOfChain
	}}
}

// WithPredicate warps a Module with specified Predicate.
// The method `handleUpdate` will only be invoked when the Predicate is true.
func WithPredicate(m Module, p preds.Predicate) Module {
	return Filter(func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) HandleResult {
		if p.ShouldHandle(update) {
			return m.HandleUpdate(ctx, update, bot)
		}
		return NextOfChain
	})
}

// Stateless creates a 'stateless' module.
// If your state is tiny(which can be captured in a closure), use this.
func Stateless(handleFunc HandleFunc, condFunc preds.Predicate) Module {
	handler := Stateful(func(_ context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) {
		handleFunc(update, bot)
	})
	return WithPredicate(handler, condFunc)
}
