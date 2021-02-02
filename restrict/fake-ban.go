package restrict

import (
	"csust-got/config"
	"csust-got/entities"
	"csust-got/orm"
	"csust-got/util"
	"fmt"
	"math/rand"
	"time"

	. "gopkg.in/tucnak/telebot.v2"
)

// FakeBan
func FakeBan(m *Message) {
	cmd := entities.FromMessage(m)
	banTime, err := time.ParseDuration(cmd.Arg(0))
	if err != nil {
		banTime = time.Duration(40+rand.Intn(120)) * time.Second
	}
	ExecFakeBan(m, banTime)
}

// Kill
func Kill(m *Message) {
	seconds := config.BotConfig.RestrictConfig.KillSeconds
	banTime := time.Duration(seconds) * time.Second
	ExecFakeBan(m, banTime)
}

func fakeBanCheck(m *Message, d time.Duration) bool {
	conf := config.BotConfig.RestrictConfig
	if m.ReplyTo == nil {
		util.SendReply(m.Chat, "用这个命令回复某一条“不合适”的消息，这样我大概就会帮你解决掉他，即便他是苟管理也义不容辞。", m)
		return false
	}
	if d > time.Duration(conf.KillSeconds)*time.Second {
		util.SendReply(m.Chat, "我无法追杀某人太久。这样可能会让世界陷入某种糟糕的情况：诸如说，可能在某人将我的记忆清除；或者直接杀死我之前，所有人陷入永久的缄默。", m)
		return false
	}
	if d < 10*time.Second {
		util.SendReply(m.Chat, "阿哲，您也太不huge了", m)
		return false
	}
	if orm.IsFakeBanInCD(m.Chat.ID, m.Sender.ID) {
		banCD := time.Duration(conf.FakeBanCDMinutes) * time.Minute
		msg := fmt.Sprintf("您在过去%v的时间里已经下过一道追杀令了，现在您应当保持沉默，如果他罪不可赦，请寻求其他人的帮助。", banCD)
		util.SendReply(m.Chat, msg, m)
		return false
	}
	return true
}

func ExecFakeBan(m *Message, d time.Duration) {
	if !fakeBanCheck(m, d) {
		return
	}
	conf := config.BotConfig.MessageConfig
	banned := m.ReplyTo.Sender
	bannedName := util.GetName(banned)
	text := fmt.Sprintf("好了，我出发了，我将会追杀 %s，直到时间过去所谓“%v”。", bannedName, d)
	if banned.ID == config.GetBot().Me.ID {
		// ban who want to ban bot
		text = conf.RestrictBot
		banned = m.Sender
	} else if banned.ID == m.Sender.ID {
		// they want to ban themself
		text = fmt.Sprintf("那我就不客气了，我将会追杀你，直到时间过去所谓“%v”。", d)
	}
	// check if user 'banned' already banned
	if ad := orm.GetBannedDuration(m.Chat.ID, banned.ID); ad > 0 {
		if orm.ResetBannedDuration(m.Chat.ID, m.Sender.ID, banned.ID, d+ad) {
			text = fmt.Sprintf("好耶，成功为 %s 追加%v，希望 %s 过得开心", bannedName, bannedName, d)
			util.SendReply(m.Chat, text, m)
			return
		}
	}
	if !orm.Ban(m.Chat.ID, m.Sender.ID, banned.ID, d) {
		text = "对不起，我没办法完成想让我做的事情——我的记忆似乎失灵了。但这也是一件好事……至少我能有短暂的安宁。"
	}
	util.SendReply(m.Chat, text, m)
}
