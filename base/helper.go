package base

import (
	"fmt"
	"runtime"
	"time"

	"csust-got/config"
	"csust-got/util"

	. "gopkg.in/telebot.v3"
)

// Makefile variable.
var (
	version   string
	branch    string
	buildTime string
)

var lastBoot = time.Now().In(util.TimeZoneCST).Format(util.TimeFormat)

// Info - build info.
func Info(ctx Context) error {
	msg := "```\n----- Bot Info -----\n"
	msg += fmt.Sprintf("UserName:    %s\n", config.BotConfig.Bot.Me.Username)
	msg += fmt.Sprintf("Version:     %s\n", version)
	msg += fmt.Sprintf("Branch:      %s\n", branch)
	msg += fmt.Sprintf("Build Time:  %s\n", buildTime)
	msg += fmt.Sprintf("Last Boot:   %s\n", lastBoot)
	msg += fmt.Sprintf("Go Version:  %s\n", runtime.Version())
	if ctx.Bot().URL != DefaultApiURL {
		msg += "API Server:  CUSTOM\n"
	} else {
		msg += "API Server:  OFFICIAL\n"
	}
	if config.BotConfig.DebugMode {
		msg += "Debug Mode:  YES\n"
	}
	msg += "```"

	return ctx.Send(msg, ModeMarkdownV2)
}

// GetUserID is handle for command `/id`.
func GetUserID(ctx Context) error {
	msg := fmt.Sprintf("Your userID is %d", ctx.Sender().ID)
	return ctx.Reply(msg)
}

// GetChatID is handle for command `/cid`.
func GetChatID(ctx Context) error {
	msg := fmt.Sprintf("Current chatID is %d", ctx.Chat().ID)
	return ctx.Reply(msg)
}

// DoNothing is a handler do nothing
// It just a placeholder for some handle endpoint, let the bot know
// it should process this update, then the update can be processed in middleware.
func DoNothing(ctx Context) error {
	return nil
}
