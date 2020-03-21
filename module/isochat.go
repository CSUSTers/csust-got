package module

import (
	"github.com/go-telegram-bot-api/telegram-bot-api"
)

type Factory func(update tgbotapi.Update) Module

type isolatedChatModule struct {
	// Key: chat id
	// Value: handleModule
	registeredMods map[int64]Module
	factory        Factory
	shouldRegister func(update tgbotapi.Update) bool
}

func (i *isolatedChatModule) HandleUpdate(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	chat := update.Message.Chat
	handler := i.registeredMods[chat.ID]
	handler.HandleUpdate(update, bot)
}

func (i *isolatedChatModule) ShouldHandle(update tgbotapi.Update) bool {
	chat := update.Message.Chat
	// Registered chat.
	if module, ok := i.registeredMods[chat.ID]; ok {
		return module.ShouldHandle(update)
	}
	// Not yet registered chat, but we should register now.
	if i.shouldRegister == nil || i.shouldRegister(update) {
		module := i.factory(update)
		i.registeredMods[chat.ID] = module
		return module.ShouldHandle(update)
	}
	return false
}

// IsolatedChat returns a Module that will, for each incoming update, split it by
func IsolatedChat(factory Factory, shouldRegister HandleCondFunc) Module {
	return &isolatedChatModule{
		registeredMods: make(map[int64]Module),
		factory:        factory,
		shouldRegister: shouldRegister,
	}
}
