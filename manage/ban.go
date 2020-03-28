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
	if BanSomeone(bot, chatID, update.Message.From.ID, true, sec) {
		text = "我实现了你的愿望！现在好好享用这" + strconv.FormatInt(int64(sec.Seconds()), 10) + "秒~"
	}
	message := tgbotapi.NewMessage(chatID, text)
	message.ReplyToMessageID = update.Message.MessageID
	util.SendMessage(bot, message)
}

// SoftBan is handle for command `ban_soft`.
func SoftBan(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	BanCommand(update, bot, false)
}

// Ban is handle for command `ban`.
func Ban(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	BanCommand(update, bot, true)
}

// Ban is handle for command `ban`.
func BanCommand(update tgbotapi.Update, bot *tgbotapi.BotAPI, hard bool) {
	cmd, _ := command.FromMessage(update.Message)
	banTime, err := time.ParseDuration(cmd.Arg(0))
	if err != nil {
		banTime = time.Duration(rand.Intn(30)+90) * time.Second
	}
	chatID := update.Message.Chat.ID
	bigBrother := update.Message.From
	var banTarget *tgbotapi.User = nil
	if !util.CanRestrictMembers(bot, chatID, bigBrother.ID) {
		banTarget = bigBrother
	}
	text := "我没办法完成你要我做的事……即便我已经很努力了……结局还是如此。"

	if update.Message.ReplyToMessage != nil {
		if banTarget == nil {
			banTarget = update.Message.ReplyToMessage.From
		}
		if BanSomeone(bot, chatID, banTarget.ID, hard, banTime) {
			if banTarget.ID == bigBrother.ID {
				text = "我可能没有办法帮你完成你要我做的事情……只好……对不起！"
			} else {
				if hard {
					text = fmt.Sprintf("委派下来的工作已经做完了。%s 将会沉默 %d 秒，只不过……你真的希望事情变这样吗？",
						util.GetName(*banTarget), int64(banTime.Seconds()))
				} else {
					text = fmt.Sprintf("委派下来的工作已经做完了。%s 将会失落 %d 秒，希望他再次振作起来。",
						util.GetName(*banTarget), int64(banTime.Seconds()))
				}
			}
		}
	} else {
		text = "ban 谁呀，咋 ban 呀， 你到底会不会用啊:)"
	}

	message := tgbotapi.NewMessage(chatID, text)
	message.ReplyToMessageID = update.Message.MessageID
	util.SendMessage(bot, message)
}

// BanSomeone Use to ban someone, return true if success.
func BanSomeone(bot *tgbotapi.BotAPI, chatID int64, userID int, hard bool, duration time.Duration) bool {

	chatMember := tgbotapi.ChatMemberConfig{
		ChatID: chatID,
		UserID: userID,
	}

	if hard {
		return hardBan(bot, chatMember, duration)
	}
	return softBan(bot, chatMember, duration)
}

// BanSomeoneByUsername Use to ban someone by username, return true if success.
// Not Work
//func BanSomeoneByUsername(bot *tgbotapi.BotAPI, chatID int64, username string, hard bool, duration time.Duration) bool {
//
//    chatMember := tgbotapi.ChatMemberConfig{
//        ChatID:             chatID,
//        SuperGroupUsername: username,
//    }
//
//	if hard {
//		return hardBan(bot, chatMember, duration)
//	}
//	return softBan(bot, chatMember, duration)
//}

// BanMultiByUsername Use to ban by slice of username, return true if success.
// Not Work
//func BanMultiByUsername(bot *tgbotapi.BotAPI, chatID int64, username []string, hard bool, duration time.Duration) []string {
//
//	success := make([]string, 0)
//	for i := range username {
//		if BanSomeoneByUsername(bot, chatID, username[i], hard, duration) {
//			success = append(success, username[i])
//		}
//	}
//	return success
//}

// only allow text or media message
func softBan(bot *tgbotapi.BotAPI, chatMember tgbotapi.ChatMemberConfig, duration time.Duration) bool {

	flagFalse := false
	flagTrue := true

	restrictConfig := tgbotapi.RestrictChatMemberConfig{
		ChatMemberConfig:      chatMember,
		UntilDate:             time.Now().Add(duration).UTC().Unix(),
		CanSendMessages:       &flagTrue,
		CanSendMediaMessages:  &flagTrue,
		CanSendOtherMessages:  &flagFalse,
		CanAddWebPagePreviews: &flagFalse,
	}

	return ban(bot, restrictConfig)
}

// can't send anything
func hardBan(bot *tgbotapi.BotAPI, chatMember tgbotapi.ChatMemberConfig, duration time.Duration) bool {

	flag := false

	restrictConfig := tgbotapi.RestrictChatMemberConfig{
		ChatMemberConfig:      chatMember,
		UntilDate:             time.Now().Add(duration).UTC().Unix(),
		CanSendMessages:       &flag,
		CanSendMediaMessages:  nil,
		CanSendOtherMessages:  nil,
		CanAddWebPagePreviews: nil,
	}

	return ban(bot, restrictConfig)
}

func ban(bot *tgbotapi.BotAPI, restrictConfig tgbotapi.RestrictChatMemberConfig) bool {

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
