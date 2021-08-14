package base

import (
	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"csust-got/util"
	"fmt"
	"net/url"
	"strings"

	. "gopkg.in/tucnak/telebot.v3"

	"go.uber.org/zap"
)

type htmlMapper func(m *Message) string

func mapToHTML(mapper htmlMapper) func(*Message) {
	return func(m *Message) {
		util.SendReply(m.Chat, mapper(m), m, ModeHTML, NoPreview)
	}
}

type searchEngineFunc func(string) string

// searchEngine makes a 'search engine' by a searchEngine function.
// a searchEngine function get a string as "term", and returns a HTML formatted string message.
func searchEngine(engineFunc searchEngineFunc) htmlMapper {
	return func(m *Message) string {
		cmd := entities.FromMessage(m)
		if keyWord := cmd.ArgAllInOneFrom(0); keyWord != "" {
			return engineFunc(keyWord)
		}
		if rep := m.ReplyTo; rep != nil {
			if strings.Trim(rep.Text, " \t\n") != "" {
				return engineFunc(rep.Text)
			} else if rep.Sticker != nil {
				stickerSetName := rep.Sticker.SetName
				stickerSet, err := config.BotConfig.Bot.StickerSet(stickerSetName)
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
	Google   = mapToHTML(searchEngine(google))
	Bing     = mapToHTML(searchEngine(bing))
	Bilibili = mapToHTML(searchEngine(bilibili))
	Github   = mapToHTML(searchEngine(github))
	Repeat   = mapToHTML(searchEngine(repeat))
)
