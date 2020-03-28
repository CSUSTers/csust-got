// package command provides a abstraction of tg bot command.
package command

import (
	"errors"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"strings"
)

type Command struct {
	name string
	args []string
}

func FromMessage(msg *tgbotapi.Message) (*Command, error) {
	name := msg.Command()
	if name == "" {
		return nil, errors.New("FromMessage: update isn't a Command")
	}
	args := strings.Split(msg.CommandArguments(), " ")
	return &Command{name, args}, nil
}

func (c Command) Name() string {
	return c.name
}

func (c Command) Argc() int {
	return len(c.args)
}

func (c Command) Arg(idx int) string {
	if idx >= c.Argc() {
		return ""
	}
	return c.args[idx]
}

func (c Command) MultiArgsFrom(idx int) []string {
	if idx >= c.Argc() {
		return []string{}
	}
	return c.args[idx:]
}
