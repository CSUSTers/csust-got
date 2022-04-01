// Package entities provides a abstraction of tg bot entities.
package entities

import (
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
	cmdRegex = regexp.MustCompile(`^/([0-9a-zA-Z_]+)(?:@[^\s@]*)?$`)
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
