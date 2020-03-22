package util

import (
    tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
    "log"
)

func SendMessage(bot *tgbotapi.BotAPI, message tgbotapi.Chattable) {
    _, err := bot.Send(message)
    if err != nil {
        log.Println("ERROR: Can't send message")
        log.Println(err.Error())
    }
}