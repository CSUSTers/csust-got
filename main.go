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
	stickerContext := ctx.SubContext("no sticker")
	handles := []struct {
		mod module.Module
		ctx module.Context
	}{
		//{module.IsolatedChat(base.IsoHello), ctx.SubContext("hello")},
		{module.Stateless(base.Hello, preds.IsCommand("say_hello")), ctx.SubContext("say_hello")},
		{module.Stateless(base.WelcomeNewMember, preds.NonEmpty), ctx.SubContext("welcome")},
		{module.Stateless(base.HelloToAll, preds.IsCommand("hello_to_all")), ctx.SubContext("hello_to_all")},
		{module.WithPredicate(module.IsolatedChat(manage.NoSticker), preds.IsCommand("no_sticker")), stickerContext},
		{module.WithPredicate(module.IsolatedChat(manage.DeleteSticker), preds.HasSticker), stickerContext},
	}
	for update := range updates {
		for _, handle := range handles {
			go handle.mod.HandleUpdate(handle.ctx, update, bot)
		}
	}
}
