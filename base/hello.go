package base

import (
	"csust-got/context"
	"csust-got/module"
	"csust-got/module/preds"
	"csust-got/orm"
	"csust-got/util"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"go.uber.org/zap"
)

// Hello is handle for command `hello`
func Hello(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	message := update.Message
	chatID := message.Chat.ID

	messageReply := tgbotapi.NewMessage(chatID, "hello ^_^")

	// 如果消息来自群里，但并不是由命令触发的，就以reply的形式发送
	if message.Chat.IsGroup() && !message.IsCommand() {
		messageReply.ReplyToMessageID = message.MessageID
	}

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
	text += "我是大五，大五的大，大五的wu，wuwuwuwuwuwuwuwu~"

	messageReply := tgbotapi.NewMessage(chatID, text)
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
