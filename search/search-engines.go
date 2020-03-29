package search

import (
	"csust-got/module"
	"csust-got/module/preds"
	"fmt"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"net/url"
	"strings"
)

type htmlMapper func(message *tgbotapi.Message) string

func filterEmptyCommand(interactFunc module.InteractFunc) module.Module {
	return module.InteractModule(func(message *tgbotapi.Message) tgbotapi.Chattable {
		if strings.Trim(message.CommandArguments(), " \t\n") == "" {
			msg := tgbotapi.NewMessage(message.Chat.ID, "亲亲，这个命令必须要带上一个参数的哦！")
			msg.ReplyToMessageID = message.MessageID
			return msg
		}
		return interactFunc(message)
	})
}

func mapToHTML(mapper htmlMapper) module.InteractFunc {
	return func(msg *tgbotapi.Message) tgbotapi.Chattable {
		resultMedia := tgbotapi.NewMessage(msg.Chat.ID, mapper(msg))
		resultMedia.ParseMode = tgbotapi.ModeHTML
		resultMedia.ReplyToMessageID = msg.MessageID
		return resultMedia
	}
}

func google(msg *tgbotapi.Message) string {
	cmd := msg.CommandArguments()
	query := url.QueryEscape(cmd)
	website := fmt.Sprintf("https://google.com/search?q=%s", query)
	return fmt.Sprintf("谷歌的搜索结果~：<a href=\"%s\">%s</a>", website, cmd)
}

func bing(msg *tgbotapi.Message) string {
	cmd := msg.CommandArguments()
	query := url.QueryEscape(cmd)
	website := fmt.Sprintf("https://bing.com/search?q=%s", query)
	return fmt.Sprintf("必应的搜索结果~：<a href=\"%s\">%s</a>", website, cmd)
}

func bilibili(msg *tgbotapi.Message) string {
	cmd := msg.CommandArguments()
	query := url.QueryEscape(cmd)
	website := fmt.Sprintf("https://search.bilibili.com/all?keyword=%s", query)
	return fmt.Sprintf("哔哩哔哩🍻~：<a href=\"%s\">%s</a>", website, cmd)
}

func github(msg *tgbotapi.Message) string {
	cmd := msg.CommandArguments()
	query := url.QueryEscape(cmd)
	website := fmt.Sprintf("https://github.com/search?q=%s", query)
	return fmt.Sprintf("🐙🐱 Github：<a href=\"%s\">%s</a>", website, cmd)
}

var Google = module.WithPredicate(filterEmptyCommand(mapToHTML(google)), preds.IsCommand("google"))
var Bing = module.WithPredicate(filterEmptyCommand(mapToHTML(bing)), preds.IsCommand("bing"))
var Bilibili = module.WithPredicate(filterEmptyCommand(mapToHTML(bilibili)), preds.IsCommand("bilibili"))
var Github = module.WithPredicate(filterEmptyCommand(mapToHTML(github)), preds.IsCommand("github"))
