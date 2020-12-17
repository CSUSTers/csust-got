package base

import (
	"csust-got/context"
	"csust-got/entities"
	"csust-got/module"
	"csust-got/module/preds"
	"csust-got/orm"
	"csust-got/util"
	"fmt"
	"math/rand"
	"strconv"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"go.uber.org/zap"
)

var timeZoneCST, _ = time.LoadLocation("Asia/Shanghai")

var helloText = []string{
	"",
	"我是大五，大五的大，大五的wu，wuwuwuwuwuwuwuwu~",
	"我是一只只会嗦hello的咸鱼.",
}

// Hello is handle for command `hello`
func Hello(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	message := update.Message
	chatID := message.Chat.ID

	messageReply := tgbotapi.NewMessage(chatID, "hello ^_^"+util.RandomChoice(helloText))

	util.SendMessage(bot, messageReply)
}

// HelloToAll is handle for command `hello_to_all`
func HelloToAll(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	message := update.Message
	chatID := message.Chat.ID

	text := "大家好!"
	if message.Chat.IsPrivate() {
		text = "你好!"
	}
	text += util.RandomChoice(helloText)

	messageReply := tgbotapi.NewMessage(chatID, text)
	util.SendMessage(bot, messageReply)
}

// Links is handle for command `links`
func Links(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	message := update.Message
	chatID := message.Chat.ID

	txt := fmt.Sprintf("以下本群友链:\n")
	txt += fmt.Sprintf("[本校官网](https://csu.st)\n")
	txt += fmt.Sprintf("[Github](https://github.com/CSUSTers)\n")
	txt += fmt.Sprintf("[Dashboard](http://47.103.193.47:3000/d/laBgWPTGz)\n")

	messageReply := tgbotapi.NewMessage(chatID, txt)
	messageReply.ParseMode = tgbotapi.ModeMarkdownV2
	util.SendMessage(bot, messageReply)
}

// Shutdown is handler for command `shutdown`
func Shutdown(update tgbotapi.Update) module.Module {
	handler := func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) module.HandleResult {
		key := "shutdown"
		shutdown, err := orm.GetBool(ctx, key)
		if err != nil {
			zap.L().Sugar().Error("failed to access redis.", err)
		}
		if preds.IsCommand("shutdown").
			Or(preds.IsCommand("halt")).
			Or(preds.IsCommand("poweroff")).ShouldHandle(update) {
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, GetHitokoto("i", false)+" 明天还有明天的苦涩，晚安:)")
			if shutdown {
				msg.Text = "我已经睡了，还请不要再找我了，可以使用/boot命令叫醒我……晚安:)"
			} else if err := orm.WriteBool(ctx, key, true); err != nil {
				zap.L().Sugar().Error("failed to access redis.", err)
				msg.Text = "睡不着……:("
			}
			util.SendMessage(bot, msg)
			return module.DoDeferred
		}
		if preds.IsCommand("boot").ShouldHandle(update) {
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, GetHitokoto("i", false)+" 早上好，新的一天加油哦！:)")
			if err := orm.WriteBool(ctx, key, false); err != nil {
				zap.L().Sugar().Error("failed to access redis.", err)
				msg.Text = "我不愿面对这苦涩的一天……:("
			}
			util.SendMessage(bot, msg)
			return module.NextOfChain
		}
		if shutdown {
			return module.DoDeferred
		}
		return module.NextOfChain
	}
	return module.Filter(handler)
}

// Sleep is handle for command `sleep`
func Sleep(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	if rand.Intn(100) < 2 {
		NoSleep(update, bot)
		return
	}

	message := update.Message
	chatID := message.Chat.ID

	msg := ""

	t := time.Now().In(timeZoneCST)
	if t.Hour() < 6 || t.Hour() >= 18 {
		msg = "晚安, 明天醒来就能看到我哦！"
	} else if t.Hour() >= 11 && t.Hour() < 15 {
		msg = "wu安, 醒来就能看到我哦！"
	} else {
		msg = "醒来就能看到我哦！"
	}

	messageReply := tgbotapi.NewMessage(chatID, msg)
	util.SendMessage(bot, messageReply)
}

// NoSleep is handle for command `no_sleep`
func NoSleep(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	message := update.Message
	chatID := message.Chat.ID

	messageReply := tgbotapi.NewMessage(chatID, "睡你麻痹起来嗨！")
	util.SendMessage(bot, messageReply)
}

// Forward is handle for command `forward`
func Forward(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	message := update.Message
	chatID := message.Chat.ID

	command, _ := entities.FromMessage(message)
	historyID := rand.Intn(message.MessageID) + 1
	if command.Argc() > 0 {
		// message id is a int-type number
		id, err := strconv.Atoi(command.Arg(0))
		if err != nil {
			msg := tgbotapi.NewMessage(chatID, "您这数字有点不太对劲啊。要不您回去再瞅瞅？")
			msg.ReplyToMessageID = message.MessageID
			util.SendMessage(bot, msg)
			return
		} else if id < 0 || id > message.MessageID {
			msg := tgbotapi.NewMessage(chatID, "太大或是太小，都不太行。适合的，才是坠吼的。")
			msg.ReplyToMessageID = message.MessageID
			util.SendMessage(bot, msg)
			return
		} else {
			historyID = id
		}
	}

	messageReply := tgbotapi.NewForward(chatID, chatID, historyID)
	util.SendMessage(bot, messageReply)
}

// History is handle for command `history`
func History(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	message := update.Message
	chatID := message.Chat.ID

	chatIDStr := chatIDToStr(chatID)
	command, _ := entities.FromMessage(message)
	historyID := rand.Intn(message.MessageID) + 1
	if command.Argc() > 0 {
		// message id is a int-type number
		id, err := strconv.Atoi(command.Arg(0))
		if err != nil {
			msg := tgbotapi.NewMessage(chatID, "您这数字有点不太对劲啊。要不您回去再瞅瞅？")
			msg.ReplyToMessageID = message.MessageID
			util.SendMessage(bot, msg)
			return
		} else if id < 0 || id > message.MessageID {
			msg := tgbotapi.NewMessage(chatID, "太大或是太小，都不太行。适合的，才是坠吼的。")
			msg.ReplyToMessageID = message.MessageID
			util.SendMessage(bot, msg)
			return
		} else {
			historyID = id
		}
	}

	messageReply := tgbotapi.NewMessage(chatID, "https://t.me/c/"+chatIDStr+"/"+strconv.Itoa(historyID))
	messageReply.ReplyToMessageID = message.MessageID
	util.SendMessage(bot, messageReply)
}

func chatIDToStr(chatID int64) string {
	chatNum := chatID
	if chatNum < 0 {
		chatNum *= -1
	}
	chatStr := strconv.FormatInt(chatNum, 10)
	if chatStr[0] == '1' {
		chatStr = chatStr[1:]
	}
	for chatStr[0] == '0' {
		chatStr = chatStr[1:]
	}
	return chatStr
}
