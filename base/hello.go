package base

import (
	"csust-got/context"
	"csust-got/module"
	"csust-got/module/preds"
	"csust-got/util"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"log"
)


// Hello is handle for command `hello`
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


// HelloToAll is handle for command `hello_to_all`
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


// IsoHello is handle for auto hello to someone, just for test, we not use it.
func IsoHello(tgbotapi.Update) module.Module {
	handle := func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) {
		enable, err := ctx.GlobalClient().Get(ctx.WrapKey("enabled")).Int()
		enabled := enable > 0
		if err != nil {
			log.Println("ERROR: failed to access redis.", err)
		}

		if preds.IsCommand("hello").ShouldHandle(update) {
			var newI int
			if enabled {
				newI = 0
			} else {
				newI = 1
			}
			ctx.GlobalClient().Set(ctx.WrapKey("enabled"), newI, 0)
		}

		if enabled {
			util.SendMessage(bot, tgbotapi.NewMessage(update.Message.Chat.ID, "hello ……——……"))
		}
	}
	return module.Stateful(handle)
}
