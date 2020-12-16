package tests

import (
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"math/rand"
)

func NewUpdateMessageFromGroup() *tgbotapi.Update {
	id := rand.Intn(1 << 62)
	message := NewMessageFromGroup()
	return &tgbotapi.Update{
		UpdateID: id,
		Message:  message,
	}
}

func NewUpdateCommand(command string) *tgbotapi.Update {
	id := rand.Intn(1 << 62)
	message := NewCommand(command)
	return &tgbotapi.Update{
		UpdateID: id,
		Message:  message,
	}
}
