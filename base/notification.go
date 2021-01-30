package base

import (
	"csust-got/prom"
	"csust-got/util"
	. "gopkg.in/tucnak/telebot.v2"
)

// WelcomeNewMember is handle for welcome new member.
// when someone new join group, bot will send welcome message.
func WelcomeNewMember(m *Message) {
	for _, member := range m.UsersJoined {
		text := "Welcome to this group!" + util.GetName(&member)
		util.SendMessage(m.Chat, text)
		prom.NewMember(m.Chat.Title)
	}
}

// LeftMember is handle for some member left.
func LeftMember(m *Message) {
	member := m.UserLeft
	if member == nil {
		return
	}
	prom.MemberLeft(m.Chat.Title)
}
