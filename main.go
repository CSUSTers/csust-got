package main

import (
	"csust-got/config"
	"csust-got/module"
	"csust-got/module/conds"
	"github.com/go-telegram-bot-api/telegram-bot-api"
	"log"
)

func hello() module.Module {
	running := false
	shouldHandle := func(u tgbotapi.Update) bool {
		recv := u.Message
		if recv != nil && recv.IsCommand() && recv.Command() == "hello" {
			running = !running
		}
		return running
	}
	handle := func(u tgbotapi.Update, b *tgbotapi.BotAPI) {
		msg := tgbotapi.NewMessage(u.Message.Chat.ID, "Hello ^_^")
		_, _ = b.Send(msg)
	}
	return module.Stateless(handle, shouldHandle)
}

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
		module.IsolatedChat(func(update tgbotapi.Update) module.Module {
			return hello()
		}, func(update tgbotapi.Update) bool {
			recv := update.Message
			return recv != nil && recv.IsCommand() && recv.Command() == "hello"
		}),
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

	_, _ = bot.Send(msg)
}
