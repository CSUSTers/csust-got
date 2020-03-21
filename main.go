package main

import (
	"csust-got/config"
	"csust-got/module"
	"csust-got/module/conds"
	"github.com/go-telegram-bot-api/telegram-bot-api"
	"log"
)

func main() {
	conf, err := config.FromFolder(".")
	if err != nil {
		log.Panic(err)
	}
	bot, err := tgbotapi.NewBotAPI(conf.Token)
	if err != nil {
		log.Panic(err)
	}

	bot.Debug = true

	log.Printf("Authorized on account %s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates, err := bot.GetUpdatesChan(u)

	handles := []module.Module{
		module.Stateless(echo, conds.NonEmpty),
	}
	for update := range updates {
		for _, handle := range handles {
			if handle.ShouldHandle(update) {
				handle.HandleUpdate(update, bot)
			}
		}
	}
}

func echo(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	log.Printf("[%s] %s", update.Message.From.UserName, update.Message.Text)

	msg := tgbotapi.NewMessage(update.Message.Chat.ID, update.Message.Text)
	msg.ReplyToMessageID = update.Message.MessageID

	bot.Send(msg)
}
