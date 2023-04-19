package base

import (
	"fmt"
	"strings"

	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"csust-got/prom"
	"csust-got/util"

	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
)

// MC we not use message count anymore.
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
		util.EditMessage(msgR, "算了，再mc自杀!!!")
		return
	}
	if len(data) == 0 {
		util.EditMessage(msgR, "wuuwwu, 再mc自杀!")
		return
	}

	text := generateMCMessage(data, msgType)
	util.EditMessage(msgR, text)
}

func generateMCMessage(data []prom.MsgCount, msgType string) string {
	text := "本群大水怪名单:\n\n"

	text += fmt.Sprintf("第一名: '%v'! 他的一生，是龙王的一生，他把有限的生命贡献在了无限的发送 %v 上，24h内 %v 数量高达 %v 条! 群友因为他感受到这个群还有活人，我们把最热烈 fake_ban 送给他，让他在新的一天里享受快乐的退休时光吧!\n\n",
		data[0].Name, msgType, msgType, data[0].Value)

	if len(data) > 1 {
		text += fmt.Sprintf("第二名: '%v'! 他用上洪荒之力，在24h内水了 %v 条 %v ，这个数字证明了它他的决心，虽然没能夺冠，让我们仍旧把掌声送给他!\n\n",
			data[1].Name, data[1].Value, msgType)
	}

	if len(data) > 2 {
		text += fmt.Sprintf("第三名: '%v'! 这位朋友很努力，在24h内水了 %v 条 %v ! 很棒，再接再厉!\n\n",
			data[2].Name, data[2].Value, msgType)
	}

	if len(data) > 3 {
		text += fmt.Sprintf("第四名: '%v'! 他在24h内奋力发表了 %v 条 %v，他的努力让这个群更加生机勃勃！鼓掌，让我们共同见证他今后的辉煌!\n\n",
			data[3].Name, data[3].Value, msgType)
	}

	if len(data) > 4 {
		text += fmt.Sprintf("第五名: '%v'! 勇敢地踏上了发言之路，在24h内贡献了 %v 条有价值的 %v！他的脚步不可阻挡，让我们期待他未来的精彩表现！\n\n",
			data[4].Name, data[4].Value, msgType)
	}

	if len(data) > 5 {
		text += fmt.Sprintf("第六名: '%v'! 他同样勇敢地迈出了一步，24h内成功发出 %v 条 %v！我们期待这位朋友的未来进步，让群里的沟通更加繁荣！\n\n",
			data[5].Name, data[5].Value, msgType)
	}

	if len(data) > 6 {
		text += fmt.Sprintf("第七名: '%v'! 他拿出了勇气，在24h内为我们带来了 %v 条精彩的 %v！我们为他的毅力表示敬意，期待他在未来绽放光芒！\n\n",
			data[6].Name, data[6].Value, msgType)
	}

	if len(data) > 7 {
		text += fmt.Sprintf("第八名: '%v'! 这位朋友不甘示弱，在24h内也为大家贡献了 %v 条 %v！向他致敬，让我们一起为他的魄力鼓掌！\n\n",
			data[7].Name, data[7].Value, msgType)
	}

	if len(data) > 8 {
		text += fmt.Sprintf("第九名: '%v'! 努力的身影随处可见，他在24h内同样奉献了 %v 条 %v！让我们鼓励这位朋友继续前行，将群里的氛围点燃！\n\n",
			data[8].Name, data[8].Value, msgType)
	}

	if len(data) > 9 {
		text += fmt.Sprintf("第十名: '%v'! 最后，这位勇士也没落下，24h内成功发出了 %v 条 %v！他为这个群的活跃做出了贡献，让我们一起向他致敬！\n\n",
			data[9].Name, data[9].Value, msgType)
	}
	return text
}
