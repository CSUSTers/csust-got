package restrict

import (
	"csust-got/context"
	"csust-got/module"
	"strconv"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"golang.org/x/time/rate"
)

var limitMap = make(map[string]*rate.Limiter, 16)

// RateLimit is used to limit rate of user's message.
func RateLimit(tgbotapi.Update) module.Module {
	limitHandler := func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) module.HandleResult {
		message := update.Message
		if message == nil || message.Chat.IsPrivate() {
			return module.NextOfChain
		}
		userID := message.From.ID
		chatID := message.Chat.ID
		key := strconv.FormatInt(chatID, 10) + ":" + strconv.Itoa(userID)
		if limiter, ok := limitMap[key]; ok {
			if limiter.Allow() {
				return module.NextOfChain
			}
			_, _ = bot.DeleteMessage(tgbotapi.NewDeleteMessage(chatID, message.MessageID))
			return module.NoMore
		}
		limitMap[key] = rate.NewLimiter(1, 15)
		return module.NextOfChain
	}
	return module.Filter(limitHandler)
}
