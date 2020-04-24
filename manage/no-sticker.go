package manage

import (
	"csust-got/context"
	"csust-got/module"
	"csust-got/util"
	"go.uber.org/zap"
	"log"

	"github.com/go-redis/redis/v7"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
)

var key = "enabled"

// NoSticker is a switch for NoStickerMode
func NoSticker(tgbotapi.Update) module.Module {
	handler := func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) {
		v, text := 0, "NoStickerMode is off."
		if !isNoStickerMode(ctx) {
			v, text = 1, "Do NOT send Sticker!"
		}

		_, err := ctx.GlobalClient().Set(ctx.WrapKey(key), v, 0).Result()
		if err != nil {
			log.Println("ERROR: Can't set NoStickerMode")
			log.Println(err.Error())
			return
		}
		util.SendMessage(bot, tgbotapi.NewMessage(update.Message.Chat.ID, text))
	}
	return module.Stateful(handler)
}

// DeleteSticker is a handle, if a message has Sticker, the message will arrive this function.
// If this chat in NoStickerMode, Sticker may be deleted.
func DeleteSticker(tgbotapi.Update) module.Module {
	handler := func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) module.HandleResult {
		message := update.Message

		if !isNoStickerMode(ctx) {
			return module.NextOfChain
		}

		deleteMessage := tgbotapi.DeleteMessageConfig{
			ChatID:    message.Chat.ID,
			MessageID: message.MessageID,
		}

		resp, err := bot.DeleteMessage(deleteMessage)
		if err != nil {
			log.Println("ERROR: Can't delete sticker")
			log.Println(err.Error())
			return module.NoMore
		}
		if !resp.Ok {
			log.Println("NoSticker Response NOT OK")
		}
		return module.NoMore
	}
	return module.Filter(handler)
}

// check if this chat in NoStickerMode
func isNoStickerMode(ctx context.Context) bool {
	isNoStickerMode, err := ctx.GlobalClient().Get(ctx.WrapKey(key)).Int()
	if err != nil && err != redis.Nil {
		zap.L().Error("ERROR: Can't get no-sticker mode from context", zap.Error(err))
		return false
	}

	// No Sticker Mode is off
	if isNoStickerMode == 0 || err == redis.Nil {
		return false
	}

	return true
}
