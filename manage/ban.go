package manage

import (
    tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
    "log"
    "math/rand"
    "time"
)


// BanMyself is a handle for command `ban_myself`, which can ban yourself
func BanMyself(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
    sec := time.Duration(rand.Intn(30)+90)*time.Second
    banSomeone(bot, update.Message.Chat.ID, update.Message.From.ID, sec)
}


// Ban is handle for command `ban`.
func Ban(update tgbotapi.Update, bot *tgbotapi.BotAPI) {

}


// Use to ban someone
func banSomeone(bot *tgbotapi.BotAPI, chatID int64, userID int, duration time.Duration) {

    chatMember := tgbotapi.ChatMemberConfig{
        ChatID:             chatID,
        UserID:             userID,
    }

    canSendMessages := false

    restrictConfig := tgbotapi.RestrictChatMemberConfig {
        ChatMemberConfig:      chatMember,
        UntilDate:             time.Now().Add(duration).UnixNano(),
        CanSendMessages:       &canSendMessages,
        CanSendMediaMessages:  nil,
        CanSendOtherMessages:  nil,
        CanAddWebPagePreviews: nil,
    }

    resp, err := bot.RestrictChatMember(restrictConfig)
    if err != nil {
        log.Println("ERROR: Can't restrict chat member.")
        log.Println(err.Error())
    }
    if !resp.Ok {
        log.Println("ERROR: Can't restrict chat member.")
        log.Println(resp.Description)
    }
}