package search

import (
	"csust-got/module"
	"csust-got/module/preds"
	"fmt"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"net/url"
)

type htmlMapper func(message *tgbotapi.Message) string

func mapToHTML(mapper htmlMapper) module.Module {
	return module.InteractModule(func(msg *tgbotapi.Message) tgbotapi.Chattable {
		resultMedia := tgbotapi.NewMessage(msg.Chat.ID, mapper(msg))
		resultMedia.ParseMode = tgbotapi.ModeHTML
		resultMedia.ReplyToMessageID = msg.MessageID
		return resultMedia
	})
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

var Google = module.WithPredicate(mapToHTML(google), preds.IsCommand("google"))
var Bing = module.WithPredicate(mapToHTML(bing), preds.IsCommand("bing"))
var Bilibili = module.WithPredicate(mapToHTML(bilibili), preds.IsCommand("bilibili"))
var Github = module.WithPredicate(mapToHTML(github), preds.IsCommand("github"))
