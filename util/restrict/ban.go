package restrict

import (
	"csust-got/config"
	"csust-got/log"
	"time"

	"go.uber.org/zap"

	tb "gopkg.in/telebot.v3"
)

// Ban is used to ban someone.
func Ban(chat *tb.Chat, user *tb.User, hard bool, duration time.Duration) Result {
	member, err := config.BotConfig.Bot.ChatMemberOf(chat, user)
	if err != nil {
		log.Error("get ChatMemberOf failed", zap.Int64("chatID", chat.ID),
			zap.Int64("userID", user.ID), zap.Error(err))
		return fail()
	}
	member.RestrictedUntil = time.Now().Add(duration).Unix()
	ok := false
	if hard {
		ok = hardBan(chat, member)
	} else {
		ok = softBan(chat, member)
	}
	if ok {
		return success(duration, time.Unix(member.RestrictedUntil, 0), false, RestrictTypeBan)
	}

	return fail()
}

// only allow text or media message.
func softBan(chat *tb.Chat, member *tb.ChatMember) bool {
	member.Rights = tb.NoRights()
	member.CanSendMessages = true
	member.CanSendMedia = true
	return ban(chat, member)
}

// can't send anything.
func hardBan(chat *tb.Chat, member *tb.ChatMember) bool {
	member.Rights = tb.NoRights()
	return ban(chat, member)
}

func ban(chat *tb.Chat, member *tb.ChatMember) bool {
	err := config.BotConfig.Bot.Restrict(chat, member)
	if err != nil {
		log.Warn("Can't restrict chat member.", zap.Error(err))
	}
	return err == nil
}
