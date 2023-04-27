package gacha

import (
	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"csust-got/orm"
	"encoding/json"
	"fmt"
	"go.uber.org/zap"
	"gopkg.in/telebot.v3"
	"math/rand"
	"strconv"
)

// execute a gacha. Result: 3, 4, 5 stands for 3 star, 4 star, 5 star.
func execute(tenant *config.GachaTenant) int64 {
	tenant.FiveStar.Counter++
	tenant.FourStar.Counter++

	if tenant.FiveStar.Counter >= tenant.FiveStar.FailBackNum {
		tenant.FiveStar.Counter = 0
		return 5
	}

	if tenant.FourStar.Counter >= tenant.FourStar.FailBackNum {
		tenant.FourStar.Counter = 0
		return 4
	}

	random := rand.Float64() * 100

	if random < tenant.FiveStar.Probability {
		tenant.FiveStar.Counter = 0
		return 5
	}

	if random < tenant.FourStar.Probability {
		tenant.FourStar.Counter = 0
		return 4
	}

	return 3
}

// PerformGaCha performs a gacha with the given chatID and returns the result
func PerformGaCha(chatID int64) (int64, error) {
	tenant, err := orm.LoadGachaSession(chatID)
	if err != nil {
		log.Error("[GaCha]: load tenant session failed", zap.Error(err))
		return 0, err
	}

	result := execute(&tenant)

	err = orm.SaveGachaSession(chatID, tenant)
	if err != nil {
		log.Error("[GaCha]: save tenant session failed", zap.Error(err))
		return 0, err
	}
	return result, nil
}

// SetGachaSession sets the gacha session for the caller chat
func SetGachaSession(ctx telebot.Context) error {
	m := ctx.Message()
	command := entities.FromMessage(m)
	arg := command.ArgAllInOneFrom(0)
	if arg == "" {
		// default value： 5star : 0.6, 90; 4star : 5.7, 10
		arg = "{\"FiveStar\":{\"Counter\":0,\"Probability\":0.6,\"FailBackNum\":90},\"" +
			"FourStar\":{\"Counter\":0,\"Probability\":5.7,\"FailBackNum\":10},\"ID\":\"" +
			strconv.FormatInt(ctx.Chat().ID, 10) + "\"}"
	}
	var tenant config.GachaTenant
	err := json.Unmarshal([]byte(arg), &tenant)
	if err != nil {
		log.Error("[GaCha]: unmarshal tenant failed", zap.Error(err))
		return ctx.Reply("Failed")
	}
	err = ctx.Reply("Modify success")
	if err != nil {
		log.Error("[GaCha]: reply failed", zap.Error(err))
	}
	return orm.SaveGachaSession(ctx.Chat().ID, tenant)
}

// WithMsgRpl is the handler for gacha command
func WithMsgRpl(ctx telebot.Context) error {
	m := ctx.Message()
	result, err := PerformGaCha(m.Chat.ID)
	if err != nil {
		log.Error("[GaCha]: perform gacha failed", zap.Error(err))
		return ctx.Reply("Failed")
	}
	rplMsg := fmt.Sprintf("You got %d star", result)
	return ctx.Reply(rplMsg)
}
