// Package entities provides a abstraction of tg bot entities.
package entities

import (
	. "gopkg.in/tucnak/telebot.v2"
	"regexp"
	"strings"
)

// BotCommand - command in message
type BotCommand struct {
	name string
	args []string
}

var (
	spaces, _ = regexp.Compile(`\s+`)
)

// FromMessage - get command in a message
func FromMessage(msg *Message) *BotCommand {
	args := splitText(strings.TrimSpace(msg.Text))
	if len(args) == 0 {
		return &BotCommand{"", []string{}}
	}
	name := args[0]
	if idx := strings.IndexRune(name, '@'); idx != -1 {
		name = name[1:idx]
	}
	return &BotCommand{name, args[1:]}
}

func splitText(txt string) []string {
	ts := []string{}
	if len(txt) > 0 {
		ts = spaces.Split(txt, -1)
	}
	return ts
}

// Name - command name
func (c BotCommand) Name() string {
	return c.name
}

// Argc - length of args
func (c BotCommand) Argc() int {
	return len(c.args)
}

// Arg - get arg at index `idx`
func (c BotCommand) Arg(idx int) string {
	if idx >= c.Argc() {
		return ""
	}
	return c.args[idx]
}

// MultiArgsFrom - get args from index `idx`
func (c BotCommand) MultiArgsFrom(idx int) []string {
	if idx >= c.Argc() {
		return []string{}
	}
	return c.args[idx:]
}

// ArgAllInOneFrom - get all args as one string
func (c BotCommand) ArgAllInOneFrom(idx int) string {
	arg := strings.Builder{}
	for _, s := range c.MultiArgsFrom(idx) {
		arg.WriteString(s)
		arg.WriteRune(' ')
	}
	return arg.String()
}
