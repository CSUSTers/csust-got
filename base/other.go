package base

import (
	"csust-got/manage"
	"csust-got/util"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"math/rand"
	"strconv"
	"time"
)

// FakeBanMyself is handle for command `fake_ban_myself`.
// Use it to just get a reply like command `ban_myself`.
// It looks like you've been banned, but in fact you have a 2% chance that it will actually be banned。
func FakeBanMyself(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	sec := time.Duration(rand.Intn(30)+90) * time.Second
	chatID := update.Message.Chat.ID
	text := "我实现了你的愿望！现在好好享用这" + strconv.FormatInt(int64(sec.Seconds()), 10) + "秒~"
	message := tgbotapi.NewMessage(chatID, text)
	message.ReplyToMessageID = update.Message.MessageID
	util.SendMessage(bot, message)
	if rand.Intn(100) < 2 {
		manage.BanSomeone(bot, chatID, update.Message.From.ID, true, sec)
	}
}
