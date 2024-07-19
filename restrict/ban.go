package restrict

import (
	"fmt"
	"math/rand"
	"strconv"
	"time"

	"csust-got/entities"
	"csust-got/util"
	"csust-got/util/restrict"

	. "gopkg.in/telebot.v3"
)

/*
If user is restricted for more than 366 days or less than 30 seconds from the current time,
they are considered to be restricted forever.
*/

// BanMyself is a handle for command `ban_myself`, which can ban yourself.
func BanMyself(m *Message) {
	sec := time.Duration(rand.Intn(80)+40) * time.Second
	text := "太强了，我居然ban不掉您，您TQL!"
	if restrict.Ban(m.Chat, m.Sender, true, sec).Success {
		text = "我实现了你的愿望! 现在好好享用这" + strconv.FormatInt(int64(sec.Seconds()), 10) + "秒~"
	}
	util.SendReply(m.Chat, text, m)
}

// SoftBanCommand is handle for command `ban_soft`.
func SoftBanCommand(m *Message) {
	DoBan(m, false)
}

// BanCommand is handle for command `ban`.
func BanCommand(m *Message) {
	DoBan(m, true)
}

// DoBan can execute ban.
func DoBan(m *Message, hard bool) {
	cmd := entities.FromMessage(m)
	banTime, err := time.ParseDuration(cmd.Arg(0))
	if err != nil || isBanForever(banTime) {
		banTime = time.Duration(rand.Intn(80)+40) * time.Second
	}
	var banTarget *User
	if !util.CanRestrictMembers(m.Chat, m.Sender) {
		banTarget = m.Sender
	}

	text := banAndGetMessage(m, banTarget, hard, banTime)
	util.SendReply(m.Chat, text, m)
}

// tg will ban forever if banTime less than 30 seconds or more than 366 days.
func isBanForever(banTime time.Duration) bool {
	return banTime < 30*time.Second || banTime > 366*24*time.Hour
}

func banAndGetMessage(m *Message, banTarget *User, hard bool, banTime time.Duration) string {
	text := "我没办法完成你要我做的事……即便我已经很努力了……结局还是如此。"
	if m.ReplyTo == nil {
		text = "ban 谁呀，咋 ban 呀， 你到底会不会用啊:)"
	}

	if banTarget == nil {
		banTarget = m.ReplyTo.Sender
	}

	if !restrict.Ban(m.Chat, banTarget, hard, banTime).Success {
		return text
	}

	if banTarget.ID == m.Sender.ID {
		text = "我可能没有办法帮你完成你要我做的事情……只好……对不起!"
	} else {
		if hard {
			text = fmt.Sprintf("委派下来的工作已经做完了。%s 将会沉默 %d 秒，只不过……你真的希望事情变这样吗？",
				util.GetName(banTarget), int64(banTime.Seconds()))
		} else {
			text = fmt.Sprintf("委派下来的工作已经做完了。%s 将会失落 %d 秒，希望他再次振作起来。",
				util.GetName(banTarget), int64(banTime.Seconds()))
		}
	}

	return text
}
