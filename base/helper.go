package base

import (
	"csust-got/util"
	"fmt"
	"runtime"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
)

// Makefile variable
var (
	version string
	branch string
	buildTime string
)

// Info - build info
func Info(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	msg := "Bot Info\n"
	msg += fmt.Sprintf("Version: %s\n", version)
	msg += fmt.Sprintf("Branch: %s\n", branch)
	msg += fmt.Sprintf("Build Time: %s\n", buildTime)
	msg += fmt.Sprintf("Go Version: %s\n", runtime.Version())

	messageReply := tgbotapi.NewMessage(update.Message.Chat.ID, msg)
	util.SendMessage(bot, messageReply)
}

// GetUserID is handle for command `/id`
func GetUserID(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	message := update.Message
	chatID := message.Chat.ID

	msg := "这条命令会返回你的UserID，请不要在群里使用"
	if message.Chat.IsPrivate() {
		msg = fmt.Sprintf("Your userID is %d", message.From.ID)
	}

	messageReply := tgbotapi.NewMessage(chatID, msg)
	messageReply.ReplyToMessageID = message.MessageID

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
