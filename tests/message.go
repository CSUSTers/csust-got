package tests

import (
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"math/rand"
)

func NewMessageFromGroup() *tgbotapi.Message {
	userA := NewUser()
	chatA := NewGroupChat()
	date := rand.Intn(1 << 31)
	id := rand.Intn(1 << 31)
	return &tgbotapi.Message{
		MessageID: id,
		From:      userA,
		Date:      date,
		Chat:      chatA,
		Text:      "Hello",
	}
}

func NewCommand(command string) *tgbotapi.Message {
	userA := NewUser()
	chatA := NewGroupChat()
	date := rand.Intn(1 << 31)
	id := rand.Intn(1 << 31)
	return &tgbotapi.Message{
		MessageID: id,
		From:      userA,
		Date:      date,
		Chat:      chatA,
		Text:      command,
		Entities: &[]tgbotapi.MessageEntity{
			{
				Offset: 0,
				Length: len(command),
				Type:   "bot_command",
			},
		},
	}
}
