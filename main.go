package main

import (
	"csust-got/base"
	"csust-got/config"
	"csust-got/module"
	"csust-got/module/preds"
	"csust-got/orm"
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
		preds.IsCommand("hello").SideEffectOnTrue(toggleRunning).
			Or(preds.BoolFunction(getRunning)))
}

func main() {
	bot, err := tgbotapi.NewBotAPI(config.BotConfig.Token)
	if err != nil {
		log.Panic(err)
	}

	bot.Debug = config.BotConfig.DebugMode

	log.Printf("Authorized on account %s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates, err := bot.GetUpdatesChan(u)

	ctx := module.GlobalContext(orm.GetClient(), config.BotConfig)
	handles := []struct {
		mod module.Module
		ctx module.Context
	}{
		{module.IsolatedChat(hello, preds.IsCommand("hello")), ctx.SubContext("hello")},
		{module.Stateless(base.Hello, preds.IsCommand("say_hello")), ctx.SubContext("sayhello")},
		{module.Stateless(base.WelcomeNewMember, preds.NonEmpty), ctx.SubContext("welcome")},
	}
	for update := range updates {
		for _, handle := range handles {
			go handle.mod.HandleUpdate(handle.ctx, update, bot)
		}
	}
}
