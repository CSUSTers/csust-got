package base

import (
	"fmt"
	"net/url"
	"strings"

	"csust-got/config"
	"csust-got/entities"
	"csust-got/log"
	"csust-got/util"

	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
)

type htmlMapper func(m *Message) string

func mapToHTML(mapper htmlMapper) func(Context) error {
	return func(ctx Context) error {
		_, err := util.ReplyWithError(ctx, util.RawTgText(mapper(ctx.Message())), ModeHTML, NoPreview)
		return err
	}
}

type searchEngineFunc func(string) string

// searchEngine makes a 'search engine' by a searchEngine function.
// a searchEngine function get a string as "term", and returns an HTML formatted string message.
func searchEngine(engineFunc searchEngineFunc) htmlMapper {
	return func(m *Message) string {
		cmd := entities.FromMessage(m)
		if keyWord := cmd.ArgAllInOneFrom(0); keyWord != "" {
			return engineFunc(keyWord)
		}
		text := "亲亲，这个命令<em>必须</em>要带上一个参数的哦! 或者至少回复你想要搜索的内容哦!"
		rep := m.ReplyTo
		if rep == nil {
			return text
		}
		if strings.Trim(rep.Text, " \t\n") != "" {
			return engineFunc(rep.Text)
		}
		if rep.Sticker != nil {
			stickerSetName := rep.Sticker.SetName
			stickerSet, err := config.BotConfig.Bot.StickerSet(stickerSetName)
			if err == nil {
				return engineFunc(stickerSet.Title)
			}
			log.Error("searchEngine: GetStickerSet failed", zap.Error(err))
		}
		return text
	}
}

func google(cmd string) string {
	query := url.QueryEscape(cmd)
	website := "https://google.com/search?q=" + query
	return fmt.Sprintf("谷歌的搜索结果~: <a href=\"%s\">%s</a>", website, cmd)
}

func bing(cmd string) string {
	query := url.QueryEscape(cmd)
	website := "https://bing.com/search?q=" + query
	return fmt.Sprintf("必应的搜索结果~: <a href=\"%s\">%s</a>", website, cmd)
}

func bilibili(cmd string) string {
	query := url.QueryEscape(cmd)
	website := "https://search.bilibili.com/all?keyword=" + query
	return fmt.Sprintf("哔哩哔哩🍻~: <a href=\"%s\">%s</a>", website, cmd)
}

func github(cmd string) string {
	query := url.QueryEscape(cmd)
	website := "https://github.com/search?q=" + query
	return fmt.Sprintf("🐙🐱 Github: <a href=\"%s\">%s</a>", website, cmd)
}

func repeat(cmd string) string {
	return cmd
}

// Search Engine.
var (
	Google   = mapToHTML(searchEngine(google))
	Bing     = mapToHTML(searchEngine(bing))
	Bilibili = mapToHTML(searchEngine(bilibili))
	Github   = mapToHTML(searchEngine(github))
	Repeat   = mapToHTML(searchEngine(repeat))
)
