package search

import (
	"csust-got/util"
	"fmt"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"net/url"
)

func Google(update tgbotapi.Update, bot *tgbotapi.BotAPI) {
	msg := update.Message

	cmd := msg.CommandArguments()
	query := url.QueryEscape(cmd)
	website := fmt.Sprintf("https://google.com/?q=%s", query)
	resultMedia := tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("<a href=\"%s\">%s</a>", website, cmd))
	resultMedia.ParseMode = tgbotapi.ModeHTML
	util.SendMessage(bot, resultMedia)
}
