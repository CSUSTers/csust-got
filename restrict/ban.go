package restrict

import (
	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"csust-got/util"
	"fmt"
	"go.uber.org/zap"
	. "gopkg.in/tucnak/telebot.v2"
	"math/rand"
	"strconv"
	"time"
)

/*
If user is restricted for more than 366 days or less than 30 seconds from the current time,
they are considered to be restricted forever.
*/

// BanMyself is a handle for command `ban_myself`, which can ban yourself
func BanMyself(m *Message) {
	sec := time.Duration(rand.Intn(60)+60) * time.Second
	text := "太强了，我居然ban不掉您，您TQL！"
	if BanSomeone(m.Chat, m.Sender, true, sec) {
		text = "我实现了你的愿望！现在好好享用这" + strconv.FormatInt(int64(sec.Seconds()), 10) + "秒~"
	}
	util.SendReply(m.Chat, text, m)
}

// SoftBan is handle for command `ban_soft`.
func SoftBan(m *Message) {
	BanCommand(m, false)
}

// Ban is handle for command `ban`.
func Ban(m *Message) {
	BanCommand(m, true)
}

// BanCommand can execute ban.
func BanCommand(m *Message, hard bool) {
	cmd := entities.FromMessage(m)
	banTime, err := time.ParseDuration(cmd.Arg(0))
	if err != nil {
		banTime = time.Duration(rand.Intn(60)+60) * time.Second
	}
	var banTarget *User = nil
	if !util.CanRestrictMembers(m.Chat, m.Sender) {
		banTarget = m.Sender
	}
	text := "我没办法完成你要我做的事……即便我已经很努力了……结局还是如此。"

	if m.ReplyTo != nil {
		if banTarget == nil {
			banTarget = m.ReplyTo.Sender
		}
		if BanSomeone(m.Chat, banTarget, hard, banTime) {
			if banTarget.ID == m.Sender.ID {
				text = "我可能没有办法帮你完成你要我做的事情……只好……对不起！"
			} else {
				if hard {
					text = fmt.Sprintf("委派下来的工作已经做完了。%s 将会沉默 %d 秒，只不过……你真的希望事情变这样吗？",
						util.GetName(banTarget), int64(banTime.Seconds()))
				} else {
					text = fmt.Sprintf("委派下来的工作已经做完了。%s 将会失落 %d 秒，希望他再次振作起来。",
						util.GetName(banTarget), int64(banTime.Seconds()))
				}
			}
		}
	} else {
		text = "ban 谁呀，咋 ban 呀， 你到底会不会用啊:)"
	}

	util.SendReply(m.Chat, text, m)
}

// BanSomeone Use to ban someone, return true if success.
func BanSomeone(chat *Chat, user *User, hard bool, duration time.Duration) bool {
	member, err := config.BotConfig.Bot.ChatMemberOf(chat, user)
	if err != nil {
		log.Error("get ChatMemberOf failed", zap.Int64("chatID", chat.ID),
			zap.Int("userID", user.ID), zap.Error(err))
		return false
	}
	member.RestrictedUntil = time.Now().Add(duration).Unix()
	if hard {
		return hardBan(chat, member)
	}
	return softBan(chat, member)
}

// only allow text or media message
func softBan(chat *Chat, member *ChatMember) bool {
	member.Rights = NoRights()
	member.CanSendMessages = true
	member.CanSendMedia = true
	return ban(chat, member)
}

// can't send anything
func hardBan(chat *Chat, member *ChatMember) bool {
	member.Rights = NoRights()
	return ban(chat, member)
}

func ban(chat *Chat, member *ChatMember) bool {
	err := config.BotConfig.Bot.Restrict(chat, member)
	if err != nil {
		log.Error("Can't restrict chat member.", zap.Error(err))
	}
	return err == nil
}
