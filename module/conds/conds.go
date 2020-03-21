package conds

import tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"

// NonEmpty is the condition of a module which only processes non-empty message.
func NonEmpty(update tgbotapi.Update) bool {
	return update.Message != nil
}
