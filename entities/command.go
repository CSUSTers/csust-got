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
	spaces   = regexp.MustCompile(`\s+`)
	cmdRegex = regexp.MustCompile(`^/([0-9a-zA-Z_]+)(?:@[^\s@]+)?$`)

	errParseCommand     = errors.New("parse command failed")
	errParseCommandName = errors.New("parse command name failed")
)

// FromMessage - get command in a message.
func FromMessage(msg *Message) *BotCommand {
	args := splitText(strings.TrimSpace(msg.Text), -1)
	if len(args) == 0 {
		return nil
	}
	name := args[0]
	m := cmdRegex.FindStringSubmatch(name)
	if len(m) == 0 {
		return nil
	}
	name = m[1]
	return &BotCommand{name, args[1:]}
}

// CommandTakeArgs returns a command and rest part of text.
// `argc` is the number of args to take, if `argc` < 0, all args will be taken.
func CommandTakeArgs(msg *Message, argc int) (cmd *BotCommand, rest string, err error) {
	taken := argc
	if argc >= 0 {
		// cmd .. args .. rest
		//  1  +  argc  +  1
		taken = argc + 2
	}
	args := spaces.Split(strings.TrimSpace(msg.Text), taken)
	if len(args) == 0 {
		err = errParseCommand
		return
	}

	name, args := args[0], args[1:]
	m := cmdRegex.FindStringSubmatch(name)
	if len(m) == 0 {
		return nil, "", errParseCommandName
	}
	name = m[1]
	cmd = &BotCommand{name, args}

	if argc >= 0 && len(args) > argc {
		cmd.args = args[:argc]
		rest = args[argc]
	}
	return cmd, rest, nil
}

func splitText(txt string, n int) []string {
	if len(txt) > 0 {
		return spaces.Split(txt, n)
	}
	return nil
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
