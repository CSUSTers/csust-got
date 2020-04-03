package base

import (
    "csust-got/context"
    "csust-got/module"
    "csust-got/util"
    "github.com/go-redis/redis/v7"
    tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
    "log"
    "strconv"
)

const (
    MESSAGE = "message"
    STICKER = "sticker"
    TOTAL = "total"
)

// MC is handler for command `mc`.
func MC(update tgbotapi.Update) module.Module {
    handler := func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) {
        text := "啊我脑子坏了..."
        defer func() {
            msg := tgbotapi.NewMessage(update.Message.Chat.ID, text)
            util.SendMessage(bot, msg)
        }()
        resSticker, err := ctx.GlobalClient().ZRangeWithScores(ctx.WrapKey(STICKER), 0, 3).Result()
        if err != nil {
            log.Println(err.Error())
            return
        }
        err = ctx.GlobalClient().ZUnionStore(ctx.WrapKey(TOTAL), &redis.ZStore{
            Keys:      []string{ctx.WrapKey(STICKER),ctx.WrapKey(MESSAGE)},
            Weights:   nil,
            Aggregate: "",
        }).Err()
        if err != nil {
            log.Println(err.Error())
            return
        }
        resTotal, err := ctx.GlobalClient().ZRangeWithScores(ctx.WrapKey(TOTAL), 0, 3).Result()
        if err != nil {
            log.Println(err.Error())
            return
        }
        log.Println(resSticker)
        log.Println(resTotal)
    }
    return module.Stateful(handler)
}

// MessageCount is used to count message.
func MessageCount(update tgbotapi.Update) module.Module {
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

// We count Stickers and other Messages separately.
func getMessageType(message *tgbotapi.Message) string {
    if message.Sticker != nil {
        return STICKER
    }
    return MESSAGE
}

func IncrKey(userID int) *redis.Z {
    return &redis.Z{
        Score:  1,
        Member: strconv.Itoa(userID),
    }
}

func DecrKey(userID int) *redis.Z {
    return &redis.Z{
        Score:  -1,
        Member: strconv.Itoa(userID),
    }
}