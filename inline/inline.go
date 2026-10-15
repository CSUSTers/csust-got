package inline

import (
	"bytes"
	"csust-got/config"
	"csust-got/log"
	"csust-got/util"
	"csust-got/util/urlx"
	"errors"
	"regexp"

	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

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
		translatedBuf := bytes.NewBufferString("")
		translated, err := writeAll(buf, translatedBuf, exs)
		if err != nil {
			log.Error("write all error", zap.Error(err))
			return err
		}

		reText := buf.String()

		if reText == "" {
			return ctx.Answer(&tb.QueryResponse{})
		}

		log.Debug("replaced text", zap.String("origin", text), zap.String("replaced", reText))
		results := tb.Results{newArticleResult("发送", reText)}
		if translated {
			translatedText := translatedBuf.String()
			log.Debug("translated text", zap.String("origin", text), zap.String("translated", translatedText))
			results = append(results, newArticleResult("发送（中文翻译）", translatedText))
		}
		err = ctx.Answer(&tb.QueryResponse{Results: results})
		if err != nil {
			log.Error("inline mode answer error", zap.Error(err))
		}
		return nil
	}
}

func newArticleResult(title, text string) *tb.ArticleResult {
	return &tb.ArticleResult{
		ResultBase: tb.ResultBase{
			ParseMode: tb.ModeMarkdownV2,
		},
		Title:       title,
		Description: text,
		Text:        util.EscapeTgMDv2ReservedChars(text),
	}
}

func writeAll(buf, translatedBuf *bytes.Buffer, exs []*urlx.Extra) (bool, error) {
	translated := false
	for _, e := range exs {
		if e.Type != urlx.TypeUrl {
			buf.WriteString(e.Text)
			translatedBuf.WriteString(e.Text)
			continue
		}
		ok, err := writeUrl(buf, translatedBuf, e)
		if err != nil {
			return false, err
		}
		translated = translated || ok
	}
	return translated, nil
}

func writeUrl(buf, translatedBuf *bytes.Buffer, e *urlx.Extra) (bool, error) {
	u := e.Url

	for _, cfg := range urlProcessConfigs {
		if !cfg.needProcess(e) {
			continue
		}
		start := buf.Len()
		if err := cfg.writeUrl(buf, u); err != nil {
			return false, err
		}
		if t, ok := cfg.(translatedUrlProcessor); ok {
			if translatedUrl, translated := t.translatedUrl(u); translated {
				translatedBuf.WriteString(translatedUrl)
				return true, nil
			}
		}
		translatedBuf.Write(buf.Bytes()[start:])
		return false, nil
	}

	buf.WriteString(u.Text)
	translatedBuf.WriteString(u.Text)
	return false, nil
}
