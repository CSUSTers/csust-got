package base

import (
	"csust-got/config"
	"csust-got/util"
	"fmt"
	. "gopkg.in/tucnak/telebot.v2"
	"runtime"
	"time"
)

// Makefile variable
var (
	version   string
	branch    string
	buildTime string
)

var lastBoot = time.Now().In(timeZoneCST).Format("2006/01/02-15:04:05")

// Info - build info
func Info(m *Message) {
	msg := "```\n----- Bot Info -----\n"
	msg += fmt.Sprintf("UserName:    %s\n", config.BotConfig.Bot.Me.Username)
	msg += fmt.Sprintf("Version:     %s\n", version)
	msg += fmt.Sprintf("Branch:      %s\n", branch)
	msg += fmt.Sprintf("Build Time:  %s\n", buildTime)
	msg += fmt.Sprintf("Last Boot:   %s\n", lastBoot)
	msg += fmt.Sprintf("Go Version:  %s\n", runtime.Version())
	msg += "```"

	util.SendMessage(m.Chat, msg)
}

// GetUserID is handle for command `/id`
func GetUserID(m *Message) {
	msg := fmt.Sprintf("Your userID is %d", m.Sender.ID)
	util.SendReply(m.Chat, msg, m)
}

// GetChatID is handle for command `/cid`
func GetChatID(m *Message) {
	msg := fmt.Sprintf("Current chatID is %d", m.Chat.ID)
	util.SendReply(m.Chat, msg, m)
}

// GetGroupMember get how many members in group
// func GetGroupMember() {
// 	chat := update.Message.Chat
// 	if chat.IsPrivate() {
// 		return
// 	}
// 	num, err := bot.GetChatMembersCount(chat.ChatConfig())
// 	if err != nil {
// 		log.Error("GetChatMembersCount failed", zap.Int64("chatID", chat.ID), zap.Error(err))
// 		return
// 	}
// 	prom.GetMember(chat.Title, num)
// }
