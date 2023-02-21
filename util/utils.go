package util

import (
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"csust-got/config"
	"csust-got/log"

	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

// ParseNumberAndHandleError is used to get a number from string or reply a error msg when get error.
func ParseNumberAndHandleError(m *tb.Message, ns string, rng IRange[int]) (number int, ok bool) {
	// message id is a int-type number
	id, err := strconv.Atoi(ns)
	if err != nil {
		SendReply(m.Chat, "您这数字有点不太对劲啊。要不您回去再瞅瞅？", m)
		return 0, false
	}
	if rng.IsEmpty() || rng.Cover(id) {
		return id, true
	}
	SendReply(m.Chat, "太大或是太小，都不太行。适合的，才是坠吼的。", m)
	return id, false
}

// SendMessage will use the bot to send a message.
func SendMessage(to tb.Recipient, what interface{}, ops ...interface{}) *tb.Message {
	msg, _ := SendMessageWithError(to, what, ops...)
	return msg
}

// SendReply will use the bot to reply a message.
func SendReply(to tb.Recipient, what interface{}, replyMsg *tb.Message, ops ...interface{}) *tb.Message {
	ops = append([]interface{}{&tb.SendOptions{ReplyTo: replyMsg}}, ops...)
	return SendMessage(to, what, ops...)
}

// SendMessageWithError is same as SendMessage but return error.
func SendMessageWithError(to tb.Recipient, what interface{}, ops ...interface{}) (*tb.Message, error) {
	msg, err := config.BotConfig.Bot.Send(to, what, ops...)
	if err != nil {
		log.Error("Can't send message", zap.Error(err))
	}
	return msg, err
}

// EditMessage edit bot's message.
func EditMessage(m *tb.Message, what interface{}, ops ...interface{}) *tb.Message {
	msg, _ := EditMessageWithError(m, what, ops...)
	return msg
}

// EditMessageWithError is same as EditMessage but return error.
func EditMessageWithError(m *tb.Message, what interface{}, ops ...interface{}) (*tb.Message, error) {
	msg, err := config.GetBot().Edit(m, what, ops...)
	if err != nil {
		log.Error("Can't edit message", zap.Error(err))
	}
	return msg, err
}

// SendReplyWithError is same as SendReply but return error.
func SendReplyWithError(to tb.Recipient, what interface{}, replyMsg *tb.Message, ops ...interface{}) (*tb.Message, error) {
	ops = append([]interface{}{&tb.SendOptions{ReplyTo: replyMsg}}, ops...)
	return SendMessageWithError(to, what, ops...)
}

// DeleteMessage delete a message.
func DeleteMessage(m *tb.Message) {
	err := config.BotConfig.Bot.Delete(m)
	if err != nil {
		log.Error("Can't delete message", zap.Error(err))
	}
}

// GetName can get user's name.
func GetName(user *tb.User) string {
	name := user.FirstName
	if user.LastName != "" {
		name += " " + user.LastName
	}
	return name
}

// GetUserNameFromString can get userName from message text.
func GetUserNameFromString(s string) (string, bool) {
	if len(s) > 1 && strings.HasPrefix(s, "@") {
		return strings.Trim(s, "@"), true
	}
	return "", false
}

// GetAdminList can get admin list from chat.
func GetAdminList(chatID int64) []tb.ChatMember {
	chat := &tb.Chat{ID: chatID}
	admins, err := config.BotConfig.Bot.AdminsOf(chat)
	if err != nil {
		log.Error("Can't get admin list", zap.Int64("chatID", chatID), zap.Error(err))
		return []tb.ChatMember{}
	}
	return admins
}

// CanRestrictMembers can check if someone can restrict members.
func CanRestrictMembers(chat *tb.Chat, user *tb.User) bool {
	member, err := config.BotConfig.Bot.ChatMemberOf(chat, user)
	if err != nil {
		log.Error("can get CanRestrictMembers", zap.Int64("chatID", chat.ID),
			zap.Int64("userID", user.ID), zap.Error(err))
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

// RandomChoice - rand one from slice.
func RandomChoice[T any](s []T) T {
	var ret T
	if len(s) == 0 {
		return ret
	}
	idx := rand.Intn(len(s))
	return s[idx]
}

// StringsToInts parse []string to []int64.
func StringsToInts(s []string) []int64 {
	res := make([]int64, 0, len(s))
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

// PrivateCommand warp command to private call only.
func PrivateCommand(fn tb.HandlerFunc) tb.HandlerFunc {
	return func(ctx tb.Context) error {
		if ctx.Chat().Type != tb.ChatPrivate {
			return ctx.Reply("这个命令只能私聊使用哦")
		}
		return fn(ctx)
	}
}

// GroupCommand warp command to group call only.
func GroupCommand(fn func(m *tb.Message)) tb.HandlerFunc {
	return func(ctx tb.Context) error {
		if ctx.Chat().Type == tb.ChatPrivate {
			return ctx.Reply("这个命令不支持私聊使用哦")
		}
		fn(ctx.Message())
		return nil
	}
}

// IsNumber check rune is number.
func IsNumber(r rune) bool {
	return unicode.IsNumber(r)
}

// IsUpper check rune is upper.
func IsUpper(r rune) bool {
	return unicode.IsUpper(r)
}

// IsLower check rune is lower.
func IsLower(r rune) bool {
	return unicode.IsLower(r)
}

// ReplaceSpace replace all empty chars in a string with the escape char
func ReplaceSpace(in string) string {
	patt := regexp.MustCompilePOSIX(`[\s\n]`)
	return patt.ReplaceAllStringFunc(in, func(s string) string {
		var r string
		switch s {
		case " ":
			r = " "
		case "\n":
			r = `\n`
		case "\t":
			r = `\t`
		default:
			r = " "
		}
		return r
	})
}
