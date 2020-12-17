package util

import (
	"math/rand"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"go.uber.org/zap"
)

// ParseNumberAndHandleError is used to get a number from string or reply a error msg when get error
func ParseNumberAndHandleError(bot *tgbotapi.BotAPI, message *tgbotapi.Message,
	ns string, rng RangeInt) (number int, ok bool) {
	chatID := message.Chat.ID

	// message id is a int-type number
	id, err := strconv.Atoi(ns)
	if err != nil {
		msg := tgbotapi.NewMessage(chatID, "您这数字有点不太对劲啊。要不您回去再瞅瞅？")
		msg.ReplyToMessageID = message.MessageID
		SendMessage(bot, msg)
		ok = false
	} else if !rng.IsEmpty() && !rng.Cover(id) {
		msg := tgbotapi.NewMessage(chatID, "太大或是太小，都不太行。适合的，才是坠吼的。")
		msg.ReplyToMessageID = message.MessageID
		SendMessage(bot, msg)
		ok = false
	}
	return id, true
}

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

// RandomChoice - rand one from slice
func RandomChoice(s []string) string {
	if len(s) == 0 {
		return ""
	}
	idx := rand.Intn(len(s))
	return s[idx]
}
