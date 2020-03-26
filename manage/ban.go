package manage

import (
	"csust-got/command"
	"csust-got/util"
	"fmt"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"log"
	"math/rand"
	"strconv"
	"time"
)

// BanMyself is a handle for command `ban_myself`, which can ban yourself
func BanMyself(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	sec := time.Duration(rand.Intn(30)+90) * time.Second
	chatID := update.Message.Chat.ID
	text := "太强了，我居然ban不掉您，您TQL！"
	if BanSomeone(bot, chatID, update.Message.From.ID, sec) {
		text = "我实现了你的愿望！现在好好享用这" + strconv.FormatInt(int64(sec.Seconds()), 10) + "秒~"
	}
	message := tgbotapi.NewMessage(chatID, text)
	message.ReplyToMessageID = update.Message.MessageID
	util.SendMessage(bot, message)
}

// Ban is handle for command `ban`.
func Ban(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	cmd, _ := command.FromMessage(update.Message)
	banTime, err := time.ParseDuration(cmd.Arg(0))
	bigBrother := update.Message.From
	banTarget := bigBrother
	if update.Message.ReplyToMessage != nil {
		banTarget = update.Message.ReplyToMessage.From
	}
	if err != nil {
		banTime = time.Duration(rand.Intn(30) + 90)
	}
	chatID := update.Message.Chat.ID
	text := "我没办法完成你要我做的事……即便我已经很努力了……结局还是如此。"
	if BanSomeone(bot, chatID, banTarget.ID, banTime) {
		if banTarget.ID == bigBrother.ID {
			text = "我可能没有办法帮你完成你要我做的事情……只好……对不起！"
		} else {
			text = fmt.Sprintf("委派下来的工作已经做完了。%s 将会沉默 %d 秒。只不过……你真的希望事情变这样吗？",
				util.GetName(*banTarget), int64(banTime.Seconds()))
		}
	}
	message := tgbotapi.NewMessage(chatID, text)
	message.ReplyToMessageID = update.Message.MessageID
	util.SendMessage(bot, message)
}

// BanSomeone Use to ban someone, return true if success.
func BanSomeone(bot *tgbotapi.BotAPI, chatID int64, userID int, duration time.Duration) bool {

	chatMember := tgbotapi.ChatMemberConfig{
		ChatID: chatID,
		UserID: userID,
	}

	canSendMessages := false

	restrictConfig := tgbotapi.RestrictChatMemberConfig{
		ChatMemberConfig:      chatMember,
		UntilDate:             time.Now().Add(duration).UTC().Unix(),
		CanSendMessages:       &canSendMessages,
		CanSendMediaMessages:  nil,
		CanSendOtherMessages:  nil,
		CanAddWebPagePreviews: nil,
	}

	resp, err := bot.RestrictChatMember(restrictConfig)
	if err != nil {
		log.Println("ERROR: Can't restrict chat member.")
		log.Println(err.Error())
		return false
	}
	if !resp.Ok {
		log.Println("ERROR: Can't restrict chat member.")
		log.Println(resp.Description)
		return false
	}
	return true
}
