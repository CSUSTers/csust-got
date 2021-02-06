package base

import (
	"csust-got/entities"
	"csust-got/util"
	"fmt"
	"time"

	. "gopkg.in/tucnak/telebot.v2"
)

// RunTask can run a task
func RunTask(m *Message) {
	text := "你嗦啥，我听不太懂欸……"

	cmd := entities.FromMessage(m)
	delay, err := util.EvalDuration(cmd.Arg(0))
	info := cmd.ArgAllInOneFrom(1)
	if err != nil || delay < time.Second {
		util.SendReply(m.Chat, text, m)
		return
	}

	text = fmt.Sprintf("好的，在 %v 后我会来叫你……“%s”，嗯，不愧是我。", delay, info)
	task := func() {
		uid := m.Sender.Username
		hint := fmt.Sprintf("@%s 我来了，你要我提醒你……“%s”，大概没错吧。", uid, info)
		util.SendMessage(m.Chat, hint)
	}
	time.AfterFunc(delay, task)
	util.SendReply(m.Chat, text, m)
}
