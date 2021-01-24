package base

import (
	"csust-got/log"
	"csust-got/prom"
	"csust-got/util"
	"fmt"
	"go.uber.org/zap"
	"runtime"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
)

// Makefile variable
var (
	version   string
	branch    string
	buildTime string
)

var lastBoot = time.Now().In(timeZoneCST).Format("2006/01/02-15:04:05")

// Info - build info
func Info(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	msg := "```\n----- Bot Info -----\n"
	msg += fmt.Sprintf("UserName:    %s\n", bot.Self.UserName)
	msg += fmt.Sprintf("Version:     %s\n", version)
	msg += fmt.Sprintf("Branch:      %s\n", branch)
	msg += fmt.Sprintf("Build Time:  %s\n", buildTime)
	msg += fmt.Sprintf("Last Boot:   %s\n", lastBoot)
	msg += fmt.Sprintf("Go Version:  %s\n", runtime.Version())
	msg += "```"

	messageReply := tgbotapi.NewMessage(update.Message.Chat.ID, msg)
	messageReply.ParseMode = tgbotapi.ModeMarkdownV2
	util.SendMessage(bot, messageReply)
}

// GetUserID is handle for command `/id`
func GetUserID(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	message := update.Message
	chatID := message.From.ID

	// chatID of private chat is userID
	msg := fmt.Sprintf("Your userID is %d", chatID)

	// send to user in private chat
	messageReply := tgbotapi.NewMessage(int64(chatID), msg)

	if update.Message.Chat.IsPrivate() {
		messageReply.ReplyToMessageID = message.MessageID
	}

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

// GetGroupMember get how many members in group
func GetGroupMember(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	chat := update.Message.Chat
	if chat.IsPrivate() {
		return
	}
	num, err := bot.GetChatMembersCount(chat.ChatConfig())
	if err != nil {
		log.Error("GetChatMembersCount failed", zap.Int64("chatID", chat.ID), zap.Error(err))
		return
	}
	prom.GetMember(chat.Title, num)
}
