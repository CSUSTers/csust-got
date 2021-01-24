package base

import (
	"csust-got/prom"
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
		messageR := tgbotapi.NewMessage(message.Chat.ID, text)
		util.SendMessage(bot, messageR)
		prom.NewMember(message.Chat.Title)
	}
}

// LeftMember is handle for some member left.
func LeftMember(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	message := update.Message
	member := message.LeftChatMember
	if member == nil {
		return
	}
	prom.MemberLeft(message.Chat.Title)
}
