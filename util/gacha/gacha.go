package gacha

import (
	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"csust-got/orm"
	"csust-got/util"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strconv"

	"go.uber.org/zap"
	"gopkg.in/telebot.v3"
)

var errEmptyTenant = errors.New("empty tenant")

// execute a gacha. Result: 3, 4, 5 stands for 3 star, 4 star, 5 star.
// 设想以下情景: 对于一个抽卡游戏，其规则是设定一个较低的初始概率p，作为每次抽中的概率，随着抽卡次数的逐渐增加，p值会渐渐增加。
// 另外还有一个保底次数n，其定义是抽到第n次时必然会抽中。但出于商业需求考虑，p值在抽卡次数小于2n/3时，会维持一个较低的概率，在2n/3次之后才会显著提升，直到达到n次p为必然发生。
// 此算法的目的是控制用户抽中的次数期望在2n/3到6n/7次。请写一个golang函数，使得对于传入的初始概率p和保底次数n，返回一个长度为n的概率数组，以满足上述要求。
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
		return 0, err
	}

	result := execute(&tenant)

	err = orm.SaveGachaSession(chatID, tenant)
	if err != nil {
		return 0, err
	}
	return result, nil
}

// setGachaSession sets the gacha session for the caller chat
func setGachaSession(m *telebot.Message) (config.GachaTenant, error) {
	command := entities.FromMessage(m)
	arg := ""
	if command == nil {
		arg = "{\"FiveStar\":{\"Counter\":0,\"Probability\":0.6,\"FailBackNum\":90},\"" +
			"FourStar\":{\"Counter\":0,\"Probability\":5.7,\"FailBackNum\":10},\"ID\":\"" +
			strconv.FormatInt(m.Chat.ID, 10) + "\"}"
	} else {
		arg = command.ArgAllInOneFrom(0)
	}
	var tenant config.GachaTenant
	err := json.Unmarshal([]byte(arg), &tenant)
	if err != nil {
		return tenant, err
	}
	if tenant == (config.GachaTenant{}) {
		return tenant, errEmptyTenant
	}
	return tenant, nil
}

// SetGachaHandle is the handler for gacha_setting command
func SetGachaHandle(ctx telebot.Context) error {
	m := ctx.Message()
	tenant, err := setGachaSession(m)
	if err != nil {
		log.Error("[GaCha]: set gacha session failed", zap.Error(err))
		err = ctx.Reply("Set Failed")
		return err
	}
	_, err = util.SendReplyWithError(m.Chat, "Modify Success", m)
	if err != nil {
		log.Error("[GaCha]: reply failed", zap.Error(err))
	}
	return orm.SaveGachaSession(m.Chat.ID, tenant)
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
