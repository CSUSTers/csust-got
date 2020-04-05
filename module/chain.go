package module

import (
	"csust-got/context"
	"csust-got/module/preds"
	"fmt"
	"github.com/go-telegram-bot-api/telegram-bot-api"
)

type chainedModules []Module

func (c chainedModules) HandleUpdate(context context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) HandleResult {
	deferredOnly := false
	for i, module := range c {
		if deferredOnly {
			if _, ok := module.(deferredModule); !ok {
				continue
			}
		}

		name := fmt.Sprint(i)
		if n, ok := module.(namedModule); ok {
			name = n.Name()
		}
		ctx := context.SubContext(name)
		result := module.HandleUpdate(ctx, update, bot)
		switch result {
		case NoMore:
			return NoMore
		case DoDeferred:
			deferredOnly = true
		default:
		}
	}
	return NextOfChain
}

type parallelModules []Module

func (p parallelModules) HandleUpdate(context context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) HandleResult {
	resultChan := make(chan HandleResult, len(p))
	for i, module := range p {
		name := fmt.Sprint(i)
		if n, ok := module.(namedModule); ok {
			name = n.Name()
		}
		ctx := context.SubContext(name)
		m := module
		go func() {
			resultChan <- m.HandleUpdate(ctx, update, bot)
		}()
	}
	for r := range resultChan {
		if r == NextOfChain {
			return NextOfChain
		}
	}
	return NoMore
}

type sharedContext []Module

func (s sharedContext) HandleUpdate(context context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) HandleResult {
	for _, module := range s {
		if module.HandleUpdate(context, update, bot) == NoMore {
			return NoMore
		}
	}
	return NextOfChain
}

// Sequential chains a list of modules and will break when HandleUpdate returns NoMore.
func Sequential(group []Module) Module {
	return chainedModules(group)
}

// Parallel executes a list of modules concurrently and will NOT break when HandleUpdate returns NoMore.
func Parallel(group []Module) Module {
	return parallelModules(group)
}

// SharedContext makes a group of modules execute SEQUENTIAL and share the exact one Context.
func SharedContext(group []Module) Module {
	return sharedContext(group)
}

// BlockWhen blocks next of chain when the predicate returns false.
func BlockWhen(predicate preds.Predicate) Module {
	return trivialModule{handleUpdate: func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) HandleResult {
		if predicate.ShouldHandle(update) {
			return NextOfChain
		}
		return NoMore
	}}
}

// Filter crates a Module that can block next of chain by its return value.
func Filter(f ChainedHandleFunc) Module {
	return trivialModule{f}
}

type namedModule interface {
	Module
	Name() string
}

type trivialNamedModule struct {
	module Module
	name   string
}

type deferredModule struct {
	Module
}

// HandleUpdate implements Module.
func (t trivialNamedModule) HandleUpdate(context context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) HandleResult {
	return t.module.HandleUpdate(context, update, bot)
}

// Name implements namedModule.
func (t trivialNamedModule) Name() string {
	return t.name
}

// NamedModule creates a module that will has specified name in its context when using
// Sequential or Parallel.
func NamedModule(module Module, name string) Module {
	return trivialNamedModule{
		module: module,
		name:   name,
	}
}

func DeferredModule(module Module) Module {
	return deferredModule{module}
}
