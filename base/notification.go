package base

import (
    tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
    "log"
)

func WelcomeNewMember(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
    message := update.Message
    memberSlice := message.NewChatMembers
    for _, member := range *memberSlice {
        text := "Welcome to this group!" + getName(member)
        go sendNotificationTo(bot, message.Chat.ID, text)
    }
}

func sendNotificationTo(bot *tgbotapi.BotAPI, chatID int64, text string) {
    message := tgbotapi.NewMessage(chatID, text)
    _, err := bot.Send(message)
    if err != nil {
        log.Println("ERROR: Can't send message")
        log.Println(err.Error())
    }
}

func getName(user tgbotapi.User) string {
    name := user.FirstName
    if user.LastName != "" {
        name += " " + user.LastName
    }
    return name
}