package restrict

import (
	"strconv"
	"time"

	"csust-got/config"
	"csust-got/entities"
	"csust-got/util"
	"golang.org/x/time/rate"
	. "gopkg.in/tucnak/telebot.v3"
)

var limitMap = make(map[string]*rate.Limiter, 16)

// CheckLimit 限制消息发送的频率，以防止刷屏.
func CheckLimit(m *Message) bool {
	rateConfig := config.BotConfig.RateLimitConfig
	key := strconv.FormatInt(m.Chat.ID, 10) + ":" + strconv.FormatInt(m.Sender.ID, 10)
	if limiter, ok := limitMap[key]; ok {
		if checkRate(m, limiter) {
			return true
		}
		// 令牌不足撤回消息
		util.DeleteMessage(m)
		return false
	}
	limitMap[key] = rate.NewLimiter(rate.Limit(rateConfig.Limit), rateConfig.MaxToken)
	return true
}

// return false if message should be limit.
func checkRate(m *Message, limiter *rate.Limiter) bool {
	rateConfig := config.BotConfig.RateLimitConfig
	if m.Sticker != nil {
		return limiter.AllowN(time.Now(), rateConfig.StickerCost)
	}
	cmd := entities.FromMessage(m)
	if cmd != nil {
		return limiter.AllowN(time.Now(), rateConfig.CommandCost)
	}
	return limiter.AllowN(time.Now(), rateConfig.Cost)
}
