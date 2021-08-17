package base

import (
	"csust-got/config"
	"csust-got/util"
	"fmt"
	"runtime"
	"time"

	. "gopkg.in/tucnak/telebot.v3"
)

// Makefile variable
var (
	version   string
	branch    string
	buildTime string
)

var lastBoot = time.Now().In(util.TimeZoneCST).Format(util.TimeFormat)

// Info - build info
func Info(ctx Context) error {
	msg := "```\n----- Bot Info -----\n"
	msg += fmt.Sprintf("UserName:    %s\n", config.BotConfig.Bot.Me.Username)
	msg += fmt.Sprintf("Version:     %s\n", version)
	msg += fmt.Sprintf("Branch:      %s\n", branch)
	msg += fmt.Sprintf("Build Time:  %s\n", buildTime)
	msg += fmt.Sprintf("Last Boot:   %s\n", lastBoot)
	msg += fmt.Sprintf("Go Version:  %s\n", runtime.Version())
	msg += "```"

	return ctx.Send(msg, ModeMarkdownV2)
}

// GetUserID is handle for command `/id`
func GetUserID(m *Message) {
	msg := fmt.Sprintf("Your userID is %d", m.Sender.ID)
	util.SendReply(m.Chat, msg, m)
}

// GetChatID is handle for command `/cid`
func GetChatID(ctx Context) error {
	msg := fmt.Sprintf("Current chatID is %d", ctx.Chat().ID)
	return ctx.Reply(msg)
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
