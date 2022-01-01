package base

import (
	"fmt"
	"math/rand"
	"strconv"
	"time"

	"csust-got/config"
	"csust-got/entities"
	"csust-got/orm"
	"csust-got/util"

	. "gopkg.in/tucnak/telebot.v3"
)

var helloText = []string{
	"",
	"我是大五，大五的大，大五的wu，wuwuwuwuwuwuwuwu~",
	"我是一只只会嗦hello的咸鱼.",
}

// Hello is handle for command `hello`.
func Hello(ctx Context) error {
	return ctx.Reply("hello ^_^ " + util.RandomChoice(helloText))
}

// HelloToAll is handle for command `hello_to_all`.
func HelloToAll(ctx Context) error {
	text := "大家好!"
	if ctx.Message().Private() {
		text = "你好!"
	}
	return ctx.Send(text + util.RandomChoice(helloText))
}

// Links is handle for command `links`.
func Links(ctx Context) error {
	return ctx.Send(config.BotConfig.MessageConfig.Links, ModeMarkdownV2, NoPreview)
}

// Shutdown is handler for command `shutdown`.
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

// Boot is handler for command `boot`.
func Boot(m *Message) {
	text := GetHitokoto("i", false) + " 早上好，新的一天加油哦! :)"
	orm.Boot(m.Chat.ID)
	if orm.IsShutdown(m.Chat.ID) {
		text = config.BotConfig.MessageConfig.BootFailed
	}
	util.SendReply(m.Chat, text, m)
}

// Sleep is handle for command `sleep`.
func Sleep(ctx Context) error {
	t := time.Now().In(util.TimeZoneCST)
	if t.Hour() < 6 || t.Hour() >= 18 {
		return ctx.Reply("晚安, 明天醒来就能看到我哦!")
	}
	if t.Hour() >= 11 && t.Hour() < 15 {
		return ctx.Reply("wu安, 醒来就能看到我哦!")
	}
	return ctx.Reply("醒来就能看到我哦!")
}

// NoSleep is handle for command `no_sleep`.
func NoSleep(ctx Context) error {
	return ctx.Reply(config.BotConfig.MessageConfig.NoSleep)
}

// Forward is handle for command `forward`.
func Forward(m *Message) {
	command := entities.FromMessage(m)
	forwardMsg := &Message{
		Chat: m.Chat,
	}

	retry := 1
	for retry >= 0 {
		if command.Argc() > 0 {
			id, ok := util.ParseNumberAndHandleError(m, command.Arg(0), util.NewRangeInt(0, m.ID))
			retry = 0
			if !ok {
				util.SendReply(m.Chat, "嗦啥呢", m)
				return
			}
			forwardMsg.ID = id
		} else {
			forwardMsg.ID = rand.Intn(m.ID) + 1
		}

		if _, err := config.GetBot().Forward(m.Chat, forwardMsg); err == nil {
			return
		}

		retry--
	}

	const msgFmt = "我们试图找到那条消息[%d]，但是它已经永远的消失在了历史记录的长河里，对此我们深表遗憾。诀别来的总是那么自然，在你注意到时发现已经消失，希望你能珍惜现在的热爱。"
	util.SendReply(m.Chat, fmt.Sprintf(msgFmt, forwardMsg.ID), m)
}

// FakeBanMyself is handle for command `fake_ban_myself`.
// Use it to just get a reply like command `ban_myself`.
func FakeBanMyself(ctx Context) error {
	sec := time.Duration(rand.Intn(60)+60) * time.Second
	text := "我实现了你的愿望! 现在好好享用这" + strconv.FormatInt(int64(sec.Seconds()), 10) + "秒~"
	return ctx.Reply(text)
}
