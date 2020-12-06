package restrict

import (
	"csust-got/context"
	"csust-got/module"
	"strconv"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"golang.org/x/time/rate"
)

var limitMap = make(map[string]*rate.Limiter, 16)

// RateLimit 限制消息发送的频率，以防止刷屏.
func RateLimit(tgbotapi.Update) module.Module {
	limitHandler := func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) module.HandleResult {
		message := update.Message
		// 特殊消息和私聊不做限流处理
		if message == nil || message.Chat.IsPrivate() {
			return module.NextOfChain
		}
		userID := message.From.ID
		chatID := message.Chat.ID
		key := strconv.FormatInt(chatID, 10) + ":" + strconv.Itoa(userID)
		if limiter, ok := limitMap[key]; ok {
			// 普通消息消耗一个令牌
			if message.Sticker == nil && limiter.Allow() {
				return module.NextOfChain
			}
			// Sticker消耗两个令牌
			if message.Sticker != nil && limiter.AllowN(time.Now(), 2) {
				return module.NextOfChain
			}
			// 令牌不足撤回消息
			_, _ = bot.DeleteMessage(tgbotapi.NewDeleteMessage(chatID, message.MessageID))
			return module.NoMore
		}
		limitMap[key] = rate.NewLimiter(1, 20)
		return module.NextOfChain
	}
	return module.Filter(limitHandler)
}
