package chat

import (
	"csust-got/config"
	"csust-got/log"
	"csust-got/util/gacha"
	"errors"
	"strings"

	"github.com/redis/go-redis/v9"

	"go.uber.org/zap"
	"gopkg.in/telebot.v3"
)

var gachaConfigs map[int]*config.ChatConfigSingle

// InitGachaConfigs init gachaConfigs
func InitGachaConfigs() {
	gachaConfigs = make(map[int]*config.ChatConfigSingle)

	for _, ccs := range *config.BotConfig.ChatConfigV2 {
		stars, _ := ccs.TriggerForGacha()
		for _, star := range stars {
			gachaConfigs[star] = ccs
		}
	}
}

// GachaReplyHandler reply a gpt msg determined by the gacha result
func GachaReplyHandler(ctx telebot.Context) {
	msg := ctx.Message()

	// only apply to text message
	var text string
	if len(msg.Text) > 0 {
		text = msg.Text
	} else if len(msg.Caption) > 0 {
		text = msg.Caption
	}
	if len(text) == 0 || strings.HasPrefix(text, "/") {
		return
	}

	result, err := gacha.PerformGaCha(ctx.Chat().ID)
	if err != nil {
		// gacha may not be enabled, redis.Nil is expected, ignore it
		if !errors.Is(err, redis.Nil) {
			log.Error("[GaCha]: perform gacha failed", zap.Error(err))
		}
		return
	}
	star := int(result)

	if ccs, ok := gachaConfigs[star]; ok {
		_ = Chat(ctx, ccs, &config.ChatTrigger{Gacha: star})
	}
}
