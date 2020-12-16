// Package entities provides a abstraction of tg bot entities.
package entities

import (
	"errors"
	"regexp"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
)

// Command - command in message
type Command struct {
	name string
	args []string
}

var (
	spaces, _ = regexp.Compile(`\s+`)
)

// FromMessage - get command in a message
func FromMessage(msg *tgbotapi.Message) (*Command, error) {
	name := msg.Command()
	if name == "" {
		return nil, errors.New("FromMessage: update isn't a Command")
	}
	args := spaces.Split(strings.TrimSpace(msg.CommandArguments()), -1)
	return &Command{name, args}, nil
}

// Name - command name
func (c Command) Name() string {
	return c.name
}

// Argc - length of args
func (c Command) Argc() int {
	return len(c.args)
}

// Arg - get arg at index `idx`
func (c Command) Arg(idx int) string {
	if idx >= c.Argc() {
		return ""
	}
	return c.args[idx]
}

// MultiArgsFrom - get args from index `idx`
func (c Command) MultiArgsFrom(idx int) []string {
	if idx >= c.Argc() {
		return []string{}
	}
	return c.args[idx:]
}

// ArgAllInOneFrom - get all args as one string
func (c Command) ArgAllInOneFrom(idx int) string {
	arg := strings.Builder{}
	for _, s := range c.MultiArgsFrom(idx) {
		arg.WriteString(s)
		arg.WriteRune(' ')
	}
	return arg.String()
}
