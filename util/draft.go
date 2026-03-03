package util

import (
	"csust-got/config"
	"csust-got/log"
	"strconv"

	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

// SendMessageDraft sends a draft message using the Telegram sendMessageDraft API.
// This method streams a partial message to a user while the message is being generated.
// It only works in private chats.
// The draftID must be non-zero; changes of drafts with the same identifier are animated.
func SendMessageDraft(chatID int64, draftID int, text string, parseMode tb.ParseMode) error {
	params := map[string]string{
		"chat_id":  strconv.FormatInt(chatID, 10),
		"draft_id": strconv.Itoa(draftID),
		"text":     text,
	}
	if parseMode != tb.ModeDefault {
		params["parse_mode"] = parseMode
	}

	_, err := config.GetBot().Raw("sendMessageDraft", params)
	if err != nil {
		log.Error("Failed to send message draft", zap.Error(err))
	}
	return err
}
