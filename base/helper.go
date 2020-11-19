package base

import (
	"csust-got/util"
	"fmt"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
)

// GetUserID is handle for command `/id`
func GetUserID(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	message := update.Message
	chatID := message.Chat.ID

	msg := "Private chat, please"
	if message.Chat.IsPrivate() {
		msg = fmt.Sprintf("Your userID is %d", message.From.ID)
	}

	messageReply := tgbotapi.NewMessage(chatID, msg)
	messageReply.ReplyToMessageID = message.MessageID

	util.SendMessage(bot, messageReply)
}

// GetChatID is handle for command `/cid`
func GetChatID(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	message := update.Message
	chatID := message.Chat.ID

	msg := fmt.Sprintf("Current chatID is %d", message.Chat.ID)

	messageReply := tgbotapi.NewMessage(chatID, msg)
	messageReply.ReplyToMessageID = message.MessageID

	util.SendMessage(bot, messageReply)
}
