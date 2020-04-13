// Package command provides a abstraction of tg bot command.
package command

import (
	"errors"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
)

// Command is command
type Command struct {
	name string
	args []string
}

// FromMessage get command from message
func FromMessage(msg *tgbotapi.Message) (*Command, error) {
	name := msg.Command()
	if name == "" {
		return nil, errors.New("FromMessage: update isn't a Command")
	}
	args := strings.Split(msg.CommandArguments(), " ")
	return &Command{name, args}, nil
}

// Name get command's name.
func (c Command) Name() string {
	return c.name
}

// Argc get args of command.
func (c Command) Argc() int {
	return len(c.args)
}

// Arg get arg in index.
func (c Command) Arg(idx int) string {
	if idx >= c.Argc() {
		return ""
	}
	return c.args[idx]
}

// MultiArgsFrom get args from index.
func (c Command) MultiArgsFrom(idx int) []string {
	if idx >= c.Argc() {
		return []string{}
	}
	return c.args[idx:]
}
