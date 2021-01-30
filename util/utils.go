package util

import (
	"csust-got/config"
	"csust-got/log"
	. "gopkg.in/tucnak/telebot.v2"
	"math/rand"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

// ParseNumberAndHandleError is used to get a number from string or reply a error msg when get error
func ParseNumberAndHandleError(m *Message, ns string, rng RangeInt) (number int, ok bool) {
	// message id is a int-type number
	id, err := strconv.Atoi(ns)
	if err != nil {
		SendReply(m.Chat, "您这数字有点不太对劲啊。要不您回去再瞅瞅？", m)
		ok = false
	} else if !rng.IsEmpty() && !rng.Cover(id) {
		SendReply(m.Chat, "太大或是太小，都不太行。适合的，才是坠吼的。", m)
		ok = false
	} else {
		return id, true
	}
	return
}

// SendMessage will use the bot to send a message.
func SendMessage(to Recipient, what interface{}, ops ...interface{}) *Message {
	msg, _ := SendMessageWithError(to, what, ops...)
	return msg
}

// SendReply will use the bot to reply a message.
func SendReply(to Recipient, what interface{}, replyMsg *Message, ops ...interface{}) *Message {
	ops = append(ops, &SendOptions{ReplyTo: replyMsg})
	return SendMessage(to, what, ops...)
}

// SendMessageWithError is same as SendMessage but return error
func SendMessageWithError(to Recipient, what interface{}, ops ...interface{}) (*Message, error) {
	msg, err := config.BotConfig.Bot.Send(to, what, ops...)
	if err != nil {
		log.Error("Can't send message", zap.Error(err))
	}
	return msg, err
}

// SendReplyWithError is same as SendReply but return error
func SendReplyWithError(to Recipient, what interface{}, replyMsg *Message, ops ...interface{}) (*Message, error) {
	ops = append(ops, &SendOptions{ReplyTo: replyMsg})
	return SendMessageWithError(to, what, ops...)
}

// DeleteMessage delete a message
func DeleteMessage(m *Message) {
	err := config.BotConfig.Bot.Delete(m)
	if err != nil {
		log.Error("Can't delete message", zap.Error(err))
	}
}

// GetName can get user's name
func GetName(user *User) string {
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
func GetAdminList(chatID int64) []ChatMember {
	chat := &Chat{ID: chatID}
	admins, err := config.BotConfig.Bot.AdminsOf(chat)
	if err != nil {
		log.Error("Can't get admin list", zap.Int64("chatID", chatID), zap.Error(err))
		return []ChatMember{}
	}
	return admins
}

// CanRestrictMembers can check if someone can restrict members
func CanRestrictMembers(chat *Chat, user *User) bool {
	member, err := config.BotConfig.Bot.ChatMemberOf(chat, user)
	if err != nil {
		log.Error("can get CanRestrictMembers", zap.Int64("chatID", chat.ID),
			zap.Int("userID", user.ID), zap.Error(err))
		return false
	}
	return member.CanRestrictMembers
}

// GetChatMember can get chat member from chat.
// func GetChatMember(bot *tgbotapi.BotAPI, chatID int64, userID int) ChatMember {
// 	chatMember, err := bot.GetChatMember(tgbotapi.ChatConfigWithUser{
// 		ChatID: chatID,
// 		UserID: userID,
// 	})
// 	if err != nil {
// 		log.Error("GetChatMember failed", zap.Error(err))
// 	}
// 	return chatMember
// }

// RandomChoice - rand one from slice
func RandomChoice(s []string) string {
	if len(s) == 0 {
		return ""
	}
	idx := rand.Intn(len(s))
	return s[idx]
}

// StringsToInts parse []string to []int64
func StringsToInts(s []string) []int64 {
	res := make([]int64, 0)
	for _, v := range s {
		i, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			log.Error("parse str to int failed", zap.String("value", v))
			continue
		}
		res = append(res, i)
	}
	return res
}

func PrivateCommand(fn func(m *Message)) func(m *Message) {
	return func(m *Message) {
		if m.FromGroup() {
			SendReply(m.Chat, "命令不支持群里使用哦", m)
			return
		}
		fn(m)
	}
}

func GroupCommand(fn func(m *Message)) func(m *Message) {
	return func(m *Message) {
		if m.Private() {
			SendReply(m.Chat, "命令不支持私聊使用哦", m)
			return
		}
		fn(m)
	}
}
