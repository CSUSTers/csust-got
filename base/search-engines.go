package base

import (
	"csust-got/config"
	"csust-got/log"
	"csust-got/module"
	"csust-got/module/preds"
	"fmt"
	"net/url"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"go.uber.org/zap"
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

type searchEngineFunc func(string) string

// searchEngine makes a 'search engine' by a searchEngine function.
// a searchEngine function get a string as "term", and returns a HTML formatted string message.
func searchEngine(engineFunc searchEngineFunc) htmlMapper {
	return func(message *tgbotapi.Message) string {
		if cmd := message.CommandArguments(); cmd != "" {
			return engineFunc(cmd)
		}
		if rep := message.ReplyToMessage; rep != nil {
			if strings.Trim(rep.Text, " \t\n") != "" {
				return engineFunc(rep.Text)
			} else if rep.Sticker != nil {
				stickerSetName := rep.Sticker.SetName
				stickerSet, err := config.BotConfig.Bot.GetStickerSet(
					tgbotapi.GetStickerSetConfig{Name: stickerSetName})
				if err != nil {
					log.Error("searchEngine: GetStickerSet failed", zap.Error(err))
				} else {
					return engineFunc(stickerSet.Title)
				}
			}
		}
		return "亲亲，这个命令<em>必须</em>要带上一个参数的哦！或者至少回复你想要搜索的内容哦！"
	}
}

func wrap(engineFunc searchEngineFunc) module.Module {
	return mapToHTML(searchEngine(engineFunc))
}

func google(cmd string) string {
	query := url.QueryEscape(cmd)
	website := fmt.Sprintf("https://google.com/search?q=%s", query)
	return fmt.Sprintf("谷歌的搜索结果~：<a href=\"%s\">%s</a>", website, cmd)
}

func bing(cmd string) string {
	query := url.QueryEscape(cmd)
	website := fmt.Sprintf("https://bing.com/search?q=%s", query)
	return fmt.Sprintf("必应的搜索结果~：<a href=\"%s\">%s</a>", website, cmd)
}

func bilibili(cmd string) string {
	query := url.QueryEscape(cmd)
	website := fmt.Sprintf("https://search.bilibili.com/all?keyword=%s", query)
	return fmt.Sprintf("哔哩哔哩🍻~：<a href=\"%s\">%s</a>", website, cmd)
}

func github(cmd string) string {
	query := url.QueryEscape(cmd)
	website := fmt.Sprintf("https://github.com/search?q=%s", query)
	return fmt.Sprintf("🐙🐱 Github：<a href=\"%s\">%s</a>", website, cmd)
}

func repeat(cmd string) string {
	return cmd
}

// Search Engine
var (
	Google   = module.WithPredicate(wrap(google), preds.IsCommand("google"))
	Bing     = module.WithPredicate(wrap(bing), preds.IsCommand("bing"))
	Bilibili = module.WithPredicate(wrap(bilibili), preds.IsCommand("bilibili"))
	Github   = module.WithPredicate(wrap(github), preds.IsCommand("github"))
	Repeat   = module.WithPredicate(wrap(repeat), preds.IsCommand("recorder"))
)
