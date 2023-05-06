package restrict

import (
	"fmt"
	"math/rand"
	"time"

	"csust-got/config"
	"csust-got/entities"
	"csust-got/orm"
	"csust-got/util"

	. "gopkg.in/telebot.v3"
)

// FakeBan fake ban someone.
func FakeBan(m *Message) {
	cmd := entities.FromMessage(m)
	banTime, err := time.ParseDuration(cmd.Arg(0))
	if err != nil {
		banTime = time.Duration(40+rand.Intn(80)) * time.Second
	}
	ExecFakeBan(m, banTime)
}

// Kill someone.
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
		util.SendReply(m.Chat, "阿哲，您也太不huge了，建议make a huge ban", m)
		return false
	}
	if orm.IsFakeBanInCD(m.Chat.ID, m.Sender.ID) {
		banCD := orm.GetBannerDuration(m.Chat.ID, m.Sender.ID)
		msg := fmt.Sprintf("技能冷却剩余时长：%v，现在您应当保持沉默，如果他罪不可赦，请寻求其他人的帮助。", banCD)
		util.SendReply(m.Chat, msg, m)
		return false
	}
	return true
}

// ExecFakeBan exec fake ban.
func ExecFakeBan(m *Message, d time.Duration) {
	if !fakeBanCheck(m, d) {
		return
	}
	banned := m.ReplyTo.Sender
	bannedName := util.GetName(banned)
	text := fmt.Sprintf("好了，我出发了，我将会追杀 %s，直到时间过去所谓“%v”。", bannedName, d)
	if banned.ID == config.GetBot().Me.ID {
		// ban who want to ban bot
		text = config.BotConfig.MessageConfig.RestrictBot
		banned = m.Sender
	} else if banned.ID == m.Sender.ID {
		// they want to ban themselves
		text = fmt.Sprintf("那我就不客气了，我将会追杀你，直到时间过去所谓“%v”。", d)
	}
	// check if user 'banned' already banned
	maxAdd := time.Duration(config.BotConfig.RestrictConfig.FakeBanMaxAddSeconds) * time.Second
	ad := d
	if ad > maxAdd {
		ad = maxAdd
	}
	if orm.AddBanDuration(m.Chat.ID, m.Sender.ID, banned.ID, ad) {
		text = fmt.Sprintf("好耶，成功为 %s 追加%v，希望 %s 过得开心", bannedName, ad, bannedName)
		util.SendReply(m.Chat, text, m)
		return
	}
	if !orm.Ban(m.Chat.ID, m.Sender.ID, banned.ID, d) {
		text = "对不起，我没办法完成想让我做的事情——我的记忆似乎失灵了。但这也是一件好事……至少我能有短暂的安宁。"
	}
	util.SendReply(m.Chat, text, m)
}
