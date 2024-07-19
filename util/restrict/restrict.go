package restrict

import (
	"csust-got/config"
	"csust-got/log"
	"csust-got/util"
	"time"

	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

// Type is used to distinguish between ban and kill.
type Type int

const (
	// RestrictTypeBan is ban.
	RestrictTypeBan Type = iota
	// RestrictTypeKill is kill.
	RestrictTypeKill
)

// Result is result of `BanOrKill`.
type Result struct {
	Success  bool
	Duration time.Duration
	Until    time.Time
	IsAppend bool
	Type
}

func success(d time.Duration, until time.Time, isAppend bool, restrictType Type) Result {
	return Result{
		Success:  true,
		Duration: d,
		Until:    until,
		IsAppend: isAppend,
		Type:     restrictType,
	}
}

func fail() Result {
	return Result{
		Success: false,
	}
}

// BanOrKill exec ban or kill.
func BanOrKill(chat *tb.Chat, user *tb.User, hard bool, banTime time.Duration) Result {
	// if bot is admin and user is not admin, use `Ban`,
	// else use `Kill`.
	if canRestrict(config.GetBot(), chat, user) {
		return Ban(chat, user, hard, banTime)
	}
	return Kill(chat, user, banTime)
}

func canRestrict(bot *tb.Bot, chat *tb.Chat, user *tb.User) bool {
	if !util.CanRestrictMembers(chat, bot.Me) {
		return false
	}

	groupAdmins, err := bot.AdminsOf(chat)
	if err != nil {
		log.Error("can get AdminsOf", zap.Int64("chatID", chat.ID),
			zap.Int64("userID", user.ID), zap.Error(err))
		return false
	}
	for _, admin := range groupAdmins {
		if admin.User.ID == user.ID {
			return false
		}
	}
	return true
}
