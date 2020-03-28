package preds

import (
	"csust-got/config"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"strings"
)

// Predicate is the common interface of a 'condition' that indices whether an update should be handled.
type Predicate struct {
	predicate
}

func (p Predicate) And(other Predicate) Predicate {
	return Predicate{andPredicate{p, other}}
}

func (p Predicate) Or(other Predicate) Predicate {
	return Predicate{orPredicate{p, other}}
}

// SideEffect will be triggered if the predicate is true.
func (p Predicate) SideEffectOnTrue(sideEffect func(update tgbotapi.Update)) Predicate {
	next := func(update tgbotapi.Update) bool {
		sideEffect(update)
		return true
	}
	return p.And(BoolFunction(next))
}

type predicate interface {
	ShouldHandle(update tgbotapi.Update) bool
}

type andPredicate struct {
	lhs predicate
	rhs predicate
}

func (p andPredicate) ShouldHandle(update tgbotapi.Update) bool {
	return p.lhs.ShouldHandle(update) && p.rhs.ShouldHandle(update)
}

type orPredicate struct {
	lhs predicate
	rhs predicate
}

func (o orPredicate) ShouldHandle(update tgbotapi.Update) bool {
	return o.lhs.ShouldHandle(update) || o.rhs.ShouldHandle(update)
}

type functionalPredicate struct {
	pred func(update tgbotapi.Update) bool
}

func (f functionalPredicate) ShouldHandle(update tgbotapi.Update) bool {
	return f.pred(update)
}

// BoolFunction wraps a `func(Update) bool` to a Predicate.
func BoolFunction(pred func(update tgbotapi.Update) bool) Predicate {
	return Predicate{functionalPredicate{pred: pred}}
}

// NonEmpty is the condition of a module which only processes non-empty message.
var NonEmpty = BoolFunction(nonEmpty)

// IsAnyCommand is the condition of a module which only process Command message.
var IsAnyCommand = BoolFunction(command)

// HasSticker is the condition of a module which process Sticker message.
var HasSticker = BoolFunction(sticker)

// IsCommand handles the update when the command is exactly the argument.
func IsCommand(command string) Predicate {
	isThat := func(update tgbotapi.Update) bool {
		cmdAndName := strings.Split(update.Message.CommandWithAt(), "@")
		if len(cmdAndName) == 1 {
			return cmdAndName[0] == command
		} else if len(cmdAndName) == 2 {
			bot := config.BotConfig.Bot.UserName
			return cmdAndName[0] == command && cmdAndName[1] == bot
		}
		return false
	}
	return NonEmpty.And(IsAnyCommand).And(BoolFunction(isThat))
}

func nonEmpty(update tgbotapi.Update) bool {
	return update.Message != nil
}

func command(update tgbotapi.Update) bool {
	return nonEmpty(update) && update.Message.IsCommand()
}

func sticker(update tgbotapi.Update) bool {
	return nonEmpty(update) && update.Message.Sticker != nil
}
