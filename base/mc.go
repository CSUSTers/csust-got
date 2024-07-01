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

// var tmpls = []string{
// 	"第一名: '%v'! 他的一生，是龙王的一生，他把有限的生命贡献在了无限的发送 %[3]v 上，24h内 %[3]v 数量高达 %[2]v 条! 群友因为他感受到这个群还有活人，我们把最热烈 fake_ban 送给他，让他在新的一天里享受快乐的退休时光吧!\n\n",
// 	"第二名: '%v'! 他用上洪荒之力，在24h内水了 %v 条 %v ，这个数字证明了它他的决心，虽然没能夺冠，让我们仍旧把掌声送给他!\n\n",
// 	"第三名: '%v'! 这位朋友很努力，在24h内水了 %v 条 %v ! 很棒，再接再厉!\n\n",
// 	"第四名: '%v'! 他在24h内奋力发表了 %v 条 %v，他的努力让这个群更加生机勃勃！鼓掌，让我们共同见证他今后的辉煌!\n\n",
// 	"第五名: '%v'! 勇敢地踏上了发言之路，在24h内贡献了 %v 条有价值的 %v！他的脚步不可阻挡，让我们期待他未来的精彩表现！\n\n",
// 	"第六名: '%v'! 他同样勇敢地迈出了一步，24h内成功发出 %v 条 %v！我们期待这位朋友的未来进步，让群里的沟通更加繁荣！\n\n",
// 	"第七名: '%v'! 他拿出了勇气，在24h内为我们带来了 %v 条精彩的 %v！我们为他的毅力表示敬意，期待他在未来绽放光芒！\n\n",
// 	"第八名: '%v'! 这位朋友不甘示弱，在24h内也为大家贡献了 %v 条 %v！向他致敬，让我们一起为他的魄力鼓掌！\n\n",
// 	"第九名: '%v'! 努力的身影随处可见，他在24h内同样奉献了 %v 条 %v！让我们鼓励这位朋友继续前行，将群里的氛围点燃！\n\n",
// 	"第十名: '%v'! 最后，这位勇士也没落下，24h内成功发出了 %v 条 %v！他为这个群的活跃做出了贡献，让我们一起向他致敬！\n\n",
// }

// // MC we not use message count anymore.
// func MC(m *Message) {
// 	if !config.BotConfig.PromConfig.Enabled {
// 		util.SendReply(m.Chat, "再mc自杀", m)
// 		return
// 	}
// 	msgR := util.SendMessage(m.Chat, "稍等。。。")

// 	cmd := entities.FromMessage(m)
// 	var (
// 		data    []prom.MsgCount
// 		err     error
// 		msgType string
// 	)
// 	t := strings.TrimLeft(cmd.Arg(0), "-")
// 	switch t {
// 	case "sticker", "s":
// 		data, err = prom.QueryStickerCount(strconv.FormatInt(m.Chat.ID, 10))
// 		msgType = "sticker"
// 	default:
// 		data, err = prom.QueryMessageCount(strconv.FormatInt(m.Chat.ID, 10))
// 		msgType = "message"
// 	}

// 	if err != nil {
// 		log.Error("MC error", zap.Error(err))
// 		util.EditMessage(msgR, "算了，再mc自杀!!!")
// 		return
// 	}
// 	if len(data) == 0 {
// 		util.EditMessage(msgR, "wuuwwu, 再mc自杀!")
// 		return
// 	}

// 	text := generateMCMessage(data, msgType)
// 	util.EditMessage(msgR, text)
// }

// func generateMCMessage(data []prom.MsgCount, msgType string) string {
// 	text := "本群大水怪名单:\n\n"

// 	if len(data) == 0 {
// 		return "看来你在没有人烟的荒原，快找一些朋友来玩吧。"
// 	}

// 	for idx, d := range data[:util.Min(len(data), config.BotConfig.McConfig.MaxCount)] {
// 		text += fmt.Sprintf(tmpls[idx], d.Name, d.Value, msgType)
// 	}

// 	return text
// }

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
			userString = html.EscapeString(fmt.Sprintf("@%s", m.User.Username))
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
		tb.SendOptions{ParseMode: tb.ModeHTML})
}
