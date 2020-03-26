package util

import (
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"log"
)

// SendMessage will use the bot to send a message.
func SendMessage(bot *tgbotapi.BotAPI, message tgbotapi.Chattable) {
	_, err := bot.Send(message)
	if err != nil {
		log.Println("ERROR: Can't send message")
		log.Println(err.Error())
	}
}

func GetName(user tgbotapi.User) string {
	name := user.FirstName
	if user.LastName != "" {
		name += " " + user.LastName
	}
	return name
}
