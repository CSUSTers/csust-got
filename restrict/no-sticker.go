package restrict

import (
	"csust-got/log"
	"csust-got/orm"
	"csust-got/util"

	. "gopkg.in/tucnak/telebot.v3"

	"go.uber.org/zap"
)

// NoSticker is a switch for NoStickerMode
func NoSticker(m *Message) {
	orm.ToggleNoStickerMode(m.Chat.ID)
	text := "NoStickerMode is off."
	if orm.IsNoStickerMode(m.Chat.ID) {
		text = "Do NOT send Sticker!"
	}
	util.SendMessage(m.Chat, text)
}

// NoStickerModeHandler is a handle, if a message has Sticker, the message will arrive this function.
// If this chat in NoStickerMode, Sticker may be deleted.
func NoStickerModeHandler(m *Message) {
	if !orm.IsNoStickerMode(m.Chat.ID) {
		return
	}
	log.Info("Chat is in no sticker mode, delete message", zap.Int64("chatID", m.Chat.ID))
	util.DeleteMessage(m)
}
