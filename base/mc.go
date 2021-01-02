package base

import (
	"csust-got/context"
	"csust-got/log"
	"csust-got/module"
	"csust-got/util"
	"fmt"
	"go.uber.org/zap"
	"strconv"

	"github.com/go-redis/redis/v7"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
)

// message type
const (
	MESSAGE = "message"
	STICKER = "sticker"
	TOTAL   = "total"
)

// MC is handler for command `mc`.
func MC() module.Module {
	handler := func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) {
		text := "啊等等，刚才数到多少来着，完了，忘记了QAQ..."
		defer func() {
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, text)
			util.SendMessage(bot, msg)
		}()
		resSticker, err := ctx.GlobalClient().ZRevRangeWithScores(ctx.WrapKey(STICKER), 0, 3).Result()
		if err != nil {
			log.Error("Get Sticker count in redis ZRevRangeWithScores failed", zap.Error(err))
			return
		}
		err = ctx.GlobalClient().ZUnionStore(ctx.WrapKey(TOTAL), &redis.ZStore{
			Keys:      []string{ctx.WrapKey(STICKER), ctx.WrapKey(MESSAGE)},
			Weights:   nil,
			Aggregate: "",
		}).Err()
		if err != nil {
			log.Error("Redis ZUnionStore failed", zap.Error(err))
			return
		}
		resTotal, err := ctx.GlobalClient().ZRevRangeWithScores(ctx.WrapKey(TOTAL), 0, 3).Result()
		if err != nil {
			log.Error("Get total message count in redis ZRevRangeWithScores failed", zap.Error(err))
			return
		}
		text = wrapText(bot, update.Message.Chat.ID, resSticker, resTotal)
	}
	return module.Stateful(handler)
}

// MessageCount is used to count message.
func MessageCount() module.Module {
	handler := func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) {
		message := update.Message
		// We won't count commands
		if message.IsCommand() {
			return
		}
		userID := message.From.ID
		ctx.GlobalClient().ZIncr(ctx.WrapKey(getMessageType(message)), IncrKey(userID))
	}
	return module.Stateful(handler)
}

func wrapText(bot *tgbotapi.BotAPI, chatID int64, resSticker, resTotal []redis.Z) string {
	if len(resTotal) == 0 {
		return "从我记事起。。。。没人嗦过话呢"
	}
	text := "本群大水怪名单:\n"
	userID, _ := strconv.Atoi(resTotal[0].Member.(string))
	user := util.GetChatMember(bot, chatID, userID).User
	text += fmt.Sprintf("第一名：'%v'！他的一生，是龙王的一生，他把有限的生命贡献在了无限的发送 message 上，累计数量高达 %v 条！群友因为他感受到这个群还有活人，我们把最热烈 fake_ban 送给他，让他在新的一天里享受快乐的退休时光吧！\n",
		user.FirstName+user.LastName, int(resTotal[0].Score))
	if len(resTotal) > 1 {
		userID, _ := strconv.Atoi(resTotal[1].Member.(string))
		user := util.GetChatMember(bot, chatID, userID).User
		text += fmt.Sprintf("第二名：'%v'！他用上洪荒之力，水了 %v 条消息，这个数字证明了它他的决心，虽然没能夺冠，让我们仍旧把掌声送给他！\n",
			user.FirstName+user.LastName, int(resTotal[1].Score))
	}
	if len(resTotal) > 2 {
		userID, _ := strconv.Atoi(resTotal[2].Member.(string))
		user := util.GetChatMember(bot, chatID, userID).User
		text += fmt.Sprintf("第三名：'%v'！这位朋友很努力，累计水了 %v 条消息！很棒，再接再厉！\n",
			user.FirstName+user.LastName, int(resTotal[2].Score))
	}
	return text
}

// We count Stickers and other Messages separately.
func getMessageType(message *tgbotapi.Message) string {
	if message.Sticker != nil {
		return STICKER
	}
	return MESSAGE
}

// IncrKey return a ZSET config to increase message count
func IncrKey(userID int) *redis.Z {
	return &redis.Z{
		Score:  1,
		Member: strconv.Itoa(userID),
	}
}

// DecrKey return a ZSET config to decrease message count
func DecrKey(userID int) *redis.Z {
	return &redis.Z{
		Score:  -1,
		Member: strconv.Itoa(userID),
	}
}
