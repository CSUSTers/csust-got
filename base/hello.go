package base

import (
	"csust-got/module"
	"csust-got/module/preds"
	"csust-got/util"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
)

func Hello(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	message := update.Message
	chatID := message.Chat.ID

	messageReply := tgbotapi.NewMessage(chatID, "hello ^_^")

	// 如果消息来自群里，但并不是由命令触发的，就以reply的形式发送
	if message.Chat.IsGroup() && !message.IsCommand() {
		messageReply.ReplyToMessageID = message.MessageID
	}

	util.SendMessage(bot, messageReply)
}


func HelloToAll(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	message := update.Message
	chatID := message.Chat.ID

	text := "大家好!"
	if !message.Chat.IsGroup() {
		text = "你好!"
	}
	text += "我是大五，大五的大，大五的wu"

	messageReply := tgbotapi.NewMessage(chatID, text)
	util.SendMessage(bot, messageReply)
}


func IsoHello(tgbotapi.Update) module.Module {
	running := false
	handle := func(u tgbotapi.Update, b *tgbotapi.BotAPI) {
		msg := tgbotapi.NewMessage(u.Message.Chat.ID, "Hello ^_^")
		util.SendMessage(b, msg)
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
