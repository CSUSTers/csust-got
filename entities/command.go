// Package entities provides a abstraction of tg bot entities.
package entities

import (
	"errors"
	"regexp"
	"strings"

	. "gopkg.in/telebot.v3"
)

// BotCommand - command in message.
type BotCommand struct {
	name string
	args []string
}

var (
	spaces, _   = regexp.Compile(`\s+`)
	cmdRegex, _ = regexp.Compile(`^/[0-9a-zA-Z_]+$`)

	errParseCommand = errors.New("parse command failed")
)

// FromMessage - get command in a message.
func FromMessage(msg *Message) *BotCommand {
	args := splitText(strings.TrimSpace(msg.Text))
	if len(args) == 0 {
		return nil
	}
	name := args[0]
	if idx := strings.IndexRune(name, '@'); idx != -1 {
		name = name[:idx]
	}
	if !cmdRegex.MatchString(name) {
		return nil
	}
	return &BotCommand{name[1:], args[1:]}
}

func CommandTakeArgs(msg *Message, argc int) (cmd *BotCommand, rest string, err error) {
	if argc >= 0 {
		argc = argc + 1
	}
	args := spaces.Split(strings.TrimSpace(msg.Text), argc)
	if len(args) == 0 {
		err = errParseCommand
		return
	} else {
		name := args[0]
		if idx := strings.IndexRune(name, '@'); idx != -1 {
			name = name[:idx]
		}
		if argc > 0 {
			if len(args) < argc {
				cmd = &BotCommand{name, args[1:]}
			} else {
				cmd = &BotCommand{name, args[1:argc]}
				rest = args[argc]
			}
		} else {
			cmd = &BotCommand{name, args[1:]}
		}
	}
	return
}

func splitText(txt string) []string {
	ts := []string{}
	if len(txt) > 0 {
		ts = spaces.Split(txt, -1)
	}
	return ts
}

// Name - command name.
func (c BotCommand) Name() string {
	return c.name
}

// Argc - length of args.
func (c BotCommand) Argc() int {
	return len(c.args)
}

// Arg - get arg at index `idx`.
func (c BotCommand) Arg(idx int) string {
	if idx >= c.Argc() {
		return ""
	}
	return c.args[idx]
}

// MultiArgsFrom - get args from index `idx`.
func (c BotCommand) MultiArgsFrom(idx int) []string {
	if idx >= c.Argc() {
		return []string{}
	}
	return c.args[idx:]
}

// ArgAllInOneFrom - get all args as one string.
func (c BotCommand) ArgAllInOneFrom(idx int) string {
	arg := strings.Builder{}
	for _, s := range c.MultiArgsFrom(idx) {
		_, _ = arg.WriteString(s)
		_, _ = arg.WriteRune(' ')
	}
	return arg.String()
}
