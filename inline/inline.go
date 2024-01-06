package inline

import (
	"bytes"
	"csust-got/config"
	"csust-got/log"
	"csust-got/util"
	"csust-got/util/urlx"
	"errors"
	"regexp"
	"slices"
	"strings"

	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

//nolint:revive // It's too long.
var biliUrlRegex = `(?i)((?P<schema>https?://)?(?P<host>(?P<sub_domain>[\w\d\-]+\.)?(?P<main_domain>b23\.tv|bilibili\.com))(?P<path>(?:/[^\s\?#]*)*)?(?P<query>\?[^\s#]*)?(?P<hash>#[\S]*)?)`
var biliPatt = regexp.MustCompile(biliUrlRegex)

var (
	// ErrContextCanceled is returned when context is canceled
	ErrContextCanceled = errors.New("context canceled")
)

func init() {
	biliPatt.Longest()
}

// RegisterInlineHandler register inline mode handler
func RegisterInlineHandler(bot *tb.Bot, conf *config.Config) {
	bot.Handle(tb.OnQuery, handler(conf))
}

func handler(conf *config.Config) func(ctx tb.Context) error {
	return func(ctx tb.Context) error {
		q := ctx.Query()
		text := q.Text

		exs := urlx.ExtractStr(text)
		log.Debug("extracted urls", zap.String("origin", text), zap.Any("urls", exs))

		buf := bytes.NewBufferString("")
		err := writeAll(buf, exs)
		if err != nil {
			log.Error("write all error", zap.Error(err))
			return err
		}

		reText := buf.String()
		log.Debug("replaced text", zap.String("origin", text), zap.String("replaced", reText))
		reTextEscaped := util.EscapeTelegramReservedChars(reText)
		err = ctx.Answer(&tb.QueryResponse{
			Results: tb.Results{
				&tb.ArticleResult{
					ResultBase: tb.ResultBase{
						ParseMode: tb.ModeMarkdownV2,
					},
					Title:       "发送",
					Description: reText,
					Text:        reTextEscaped,
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

	if slices.Contains(biliDomains, strings.ToLower(u.Domain)) {
		err := writeBiliUrl(buf, u)
		return err
	}

	if slices.Contains(removeAllQueryDomains, strings.ToLower(u.Domain)) {
		return writeClearAllQuery(buf, u)
	}

	buf.WriteString(u.Text)
	return nil
}
