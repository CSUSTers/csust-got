package base

import (
	"context"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"

	"golang.org/x/time/rate"
	tb "gopkg.in/telebot.v3"
)

const twitterBodyLimit = 1 << 20

var (
	twitterBRPattern  = regexp.MustCompile(`(?i)<br\s*/?>`)
	twitterTagPattern = regexp.MustCompile(`(?i)</?[a-z][^>]*>`)
)

// TwitterTranslator serializes bounded requests to the fixed fxtwitter translation endpoint.
type TwitterTranslator struct {
	client *http.Client
	gate   chan struct{}
	wait   func(context.Context) error
}

// NewTwitterTranslator uses transport only for HTTP delivery, never to select a target URL.
func NewTwitterTranslator(transport http.RoundTripper) *TwitterTranslator {
	return &TwitterTranslator{
		client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		gate: make(chan struct{}, 1),
		wait: rate.NewLimiter(rate.Every(time.Second), 1).Wait,
	}
}

func (t *TwitterTranslator) fetch(ctx context.Context, canonical string) string {
	select {
	case t.gate <- struct{}{}:
	case <-ctx.Done():
		return ""
	}
	defer func() { <-t.gate }()
	if err := t.wait(ctx); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, canonical, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "TelegramBot")
	resp, err := t.client.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, twitterBodyLimit+1))
	if err != nil || len(data) > twitterBodyLimit {
		return ""
	}
	return twitterDescription(string(data))
}

func twitterDescription(page string) string {
	for i := 0; i < len(page); {
		start := strings.IndexByte(page[i:], '<')
		if start < 0 {
			break
		}
		start += i
		if len(page)-start < 6 || !strings.EqualFold(page[start:start+5], "<meta") || !strings.ContainsAny(page[start+5:start+6], " \t\n\r/>") {
			i = start + 1
			continue
		}
		end := start + 5
		var quote byte
	scanMeta:
		for end < len(page) {
			c := page[end]
			switch {
			case quote != 0:
				if c == quote {
					quote = 0
				}
			case c == '\'' || c == '"':
				quote = c
			case c == '>':
				break scanMeta
			}
			end++
		}
		if end == len(page) {
			break
		}
		i = end + 1
		attrs := twitterMetaAttrs(page[start+5 : end])
		if !strings.EqualFold(attrs["property"], "og:description") {
			continue
		}
		value := twitterBRPattern.ReplaceAllString(attrs["content"], "\n")
		value = twitterTagPattern.ReplaceAllString(value, "")
		value = html.UnescapeString(value)
		value = twitterBRPattern.ReplaceAllString(value, "\n")
		value = strings.TrimSpace(value)
		if !strings.HasPrefix(value, "📑 翻译自") {
			return ""
		}
		_, translation, found := strings.Cut(value, "\n")
		if !found {
			return ""
		}
		return strings.TrimSpace(translation)
	}
	return ""
}

func twitterMetaAttrs(tag string) map[string]string {
	attrs := make(map[string]string)
	for i := 0; i < len(tag); {
		for i < len(tag) && (tag[i] == ' ' || tag[i] == '\t' || tag[i] == '\n' || tag[i] == '/' || tag[i] == '\r') {
			i++
		}
		start := i
		for i < len(tag) && (tag[i] >= 'a' && tag[i] <= 'z' || tag[i] >= 'A' && tag[i] <= 'Z' || tag[i] == '-' || tag[i] == ':') {
			i++
		}
		if i == start {
			i++
			continue
		}
		key := strings.ToLower(tag[start:i])
		for i < len(tag) && (tag[i] == ' ' || tag[i] == '\t' || tag[i] == '\n') {
			i++
		}
		if i == len(tag) || tag[i] != '=' {
			continue
		}
		i++
		for i < len(tag) && (tag[i] == ' ' || tag[i] == '\t' || tag[i] == '\n') {
			i++
		}
		if i == len(tag) {
			break
		}
		quote := tag[i]
		if quote == '"' || quote == '\'' {
			i++
			start = i
			for i < len(tag) && tag[i] != quote {
				i++
			}
			attrs[key] = tag[start:i]
			if i < len(tag) {
				i++
			}
		} else {
			start = i
			for i < len(tag) && tag[i] != ' ' && tag[i] != '\t' && tag[i] != '\n' {
				i++
			}
			attrs[key] = tag[start:i]
		}
	}
	return attrs
}

func twitterReplyText(source, translation string) string {
	const marker = "…（译文已截断）"
	prefix := source + "\n\n"
	maxUnits := 4000 - len(utf16.Encode([]rune(prefix)))
	if maxUnits <= 0 {
		return ""
	}
	units := 0
	for _, r := range translation {
		cost := 1
		if r > 0xffff {
			cost = 2
		}
		if units+cost > maxUnits {
			markerUnits := len(utf16.Encode([]rune(marker)))
			if maxUnits < markerUnits {
				return ""
			}
			return prefix + twitterTruncate(translation, maxUnits-markerUnits) + marker
		}
		units += cost
	}
	return prefix + translation
}

func twitterTruncate(text string, max int) string {
	units := 0
	for i, r := range text {
		cost := 1
		if r > 0xffff {
			cost = 2
		}
		if units+cost > max {
			return text[:i]
		}
		units += cost
	}
	return text
}

// Translate replies with one plain-text translation per distinct status link.
func (t *TwitterTranslator) Translate(ctx tb.Context, sendBot *tb.Bot) {
	if ctx == nil || ctx.Message() == nil || ctx.Chat() == nil || sendBot == nil {
		return
	}
	for _, canonical := range twitterCandidates(ctx.Message()) {
		translation := t.fetch(context.Background(), canonical)
		if translation == "" {
			continue
		}
		reply := twitterReplyText(canonical, translation)
		if reply == "" {
			continue
		}
		_, _ = sendBot.Send(ctx.Chat(), reply, &tb.SendOptions{
			ReplyTo: ctx.Message(), ThreadID: ctx.Message().ThreadID,
			DisableWebPagePreview: true, ParseMode: tb.ModeDefault,
		})
	}
}
