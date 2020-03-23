package module

import (
	"csust-got/context"
	"fmt"
	"github.com/go-telegram-bot-api/telegram-bot-api"
	"log"
)

type chainedModules []Module

func (c chainedModules) HandleUpdate(context context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) HandleResult {
	for i, module := range c {
		ctx := context.SubContext(fmt.Sprint(i))
		if module.HandleUpdate(ctx, update, bot) == NoMore {
			return NoMore
		}
	}
	return NextOfChain
}

type parallelModules []Module

func (p parallelModules) HandleUpdate(context context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) HandleResult {
	resultChan := make(chan HandleResult, len(p))
	for i, module := range p {
		ctx := context.SubContext(fmt.Sprint(i))
		m := module
		log.Printf("parallelModules: Send to subcontext %v\n", ctx)
		go func() {
			resultChan <- m.HandleUpdate(ctx, update, bot)
		}()
	}
	for r := range resultChan {
		log.Printf("parallelModules: receive from handler %#v\n", r)
		if r == NoMore {
			return NoMore
		}
	}
	return NextOfChain
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
