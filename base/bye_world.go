package base

import (
	"csust-got/config"
	"csust-got/entities"
	"csust-got/orm"
	"csust-got/util"
	"time"

	. "gopkg.in/telebot.v3"
)

// ByeWorld auto delete message.
func ByeWorld(m *Message) {
	command := entities.FromMessage(m)

	deleteFrom := 5 * time.Minute
	if command.Argc() > 0 {
		arg := command.Arg(0)
		d, err := time.ParseDuration(arg)
		if err != nil {
			util.SendReply(m.Chat, "哎呀，时间扭曲失败了！请重新设置时间，比如 '3m' 代表 3 分钟，再试一次吧！😅", m)
			return
		}
		if d < time.Minute || d > 5*time.Minute {
			util.SendReply(m.Chat, "哇哦，时间选择超越了科学的界限！我可不是时间旅行者，请将参数设置在 1 分钟到 5 分钟之间，不要试图挑战宇宙法则哦！😄", m)
			return
		}
		deleteFrom = d
	}

	botInChat, err := config.BotConfig.Bot.ChatMemberOf(m.Chat, config.BotConfig.Bot.Me)
	if err != nil {
		util.SendReply(m.Chat, "哎呀，一不小心就在时间的湍流中迷失了自我，也许现在不是时候，让我们重新来过吧！😅", m)
		return
	}

	if !botInChat.CanDeleteMessages {
		util.SendReply(m.Chat, "抱歉，我好像没有足够的权力来执行这个操作。或许需要检查一下我的权限设置，或者有其他魔法师可以帮助你实现这个愿望！😅", m)
		return
	}

	_, isBye, _ := orm.IsByeWorld(m.Chat.ID, m.Sender.ID)

	err = orm.SetByeWorldDuration(m.Chat.ID, m.Sender.ID, deleteFrom)
	if err != nil {
		util.SendReply(m.Chat, "哎呀，咱记性不太好，没能记住你的命令，你刚才说啥来着，让我们重新来过，我相信下一次一定会成功的！😄", m)
		return
	}

	if isBye {
		util.SendReply(m.Chat, "看来你是个不甘寂寞的时空探险家，参数已经得到你的精心调整，时光机继续嗖嗖嗖地前进，享受这趟奇幻之旅吧！😄", m)
		return
	}

	util.SendReply(m.Chat, "哼哼，消息已经穿越时光隧道，定时删除模式已启动！等待时光倒流的奇迹吧！😄", m)

}

// HelloWorld disable auto delete message.
func HelloWorld(m *Message) {
	_, isBye, err := orm.IsByeWorld(m.Chat.ID, m.Sender.ID)
	if !isBye && err == nil {
		util.SendReply(m.Chat, "哦，时间旅行器似乎被遗忘在角落里了！但没关系，我们永不受限，继续探索这个未知的世界，自由自在地畅游吧，没有时间束缚！😎🌟", m)
		return
	}

	err = orm.DeleteByeWorldDuration(m.Chat.ID, m.Sender.ID)
	if err != nil {
		util.SendReply(m.Chat, "嗯，看来时间是一把顽固的钥匙！我们无法完全打破时间的牢笼，但不用担心，让我们与时间共舞，看看它何时决定放手吧，谁能预测时间的奇妙呢？😎⏳", m)
		return
	}

	util.SendReply(m.Chat, "恭喜，现实世界已经恢复正常运转！我们继续前进，不再受时间和空间的束缚！😎🚀", m)

}
