package tests

import (
    tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
    "math/rand"
)

var ThisBot = tgbotapi.User{
    ID:           1087946680,
    FirstName:    "小小明",
    LastName:     "",
    UserName:     "smalllight_s_test_bot",
    LanguageCode: "",
    IsBot:        true,
}

func NewUser() *tgbotapi.User {
    userID := rand.Intn(1<<31)
    return &tgbotapi.User{
        ID:           userID,
        FirstName:    "FirstName",
        LastName:     "LastName",
        UserName:     "UserName",
        LanguageCode: "zh-hans",
        IsBot:        false,
    }
}
