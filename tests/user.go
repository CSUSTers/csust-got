package tests

import (
    tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
    "math/rand"
)

func NewUser() *tgbotapi.User {
    userID := rand.Intn(1<<63)
    return &tgbotapi.User{
        ID:           userID,
        FirstName:    "FirstName",
        LastName:     "LastName",
        UserName:     "UserName",
        LanguageCode: "zh-hans",
        IsBot:        false,
    }
}
