package util

import (
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"go.uber.org/zap"
)

// SendMessage will use the bot to send a message.
func SendMessage(bot *tgbotapi.BotAPI, message tgbotapi.Chattable) {
	_, err := bot.Send(message)
	if err != nil {
		zap.L().Error("Can't send message")
		zap.L().Error(err.Error())
	}
}

// GetName can get user's name
func GetName(user tgbotapi.User) string {
	name := user.FirstName
	if user.LastName != "" {
		name += " " + user.LastName
	}
	return name
}

// GetUserNameFromString can get userName from message text
func GetUserNameFromString(s string) (string, bool) {
	if len(s) > 1 && strings.HasPrefix(s, "@") {
		return strings.Trim(s, "@"), true
	}
	return "", false
}

// GetAdminList can get admin list from chat
func GetAdminList(bot *tgbotapi.BotAPI, chatID int64) []tgbotapi.ChatMember {
	admins, err := bot.GetChatAdministrators(tgbotapi.ChatConfig{
		ChatID: chatID,
	})
	if err != nil {
		return []tgbotapi.ChatMember{}
	}
	return admins
}

// CanRestrictMembers can check if someone can restrict members
func CanRestrictMembers(bot *tgbotapi.BotAPI, chatID int64, userID int) bool {
	admins := GetAdminList(bot, chatID)
	for _, v := range admins {
		if v.User.ID == userID && (v.CanRestrictMembers || v.Status == "creator") {
			return true
		}
	}
	return false
}

// GetChatMember can get chat member from chat.
func GetChatMember(bot *tgbotapi.BotAPI, chatID int64, userID int) tgbotapi.ChatMember {
	chatMember, err := bot.GetChatMember(tgbotapi.ChatConfigWithUser{
		ChatID: chatID,
		UserID: userID,
	})
	if err != nil {
		zap.L().Error(err.Error())
	}
	return chatMember
}
