package base

import (
	"csust-got/config"
	"csust-got/log"
	"csust-got/prom"
	"csust-got/util"
	"fmt"
	"go.uber.org/zap"
	. "gopkg.in/tucnak/telebot.v2"
)

// MC we not use message count anymore
func MC(m *Message) {
	if !config.BotConfig.PromConfig.Enabled {
		util.SendReply(m.Chat, "再mc自杀", m)
		return
	}
	data, err := prom.QueryMessageCount(m.Chat.Title)
	if err != nil {
		log.Error("MC error", zap.Error(err))
		util.SendReply(m.Chat, "再mc自杀！！！", m)
		return
	}
	if len(data) == 0 {
		util.SendReply(m.Chat, "再mc自杀！", m)
		return
	}
	text := "本群大水怪名单(数据有半分钟延迟):\n"
	// rankCN := []string{"零", "一", "二", "三", "四", "wu", "六", "七", "八", "九", "十"}
	text += fmt.Sprintf("第一名：'%v'！他的一生，是龙王的一生，他把有限的生命贡献在了无限的发送 message 上，24h内数量高达 %v 条！群友因为他感受到这个群还有活人，我们把最热烈 fake_ban 送给他，让他在新的一天里享受快乐的退休时光吧！\n",
		data[0].Name, data[0].Value)
	if len(data) > 1 {
		text += fmt.Sprintf("第二名：'%v'！他用上洪荒之力，在24h内水了 %v 条消息，这个数字证明了它他的决心，虽然没能夺冠，让我们仍旧把掌声送给他！\n",
			data[1].Name, data[1].Value)
	}
	if len(data) > 2 {
		text += fmt.Sprintf("第三名：'%v'！这位朋友很努力，在24h内水了 %v 条消息！很棒，再接再厉！\n",
			data[2].Name, data[2].Value)
	}
	util.SendMessage(m.Chat, text)
}
