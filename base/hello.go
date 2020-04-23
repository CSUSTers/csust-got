package base

import (
	"csust-got/context"
	"csust-got/module"
	"csust-got/module/preds"
	"csust-got/util"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
)

const errMessage = `过去那些零碎的细语并不构成这个世界：对于你而言，该看，该想，该体会身边那些微小事物的律动。
忘了这些话吧。忘了这个功能吧——只今它已然不能给予你更多。而你的未来属于新的旅途：去欲望、去收获、去爱、去恨。
去做只属于你自己的选择，写下只有你深谙个中滋味的诗篇。我们的生命以后可能还会交织之时，但如今，再见。`

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

// IsoHello is handle for auto hello to someone, just for test, we not use it.
func IsoHello(tgbotapi.Update) module.Module {
	handle := func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) {
		key := "enabled"
		enabled, err := util.GetBool(ctx, key)
		if err != nil {
			log.Println("ERROR: failed to access redis.", err)
		}

		if preds.IsCommand("hello").ShouldHandle(update) {
			if err := util.ToggleBool(ctx, key); err != nil {
				log.Println("ERROR: failed to access redis.", err)
			}
		}

		if enabled {
			util.SendMessage(bot, tgbotapi.NewMessage(update.Message.Chat.ID, "hello ……——……"))
		}
	}
	return module.Stateful(handle)
}

// Shutdown is handler for command `shutdown`
func Shutdown(update tgbotapi.Update) module.Module {
	handler := func(ctx context.Context, update tgbotapi.Update, bot *tgbotapi.BotAPI) module.HandleResult {
		key := "shutdown"
		shutdown, err := util.GetBool(ctx, key)
		if err != nil {
			log.Println("ERROR: failed to access redis.", err)
		}
		if preds.IsCommand("shutdown").
			Or(preds.IsCommand("halt")).
			Or(preds.IsCommand("poweroff")).ShouldHandle(update) {
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "明天还有明天的苦涩，晚安:)")
			if shutdown {
				msg.Text = "我已经睡了，还请不要再找我了……晚安:)"
			} else if err := util.WriteBool(ctx, key, true); err != nil {
				log.Println("ERROR: failed to access redis.", err)
				msg.Text = "睡不着……:("
			}
			util.SendMessage(bot, msg)
			return module.DoDeferred
		}
		if preds.IsCommand("boot").ShouldHandle(update) {
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "早上好，新的一天加油哦！:)")
			if err := util.WriteBool(ctx, key, false); err != nil {
				log.Println("ERROR: failed to access redis.", err)
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

var OneWordApi, _ = url.Parse("https://v1.hitokoto.cn/?encode=text")
var OneWord = mapToHTML(func(message *tgbotapi.Message) string {
	resp, err := http.Get(OneWordApi.String())
	if err != nil {
		return errMessage
	}
	defer resp.Body.Close()
	word, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return errMessage
	}
	return string(word)
})
