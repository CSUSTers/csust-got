package util

import (
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"log"
	"strings"
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

func GetUserNameFromString(s string) (string, bool) {
	if len(s) > 1 && strings.HasPrefix(s, "@") {
		return strings.Trim(s, "@"), true
	}
	return "", false
}


func GetAdminList(bot *tgbotapi.BotAPI, chatID int64) []tgbotapi.ChatMember {
	admins, err := bot.GetChatAdministrators(tgbotapi.ChatConfig{
		ChatID:             chatID,
	})
	if err != nil {
		return []tgbotapi.ChatMember{}
	}
	return admins
}


func CanRestrictMembers(bot *tgbotapi.BotAPI, chatID int64, userID int) bool {
	admins := GetAdminList(bot, chatID)
	for _, v := range admins {
		if v.User.ID == userID && v.CanRestrictMembers {
			return true
		}
	}
	return false
}