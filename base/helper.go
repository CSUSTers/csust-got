package base

import (
	"fmt"
	"runtime"
	"time"

	"csust-got/config"
	"csust-got/orm"
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
	msg += "\n"
	msg += currentBotStatus(ctx)
	msg += "```"

	_, err := util.SendWithError(ctx, util.RawTgText(msg), ModeMarkdownV2)
	return err
}

func currentBotStatus(ctx Context) string {
	chat := ctx.Chat()
	if chat == nil {
		return formatBotStatus(false, false, false, false)
	}

	isShutdown := orm.IsShutdown(chat.ID)
	isMcDead := false
	isNoSticker := false
	if chat.Type == ChatGroup || chat.Type == ChatSuperGroup {
		isMcDead, _ = orm.IsMcDead(chat.ID)
		isNoSticker = orm.IsNoStickerMode(chat.ID)
	}
	isByeWorld := false
	if sender := ctx.Sender(); sender != nil {
		_, isByeWorld, _ = orm.IsByeWorld(chat.ID, sender.ID)
	}

	return formatBotStatus(isShutdown, isMcDead, isNoSticker, isByeWorld)
}

func formatBotStatus(isShutdown bool, isMcDead bool, isNoSticker bool, isByeWorld bool) string {
	msg := "----- Bot Status -----\n"
	if isShutdown {
		msg += "Health:      因shutdown命令关机\n"
		msg += "Recover:     使用/boot命令开机\n"
		return msg
	}
	if isMcDead {
		msg += "Health:      因mc命令死亡\n"
		msg += "Recover:     使用/reburn命令复活\n"
		return msg
	}
	if isNoSticker {
		msg += "Health:      因no_sticker命令禁用贴纸\n"
		msg += "Recover:     使用/no_sticker命令关闭禁贴纸\n"
		return msg
	}
	if isByeWorld {
		msg += "Health:      因bye_world命令自动删除你的消息\n"
		msg += "Recover:     使用/hello_world命令关闭自动删除\n"
		return msg
	}
	msg += "Health:      OK\n"
	return msg
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
