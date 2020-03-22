package manage

import (
	"csust-got/module"
	"github.com/go-redis/redis/v7"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"log"
)

var key = "enabled"

// NoSticker is a switch for NoStickerMode
func NoSticker(update tgbotapi.Update) module.Module {
	handler := func(ctx module.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) {
		v := 0
		if isNoStickerMode(ctx) {
			v = 1
		}

		_, err := ctx.GlobalClient().Set(ctx.WrapKey(key), v, 0).Result()
		if err != nil {
			log.Println("ERROR: Can't set NoStickerMode")
			log.Println(err.Error())
		}
	}
	return module.Stateful(handler)
}

// If a message has Sticker, the message will arrive this function.
// If this chat in NoStickerMode, Sticker may be deleted.
func DeleteSticker(update tgbotapi.Update) module.Module {
	handler := func(ctx module.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) {
		message := update.Message

		if isNoStickerMode(ctx) {
			return
		}

		deleteMessage := tgbotapi.DeleteMessageConfig{
			ChatID:    message.Chat.ID,
			MessageID: message.MessageID,
		}

		resp, err := bot.DeleteMessage(deleteMessage)
		if err != nil {
			log.Println("ERROR: Can't delete sticker")
			log.Println(err.Error())
		}
		if !resp.Ok {
			log.Println("NoSticker Response NOT OK")
		}
	}
	return module.Stateful(handler)
}

// check if this chat in NoStickerMode
func isNoStickerMode(ctx module.Context) bool {
	isNoStickerMode, err := ctx.GlobalClient().Get(ctx.WrapKey(key)).Int()
	if err != nil && err != redis.Nil {
		log.Println("ERROR: Can't get no-sticker mode from context")
		log.Println(err.Error())
		return false
	}

	// No Sticker Mode is off
	if isNoStickerMode == 0 || err == redis.Nil {
		return false
	}

	return true
}
