package base

import (
	"csust-got/util"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
)

// WelcomeNewMember is handle for welcome new member.
// when someone new join group, bot will send welcome message.
func WelcomeNewMember(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	message := update.Message
	memberSlice := message.NewChatMembers
	if memberSlice == nil {
		return
	}
	for _, member := range *memberSlice {
		text := "Welcome to this group!" + util.GetName(member)
		message := tgbotapi.NewMessage(message.Chat.ID, text)
		util.SendMessage(bot, message)
	}
}
