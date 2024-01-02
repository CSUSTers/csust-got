package inline

import (
	"bytes"
	"csust-got/config"
	"csust-got/log"
	"csust-got/util/urlx"
	"net/url"
	"regexp"
	"slices"

	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

var biliDomains = []string{
	"b23.tv",
	"bilibili.com",
	"www.bilibili.com",
	"space.bilibili.com",
	"m.bilibili.com",
	"t.bilibili.com",
	"live.bilibili.com",
}

//nolint:revive // It's too long.
var biliUrlRegex = `(?i)((?P<schema>https?://)?(?P<host>(?P<sub_domain>[\w\d\-]+\.)?(?P<main_domain>b23\.tv|bilibili\.com))(?P<path>(?:/[^\s\?#]*)*)?(?P<query>\?[^\s#]*)?(?P<hash>#[\S]*)?)`
var biliPatt = regexp.MustCompile(biliUrlRegex)

func init() {
	biliPatt.Longest()
}

// RegisterInlineHandler regiester inline mode handler
func RegisterInlineHandler(bot *tb.Bot, conf *config.Config) {
	bot.Handle(tb.OnInlineResult, handler(conf))
}

func handler(conf *config.Config) func(ctx tb.Context) error {
	return func(ctx tb.Context) error {
		q := ctx.Query()
		text := q.Text

		exs := urlx.ExtractStr(text)
		buf := bytes.NewBufferString("")
		err := writeAll(buf, exs)
		if err != nil {
			log.Error("write all error", zap.Error(err))
			return err
		}

		reText := buf.String()
		err = ctx.Answer(&tb.QueryResponse{
			Results: tb.Results{
				&tb.ArticleResult{
					ResultBase: tb.ResultBase{
						ParseMode: tb.ModeMarkdownV2,
					},
					Title: "强化模式",
					Text:  reText,
				},
			},
		})
		if err != nil {
			log.Error("inline mode answer error", zap.Error(err))
		}
		return nil
	}
}

func writeAll(buf *bytes.Buffer, exs []*urlx.Extra) error {
	for _, e := range exs {
		if e.Type == urlx.TypeUrl {
			err := writeUrl(buf, e)
			if err != nil {
				return err
			}
		} else {
			buf.WriteString(e.Text)
		}
	}
	return nil
}

func writeUrl(buf *bytes.Buffer, e *urlx.Extra) error {
	u := e.Url
	if slices.Contains(biliDomains, u.Domain) && u.Query != "" {
		old, err := url.ParseQuery(u.Query[1:])
		if err != nil {
			log.Error("parse url query error", zap.Error(err))
			return err
		}

		u.Query = ""

		newMap := make(url.Values)
		retainFields := []string{"tab", "t", "p"}
		for _, k := range retainFields {
			if v, ok := old[k]; ok {
				newMap[k] = v
			}
		}
		newQuery := newMap.Encode()
		if newQuery != "" {
			u.Query = "?" + newQuery
		}
		buf.WriteString(u.StringByFields())
	} else {
		buf.WriteString(u.Text)
	}
	return nil
}
