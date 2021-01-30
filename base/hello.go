package base

import (
	"csust-got/config"
	"csust-got/entities"
	"csust-got/orm"
	"csust-got/util"
	"fmt"
	. "gopkg.in/tucnak/telebot.v2"
	"math/rand"
	"strconv"
	"time"
)

var timeZoneCST, _ = time.LoadLocation("Asia/Shanghai")

var helloText = []string{
	"",
	"我是大五，大五的大，大五的wu，wuwuwuwuwuwuwuwu~",
	"我是一只只会嗦hello的咸鱼.",
}

// Hello is handle for command `hello`
func Hello(m *Message) {
	util.SendMessage(m.Chat, "hello ^_^"+util.RandomChoice(helloText))
}

// HelloToAll is handle for command `hello_to_all`
func HelloToAll(m *Message) {
	text := "大家好!"
	if m.Private() {
		text = "你好!"
	}
	text += util.RandomChoice(helloText)
	util.SendReply(m.Chat, text, m)
}

// Links is handle for command `links`
func Links(m *Message) {
	util.SendMessage(m.Chat, config.BotConfig.MessageConfig.Links, NoPreview)
}

// Shutdown is handler for command `shutdown`
func Shutdown(m *Message) {
	if orm.IsShutdown(m.Chat.ID) {
		util.SendReply(m.Chat, "我已经睡了，还请不要再找我了，可以使用/boot命令叫醒我……晚安:)", m)
		return
	}
	orm.Shutdown(m.Chat.ID)
	text := GetHitokoto("i", false) + " 明天还有明天的苦涩，晚安:)"
	if !orm.IsShutdown(m.Chat.ID) {
		text = "睡不着……:("
	}
	util.SendReply(m.Chat, text, m)
}

// Boot is handler for command `boot`
func Boot(m *Message) {
	text := GetHitokoto("i", false) + " 早上好，新的一天加油哦！:)"
	orm.Boot(m.Chat.ID)
	if orm.IsShutdown(m.Chat.ID) {
		text = "不想起:("
	}
	util.SendReply(m.Chat, text, m)
}

// Sleep is handle for command `sleep`
func Sleep(m *Message) {
	msg := ""
	t := time.Now().In(timeZoneCST)
	if t.Hour() < 6 || t.Hour() >= 18 {
		msg = "晚安, 明天醒来就能看到我哦！"
	} else if t.Hour() >= 11 && t.Hour() < 15 {
		msg = "wu安, 醒来就能看到我哦！"
	} else {
		msg = "醒来就能看到我哦！"
	}
	util.SendReply(m.Chat, msg, m)
}

// NoSleep is handle for command `no_sleep`
func NoSleep(m *Message) {
	util.SendReply(m.Chat, config.BotConfig.MessageConfig.NoSleep, m)
}

// Forward is handle for command `forward`
func Forward(m *Message) {
	command := entities.FromMessage(m)
	historyID := rand.Intn(m.ID) + 1
	if command.Argc() > 0 {
		id, ok := util.ParseNumberAndHandleError(m, command.Arg(0), util.NewRangeInt(0, m.ID))
		if ok {
			historyID = id
		} else {
			return
		}
	}

	forwardMsg := &Message{
		ID:   historyID,
		Chat: m.Chat,
	}

	if _, err := config.GetBot().Forward(m.Chat, forwardMsg); err != nil {
		const msgFmt = "我们试图找到那条消息[%d]，但是它已经永远的消失在了历史记录的长河里，对此我们深表遗憾。诀别来的总是那么自然，在你注意到时发现已经消失，希望你能珍惜现在的热爱。"
		util.SendReply(m.Chat, fmt.Sprintf(msgFmt, historyID), m)
	}
}

// History is handle for command `history`
// func History(m *Message) {
// 	command, _ := entities.FromMessage(m)
// 	historyID := rand.Intn(m.ID) + 1
// 	if command.Argc() > 0 {
// 		id, ok := util.ParseNumberAndHandleError(m, command.Arg(0), util.NewRangeInt(0, m.ID))
// 		if ok {
// 			historyID = id
// 		} else {
// 			return
// 		}
// 	}
// 	msg := fmt.Sprintf("https://t.me/c/%v/%v", chatIDToStr(m.Chat.ID), historyID)
// 	util.SendReply(m.Chat, msg, m)
// }

// func chatIDToStr(chatID int64) string {
// 	chatNum := chatID
// 	if chatNum < 0 {
// 		chatNum *= -1
// 	}
// 	chatStr := strconv.FormatInt(chatNum, 10)
// 	if chatStr[0] == '1' {
// 		chatStr = chatStr[1:]
// 	}
// 	for chatStr[0] == '0' {
// 		chatStr = chatStr[1:]
// 	}
// 	return chatStr
// }

// FakeBanMyself is handle for command `fake_ban_myself`.
// Use it to just get a reply like command `ban_myself`.
func FakeBanMyself(m *Message) {
	sec := time.Duration(rand.Intn(60)+60) * time.Second
	text := "我实现了你的愿望！现在好好享用这" + strconv.FormatInt(int64(sec.Seconds()), 10) + "秒~"
	util.SendReply(m.Chat, text, m)
}
