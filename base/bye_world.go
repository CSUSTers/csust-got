package base

import (
	"csust-got/entities"
	"csust-got/orm"
	"csust-got/util"
	. "gopkg.in/telebot.v3"
	"time"
)

// ByeWorld auto delete message.
func ByeWorld(m *Message) {
	command := entities.FromMessage(m)

	deleteFrom := 5 * time.Minute
	if command.Argc() > 0 {
		arg := command.Arg(0)
		d, err := time.ParseDuration(arg)
		if err != nil {
			util.SendReply(m.Chat, "invalid duration", m)
			return
		}
		if d < time.Minute || d > 5*time.Minute {
			util.SendReply(m.Chat, "duration should be between 1m and 5m", m)
			return
		}
		deleteFrom = d
		return
	}

	err := orm.SetByeWorldDuration(m.Chat.ID, m.Sender.ID, deleteFrom)
	if err != nil {
		util.SendReply(m.Chat, "set duration failed", m)
		return
	}

	util.SendReply(m.Chat, "bye~", m)

}

// HelloWorld disable auto delete message.
func HelloWorld(m *Message) {
	err := orm.DeleteByeWorldDuration(m.Chat.ID, m.Sender.ID)
	if err != nil {
		util.SendReply(m.Chat, "disable failed", m)
		return
	}

	util.SendReply(m.Chat, "hello~", m)

}
