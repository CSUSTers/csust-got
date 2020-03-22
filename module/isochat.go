package module

import (
	"csust-got/module/preds"
	"fmt"
	"github.com/go-telegram-bot-api/telegram-bot-api"
)

type Factory func(update tgbotapi.Update) Module

type isolatedChatModule struct {
	// Key: chat id
	// Value: handleModule
	registeredMods map[int64]Module
	factory        Factory
	shouldRegister preds.Predicate
}

func (i *isolatedChatModule) HandleUpdate(context Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	// Registered chat.
	chat := update.Message.Chat
	newCtx := context.SubContext(fmt.Sprint(chat.ID))
	if module, ok := i.registeredMods[chat.ID]; ok {
		module.HandleUpdate(newCtx, update, bot)
	}
	if i.shouldRegister.ShouldHandle(update) {
		module := i.factory(update)
		i.registeredMods[chat.ID] = module
		module.HandleUpdate(newCtx, update, bot)
	}
}

// IsolatedChat returns a Module that will, for each incoming update, split it by chatID.
// 对于每一个 update，IsolatedChat 将会借助 factory 创建单独的一个模块进行处理。
// 注意仅当 shouldRegister 返回为真的时候，我们才会真正地创建模块。
func IsolatedChat(factory Factory, shouldRegister preds.Predicate) Module {
	return &isolatedChatModule{
		registeredMods: make(map[int64]Module),
		factory:        factory,
		shouldRegister: shouldRegister,
	}
}
