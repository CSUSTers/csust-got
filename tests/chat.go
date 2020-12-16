package tests

import (
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"math/rand"
)

func NewGroupChat() *tgbotapi.Chat {
	chatID := -rand.Int63n(1 << 62)
	return &tgbotapi.Chat{
		ID:                  chatID,
		Type:                "supergroup",
		Title:               "Test",
		AllMembersAreAdmins: false,
	}
}

func NewPrivateChat() *tgbotapi.Chat {
	chatID := rand.Int63n(1 << 62)
	return &tgbotapi.Chat{
		ID:        chatID,
		Type:      "private",
		UserName:  "username",
		FirstName: "FirstName",
		LastName:  "LastName",
	}
}
