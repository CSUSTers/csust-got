package main

import (
	"csust-got/config"
	"csust-got/module"
	"csust-got/module/conds"
	"github.com/go-telegram-bot-api/telegram-bot-api"
	"log"
)

func hello(tgbotapi.Update) module.Module {
	running := false
	handle := func(u tgbotapi.Update, b *tgbotapi.BotAPI) {
		msg := tgbotapi.NewMessage(u.Message.Chat.ID, "Hello ^_^")
		_, _ = b.Send(msg)
	}
	toggleRunning := func(update tgbotapi.Update) {
		running = !running
	}
	getRunning := func(update tgbotapi.Update) bool {
		return running
	}
	return module.Stateless(handle,
		conds.IsCommand("hello").SideEffectOnTrue(toggleRunning).
			Or(conds.BoolFunction(getRunning)))
}

func main() {
	bot, err := tgbotapi.NewBotAPI(config.BotConfig.Token)
	if err != nil {
		log.Panic(err)
	}

	bot.Debug = true

	log.Printf("Authorized on account %s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates, err := bot.GetUpdatesChan(u)

	ctx := module.GlobalContext()
	handles := []struct {
		mod module.Module
		ctx module.Context
	}{
		{module.IsolatedChat(hello, conds.IsCommand("hello")), ctx.SubContext("hello")},
	}
	for update := range updates {
		for _, handle := range handles {
			if handle.mod.ShouldHandle(handle.ctx, update) {
				go handle.mod.HandleUpdate(handle.ctx, update, bot)
			}
		}
	}
}
