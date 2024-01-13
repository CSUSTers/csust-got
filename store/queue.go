package store

import (
	. "gopkg.in/telebot.v3"
	"time"
)

var (
	ByeWorldQueue TaskQueue[*Message]
)

func InitQueues(bot *Bot) {
	ByeWorldQueue = NewDeleteMsgQueue("bye_world", bot)
}

type TaskQueue[T any] interface {
	// Push adds a task to the timer.
	Push(task T, runAt time.Time) error

	// Cancel removes a task from the timer.
	Cancel(task T) error

	// Fetch returns a task from the timer.
	fetch() ([]T, error)

	// Process processes a task.
	process(task T) error

	// init initializes the queue, called once before running.
	init() error
}
