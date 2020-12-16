package base

import (
	"csust-got/util"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
)

// WelcomeNewMember is handle for welcome new member.
func WelcomeNewMember(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	message := update.Message
	memberSlice := message.NewChatMembers
	if memberSlice == nil {
		return
	}
	for _, member := range *memberSlice {
		text := "Welcome to this group!" + util.GetName(member)
		go sendNotificationTo(bot, message.Chat.ID, text)
	}
}

func sendNotificationTo(bot *tgbotapi.BotAPI, chatID int64, text string) {
	message := tgbotapi.NewMessage(chatID, text)
	util.SendMessage(bot, message)
}
