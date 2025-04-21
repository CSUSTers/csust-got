package base

import (
	"csust-got/config"
	"csust-got/util"

	. "gopkg.in/telebot.v3"
)

// WelcomeNewMember is handle for welcome new member.
// when someone new join group, bot will send welcome message.
func WelcomeNewMember(ctx Context) error {
	usersJoined := ctx.Message().UsersJoined
	for idx := range usersJoined {
		member := &usersJoined[idx]
		text := config.BotConfig.MessageConfig.WelcomeMessage + util.GetName(member)
		if err := ctx.Send(text); err != nil {
			return err
		}
	}
	return nil
}

// LeftMember is handle for some member left.
func LeftMember(m *Message) {
	if m.UserLeft == nil {
		return
	}
}
