package base

import (
	"fmt"
	"html"
	"strings"
	"time"

	"csust-got/entities"
	"csust-got/util"

	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
)

// RunTask can run a task.
func RunTask(ctx Context) error {
	text := "你嗦啥，我听不太懂欸……"

	msg := ctx.Message()
	cmd, rest, err := entities.CommandTakeArgs(msg, 1)
	if err != nil {
		return err
	}
	delay, err := util.EvalDuration(cmd.Arg(0))
	if err != nil || delay < time.Second {
		return ctx.Reply(text)
	}
	// info := cmd.ArgAllInOneFrom(1)
	info := strings.TrimSpace(rest)

	text = fmt.Sprintf("好的, 在 %v 后我会来叫你…… <code>%s</code> , 嗯, 不愧是我。", delay, html.EscapeString(info))
	task := func() {
		uid := ctx.Sender().Username
		// hint := fmt.Sprintf("@%s 我来了, 你要我提醒你…… `%s` ,大概没错吧。", uid, info)
		// err := ctx.Send(hint, ModeMarkdownV2)

		hint := fmt.Sprintf("@%s 我来了, 你要我提醒你…… <code>%s</code> ,大概没错吧。",
			html.EscapeString(uid), html.EscapeString(info))
		err := ctx.Send(hint, ModeHTML)
		if err != nil {
			zap.L().Error("Run Task send msg to user failed",
				zap.String("user", uid),
				zap.String("msg", info),
				zap.Error(err),
			)
		}
	}
	time.AfterFunc(delay, task)
	return ctx.Reply(text, ModeHTML)
}
