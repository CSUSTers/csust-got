package main

import (
	"csust-got/base"
	"csust-got/config"
	"csust-got/manage"
	"csust-got/module"
	"csust-got/module/preds"
	"csust-got/orm"
	"github.com/go-telegram-bot-api/telegram-bot-api"
	"log"
)

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
		{module.IsolatedChat(base.IsoHello, preds.IsCommand("hello")), ctx.SubContext("hello")},
		{module.Stateless(base.Hello, preds.IsCommand("say_hello")), ctx.SubContext("say hello")},
		{module.Stateless(base.WelcomeNewMember, preds.NonEmpty), ctx.SubContext("welcome")},
		{module.Stateless(base.HelloToAll, preds.IsCommand("hello_to_all")), ctx.SubContext("hello to all")},
		{module.IsolatedChat(manage.NoSticker, preds.IsCommand("no_sticker")), ctx.SubContext("no sticker")},
		{module.IsolatedChat(manage.DeleteSticker, preds.HasSticker), ctx.SubContext("delete sticker")},
	}
	for update := range updates {
		for _, handle := range handles {
			go handle.mod.HandleUpdate(handle.ctx, update, bot)
		}
	}
}
