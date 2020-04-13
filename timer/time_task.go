package timer

import (
	"csust-got/command"
	"csust-got/context"
	"csust-got/module"
	"csust-got/module/preds"
	"csust-got/util"
	"fmt"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
)

// RunTask can run a task
func RunTask() module.Module {
	handle := func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) {
		message := update.Message
		cmd, _ := command.FromMessage(message)

		newMessage := tgbotapi.NewMessage(message.Chat.ID, "你说啥，我听不太懂欸……")

		delay, err := util.EvalDuration(cmd.Arg(0))
		text := cmd.Arg(1)
		if err != nil || delay < 1 {
			util.SendMessage(bot, newMessage)
			return
		}

		newMessage.Text = fmt.Sprintf("好的，在 %v 后我会来叫你……“%s”，嗯。", delay, text)
		newMessage.ReplyToMessageID = message.MessageID
		task := func() {
			msg := tgbotapi.NewMessage(message.Chat.ID, "")
			uid := message.From.UserName
			msg.Text = fmt.Sprintf("@%s 我来了，你要我提醒你……“%s”，大概没错吧。", uid, text)
			util.SendMessage(bot, msg)
		}
		ctx.DoAfterNamed(task, delay, text)
		util.SendMessage(bot, newMessage)
	}
	m := module.Stateful(handle)
	return module.WithPredicate(m, preds.IsCommand("run_after"))
}
