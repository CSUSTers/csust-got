package module

import (
	"csust-got/context"
	"fmt"
	"github.com/go-telegram-bot-api/telegram-bot-api"
)

type chainedModules struct {
	modules []Module
}

func (c chainedModules) HandleUpdate(context context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) HandleResult {
	for i, module := range c.modules {
		ctx := context.SubContext(fmt.Sprint(i))
		if module.HandleUpdate(ctx, update, bot) == NoMore {
			return NoMore
		}
	}
	return NextOfChain
}

// Chained chain a list of modules and will break when
func Chained(group []Module) Module {
	return chainedModules{group}
}
