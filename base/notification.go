package base

import (
	"csust-got/config"
	"csust-got/prom"
	"csust-got/util"

	. "gopkg.in/tucnak/telebot.v3"
)

// WelcomeNewMember is handle for welcome new member.
// when someone new join group, bot will send welcome message.
func WelcomeNewMember(ctx Context) error {
	for _, member := range ctx.Message().UsersJoined {
		text := config.BotConfig.MessageConfig.WelcomeMessage + util.GetName(&member)
		prom.NewMember(ctx.Chat().Title)
		if err := ctx.Send(text); err != nil {
			return err
		}
	}
	return nil
}

// LeftMember is handle for some member left.
func LeftMember(m *Message) {
	member := m.UserLeft
	if member == nil {
		return
	}
	prom.MemberLeft(m.Chat.Title)
}
