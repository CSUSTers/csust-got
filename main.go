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
		conds.IsCommand("hello").
			SideEffectOnTrue(toggleRunning).
			Or(conds.BoolFunction(getRunning)))
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
		module.IsolatedChat(func(update tgbotapi.Update) module.Module {
			return hello()
		}, conds.IsCommand("hello")),
	}
	for update := range updates {
		for _, handle := range handles {
			if handle.ShouldHandle(update) {
				go handle.HandleUpdate(update, bot)
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
