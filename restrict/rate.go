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
		return checkLimit(bot, ctx, message)
	}
	return module.Filter(limitHandler)
}

func checkLimit(bot *tgbotapi.BotAPI, ctx context.Context, msg *tgbotapi.Message) module.HandleResult {
	rateConfig := ctx.GlobalConfig().RateLimitConfig
	userID, chatID := msg.From.ID, msg.Chat.ID
	key := strconv.FormatInt(chatID, 10) + ":" + strconv.Itoa(userID)
	if limiter, ok := limitMap[key]; ok {
		if checkRate(ctx, msg, limiter) {
			return module.NextOfChain
		}
		// 令牌不足撤回消息
		_, _ = bot.DeleteMessage(tgbotapi.NewDeleteMessage(chatID, msg.MessageID))
		return module.NoMore
	}
	limitMap[key] = rate.NewLimiter(rate.Limit(rateConfig.Limit), rateConfig.MaxToken)
	return module.NextOfChain
}

// return false if message should be limit
func checkRate(ctx context.Context, msg *tgbotapi.Message, limiter *rate.Limiter) bool {
	rateConfig := ctx.GlobalConfig().RateLimitConfig
	if msg.Sticker != nil {
		return limiter.AllowN(time.Now(), rateConfig.StickerCost)
	}
	if msg.Command() != "" {
		return limiter.AllowN(time.Now(), rateConfig.CommandCost)
	}
	return limiter.AllowN(time.Now(), rateConfig.Cost)
}
