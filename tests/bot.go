package tests

import tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"

var BotForTest = &tgbotapi.BotAPI{
    Token:  "Token",
    Debug:  false,
    Buffer: 100,
    Self:   ThisBot,
    Client: nil,
}