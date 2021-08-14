package base

import (
	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"csust-got/prom"
	"csust-got/util"
	"fmt"
	"strings"

	"go.uber.org/zap"
	. "gopkg.in/tucnak/telebot.v3"
)

// MC we not use message count anymore
func MC(m *Message) {
	if !config.BotConfig.PromConfig.Enabled {
		util.SendReply(m.Chat, "再mc自杀", m)
		return
	}
	msgR := util.SendMessage(m.Chat, "稍等。。。")

	cmd := entities.FromMessage(m)
	var (
		data    []prom.MsgCount
		err     error
		msgType string
	)
	t := strings.TrimLeft(cmd.Arg(0), "-")
	switch t {
	case "sticker", "s":
		data, err = prom.QueryStickerCount(m.Chat.Title)
		msgType = "sticker"
	default:
		data, err = prom.QueryMessageCount(m.Chat.Title)
		msgType = "message"
	}

	if err != nil {
		log.Error("MC error", zap.Error(err))
		util.EditMessage(msgR, "算了，再mc自杀！！！")
		return
	}
	if len(data) == 0 {
		util.EditMessage(msgR, "wuuwwu, 再mc自杀！")
		return
	}

	text := "本群大水怪名单:\n"
	// rankCN := []string{"零", "一", "二", "三", "四", "wu", "六", "七", "八", "九", "十"}
	text += fmt.Sprintf("第一名：'%v'！他的一生，是龙王的一生，他把有限的生命贡献在了无限的发送 %v 上，24h内 %v 数量高达 %v 条！群友因为他感受到这个群还有活人，我们把最热烈 fake_ban 送给他，让他在新的一天里享受快乐的退休时光吧！\n",
		data[0].Name, msgType, msgType, data[0].Value)
	if len(data) > 1 {
		text += fmt.Sprintf("第二名：'%v'！他用上洪荒之力，在24h内水了 %v 条 %v ，这个数字证明了它他的决心，虽然没能夺冠，让我们仍旧把掌声送给他！\n",
			data[1].Name, data[1].Value, msgType)
	}
	if len(data) > 2 {
		text += fmt.Sprintf("第三名：'%v'！这位朋友很努力，在24h内水了 %v 条 %v ！很棒，再接再厉！\n",
			data[2].Name, data[2].Value, msgType)
	}
	util.EditMessage(msgR, text)
}
