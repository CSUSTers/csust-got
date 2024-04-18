package base

import (
	"fmt"
	"html"
	"strings"
	"time"

	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"csust-got/orm"
	"csust-got/store"
	"csust-got/util"

	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
)

var (
	timerTaskRunner *store.TimeTask
)

func initTimeTaskRunner() {
	timerTaskRunner = store.NewTimeTask(runTimerTask)
	go timerTaskRunner.Run()

	now := time.Now()
	tasks, err := orm.QueryTasks(0, now.Add(store.FetchTaskTime).UnixMilli())
	if err != nil {
		log.Error("init query tasks error", zap.Error(err))
		return
	}
	log.Debug("bot load tasks from redis", zap.Int("count", len(tasks)))
	_, err = orm.DelTasksInTimeRange(0, now.Add(store.FetchTaskTime).UnixMilli())
	if err != nil {
		log.Error("init del tasks error", zap.Error(err))
		return
	}

	ddl := now.Add(-store.TaskDeadTime).UnixMilli()
	for _, t := range tasks {
		if t.ExecTime < ddl {
			log.Info("task exec time expired, skip it", zap.String("task", t.Raw))
			continue
		}
		timerTaskRunner.AddTask(&t.Task)
	}
}

func runTimerTask(task *store.Task) {
	log.Debug("running task", zap.Any("task", task))
	bot := config.BotConfig.Bot
	chat, err := bot.ChatByID(task.ChatId)
	if err != nil {
		log.Error("run timer task, get chat by id error", zap.Int64("chat_id", task.ChatId), zap.Error(err))
		return
	}

	hint := fmt.Sprintf("我来了, 你要我提醒你…… <code>%s</code> ,大概没错吧。", html.EscapeString(task.Info))
	if chat.Type != ChatPrivate {
		var user *Chat
		user, err = bot.ChatByID(task.UserId)
		if err != nil {
			log.Error("run timer task, get user by id error", zap.Int64("user_id", task.UserId), zap.Error(err))
			return
		}
		hint = fmt.Sprintf("@%s, %s", user.Username, hint)
	}

	_, err = bot.Send(chat, hint, ModeHTML)
	if err != nil {
		log.Error("Run Task send msg failed", zap.Any("task", task), zap.Error(err))
	}
}

// RunTask can run a task.
func RunTask(ctx Context) error {
	now := time.Now()
	text := "你嗦啥，我听不太懂欸……"

	msg := ctx.Message()
	cmd, rest, err := entities.CommandTakeArgs(msg, 1)
	if err != nil {
		return ctx.Reply(text)
	}
	delay, err := util.EvalDuration(cmd.Arg(0))
	if err != nil || delay < time.Second {
		return ctx.Reply(text)
	}
	// info := cmd.ArgAllInOneFrom(1)
	info := strings.TrimSpace(rest)

	timerTaskRunner.AddTask(&store.Task{
		User:     ctx.Sender().Username,
		UserId:   ctx.Sender().ID,
		ChatId:   ctx.Chat().ID,
		Info:     info,
		ExecTime: now.Add(delay).UnixMilli(),
		SetTime:  now.UnixMilli(),
	})

	text = fmt.Sprintf("好的, 在 %v 后我会来叫你…… <code>%s</code> , 嗯, 不愧是我。", delay, html.EscapeString(info))
	return ctx.Reply(text, ModeHTML)
}
