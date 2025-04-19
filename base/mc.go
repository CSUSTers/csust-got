package base

import (
	"fmt"
	"html"
	"slices"
	"strconv"
	"strings"
	"time"

	"csust-got/config"
	"csust-got/log"
	"csust-got/orm"
	"csust-got/util/restrict"

	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

type mcSascrfice struct {
	ChatID int64
	UserID int64

	TotalSascrfice int
	Sascrfices     []int
	Odds           int
}

func computeSas(idx int) int {
	baseSas := config.BotConfig.McConfig.Sacrifices
	if idx >= len(baseSas) {
		return baseSas[len(baseSas)-1]
	}
	return baseSas[idx]
}

func reburnHelper(chatID, prayer int64) (bool, []*mcSascrfice, error) {
	// check bot dead or not
	souls, err := orm.GetMcDead(chatID)
	if err != nil {
		log.Error("reburn get souls failed", zap.Int64("chat", chatID), zap.Error(err))
		return false, nil, err
	} else if len(souls) == 0 {
		return false, nil, nil
	}

	// compute sascrifces
	sascrfices := make([]*mcSascrfice, 0, len(souls))
	for i, soul := range souls {
		idx := len(souls) - 1 - i
		var userID int64
		userID, err = strconv.ParseInt(soul, 10, 64)
		if err != nil {
			log.Error("reburn parse userID failed", zap.Int64("chat", chatID), zap.Error(err))
			continue
		}

		sas := computeSas(idx)

		var currSas *mcSascrfice
		for i := range sascrfices {
			if sascrfices[i].UserID == userID {
				currSas = sascrfices[i]
				currSas.TotalSascrfice += sas
				currSas.Sascrfices = append(currSas.Sascrfices, sas)

				// move to end
				if i < len(sascrfices)-1 {
					copy(sascrfices[i:], sascrfices[i+1:])
					sascrfices[len(sascrfices)-1] = currSas
				}
				break
			}
		}
		if currSas == nil {
			currSas = &mcSascrfice{
				ChatID:         chatID,
				UserID:         userID,
				TotalSascrfice: sas,
				Sascrfices:     []int{sas},
				Odds:           1,
			}
			sascrfices = append(sascrfices, currSas)
		}
	}

	// check sascrfice odds
	for _, sascrfice := range sascrfices {
		var isPrayed bool
		isPrayed, err = orm.IsPrayerInPost(sascrfice.ChatID, sascrfice.UserID)
		if err != nil {
			log.Error("reburn check pray failed", zap.Int64("chat", chatID), zap.Error(err))
			continue
		}
		if isPrayed {
			sascrfice.Odds = config.BotConfig.McConfig.Odds
		}
		_ = orm.ClearPrayer(sascrfice.ChatID, sascrfice.UserID)
	}

	err = orm.ClearMcDead(chatID)
	if err != nil {
		log.Error("reburn clear dead failed", zap.Int64("chat", chatID), zap.Error(err))
	}

	// set current reburn prayer in orm
	err = orm.SetPrayer(chatID, prayer)
	if err != nil {
		log.Error("reburn set prayer failed", zap.Int64("chat", chatID), zap.Error(err))
	}

	return true, sascrfices, nil
}

// MC handle `/mc` command
func MC(ctx tb.Context) error {
	if config.BotConfig.McConfig.Mc2Dead <= 0 {
		return ctx.Reply("再mc自杀（也不一定）")
	}

	chatID := ctx.Chat().ID
	userID := ctx.Sender().ID

	ok, souls, err := orm.McRaiseSoul(chatID, userID)
	if err != nil {
		log.Error("mc raise soul failed", zap.Int64("chat", chatID), zap.Int64("user", userID), zap.Error(err))
		_ = ctx.Reply("哪里出错了呢")
		return err
	}
	if !ok {
		_ = ctx.Reply("再MC自杀")
		return nil
	}

	err = orm.McDead(chatID, souls)
	if err != nil {
		log.Error("mc dead failed", zap.Int64("chat", chatID), zap.Strings("users", souls), zap.Error(err))
		_ = ctx.Reply("哪里出错了呢")
		return err
	}

	return ctx.Reply("啊，我死了。来个好心的大魔术师使用 /reburn 复活我吧。")
}

// Reburn handle `/reburn` command
func Reburn(ctx tb.Context) error {
	if config.BotConfig.McConfig.Mc2Dead <= 0 {
		return nil
	}

	chatID := ctx.Chat().ID
	userID := ctx.Sender().ID

	ok, sascrfices, err := reburnHelper(chatID, userID)
	if err != nil {
		log.Error("reburn failed", zap.Int64("chat", chatID), zap.Int64("user", userID), zap.Error(err))
		_ = ctx.Reply("哪里出错了呢")
		return err
	}

	if !ok {
		_ = ctx.Reply("？？？")
		return nil
	}

	slices.Reverse(sascrfices)
	missingUsers := make([]int64, 0)
	replyText := []string{
		"祭品！我收下了！",
	}
	replyText2 := []string{}
	for _, s := range sascrfices {
		chat := ctx.Chat()
		if chat.ID != s.ChatID {
			continue
		}
		m, err := ctx.Bot().ChatMemberOf(chat, tb.ChatID(s.UserID))
		if err != nil {
			log.Error("get chat member failed", zap.Int64("chat", s.ChatID), zap.Int64("user", s.UserID), zap.Error(err))
			missingUsers = append(missingUsers, s.UserID)
			continue
		}

		d := time.Duration(s.TotalSascrfice) * time.Second * time.Duration(s.Odds)
		res := restrict.BanOrKill(chat, m.User, true, d)

		var userString string
		if m.User.Username != "" {
			userString = html.EscapeString("@" + m.User.Username)
		} else {
			userString = fmt.Sprintf(`<a href="tg://user?id=%d">%s %s</a>`, m.User.ID,
				html.EscapeString(m.User.FirstName),
				html.EscapeString(m.User.LastName))
		}

		if res.Success {
			text := fmt.Sprintf(`%s 献祭了 %v 作为祭品`, userString, d)
			if s.Odds > 1 {
				text += fmt.Sprintf("，因为他背叛了我，所以需要额外献上%d倍", s.Odds)
			}
			replyText = append(replyText, text)
		} else {
			text := fmt.Sprintf(`%s 不知为何没能献祭 %v 作为祭品`, userString, d)
			replyText2 = append(replyText2, text)
		}
	}

	if len(missingUsers) > 0 {
		log.Info("reburn missing users", zap.Int64("chat", chatID), zap.Int64s("users", missingUsers))
	}
	return ctx.Send(strings.Join(append(replyText, replyText2...), "\n"),
		&tb.SendOptions{ParseMode: tb.ModeHTML})
}
