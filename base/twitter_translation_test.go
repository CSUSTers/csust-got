package base

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

type twitterRoundTrip func(*http.Request) (*http.Response, error)

func (f twitterRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func twitterResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func twitterPage(text string) string {
	return `<html><meta content='📑 翻译自英语<br><br>` + text + `<br><br>' property="og:description"></html>`
}

func TestTwitterCandidates(t *testing.T) {
	a := "https://fxtwitter.com/user/status/123/zh"
	b := "https://fxtwitter.com/i/status/456/zh"
	for _, msg := range []*tb.Message{
		{Text: "bad https://x.com.evil/user/status/123 then https://x.com/user/status/123?foo=1#bar and https://fxtwitter.com/user/status/123/zh/ then https://x.com/i/status/456."},
		{Caption: "😀 see https://x.com/user/status/123 and hidden", CaptionEntities: tb.Entities{{Type: tb.EntityTextLink, Offset: len(utf16.Encode([]rune("😀 see https://x.com/user/status/123 and "))), Length: 6, URL: "https://x.com/i/status/456?query=private"}}},
		{Text: "😀 link https://x.com/user/status/123", Entities: tb.Entities{{Type: tb.EntityURL, Offset: 8, Length: 29}}},
	} {
		got := twitterCandidates(msg)
		if msg.Text == "😀 link https://x.com/user/status/123" {
			require.Equal(t, []string{a}, got)
		} else {
			require.Equal(t, []string{a, b}, got)
		}
	}
	require.Empty(t, twitterCandidates(nil))
	require.Empty(t, twitterCandidates(&tb.Message{Text: "hello"}))
	require.Equal(t, []string{b}, twitterCandidates(&tb.Message{Caption: "😀read", CaptionEntities: tb.Entities{{Type: tb.EntityTextLink, Offset: 2, Length: 4, URL: "http://x.com/i/status/456"}}}))
	_, _, ok := twitterUTF16Range("😀link", 1, 4)
	require.False(t, ok)
	require.Empty(t, twitterCandidates(&tb.Message{Caption: "short", CaptionEntities: tb.Entities{{Type: tb.EntityTextLink, Offset: 99, Length: 1, URL: b}}}))
	require.Equal(t, []string{b, a}, twitterCandidates(&tb.Message{
		Text:     "😀hidden https://x.com/user/status/123",
		Entities: tb.Entities{{Type: tb.EntityTextLink, Offset: 2, Length: 6, URL: b}},
	}))
}

func TestTwitterCanonicalRejectsHostileInputs(t *testing.T) {
	bad := []string{
		"https://x.com.evil/u/status/1", "https://evilx.com/u/status/1", "https://x.com@localhost/u/status/1",
		"https://a@x.com/u/status/1", "https://x.com:443/u/status/1", "https://x.com./u/status/1",
		"https://x.com/u/status/1%2fother", "https://x.com/u/status/1%5cfoo", "https://x.com/u\\status/1",
		"https://x.com/u/status/no", "https://x.com/u/status/1/evil", "https://x.com/u/notstatus/1",
		"https://x.com/u-x/status/1", "https://x.com//u/status/1", "file://x.com/u/status/1",
	}
	var calls int
	translator := NewTwitterTranslator(twitterRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		return twitterResponse(200, twitterPage("ok")), nil
	}))
	translator.wait = func(context.Context) error { return nil }
	for _, raw := range bad {
		_, ok := canonicalTwitterURL(raw)
		require.False(t, ok, raw)
		require.Empty(t, twitterCandidates(&tb.Message{Text: raw}), raw)
	}
	var sends int
	replyBot, err := tb.NewBot(tb.Settings{Token: "test", Offline: true, Client: &http.Client{
		Transport: twitterRoundTrip(func(*http.Request) (*http.Response, error) {
			sends++
			return twitterResponse(200, `{"ok":true,"result":{"message_id":1}}`), nil
		}),
	}})
	require.NoError(t, err)
	for _, raw := range bad {
		for _, msg := range []*tb.Message{
			{Text: raw, Chat: &tb.Chat{ID: 123}},
			{Text: "link", Entities: tb.Entities{{Type: tb.EntityTextLink, Offset: 0, Length: 4, URL: raw}}, Chat: &tb.Chat{ID: 123}},
		} {
			translator.Translate(replyBot.NewContext(tb.Update{Message: msg}), replyBot)
		}
	}
	require.Zero(t, calls)
	require.Zero(t, sends)
}

func TestTwitterCanonicalIgnoresEncodedSeparatorsOutsidePath(t *testing.T) {
	for _, raw := range []string{
		"https://x.com/u/status/1?next=%2Fprivate&other=%5Csecret",
		"https://fxtwitter.com/u/status/1/zh#%2f%5c",
	} {
		canonical, ok := canonicalTwitterURL(raw)
		require.True(t, ok, raw)
		require.Equal(t, "https://fxtwitter.com/u/status/1/zh", canonical)
		require.Equal(t, []string{canonical}, twitterCandidates(&tb.Message{Text: raw}))
	}
}

func TestTwitterFetchBoundedAndIsolated(t *testing.T) {
	var urls []string
	translator := NewTwitterTranslator(twitterRoundTrip(func(r *http.Request) (*http.Response, error) {
		urls = append(urls, r.URL.String())
		require.Equal(t, "TelegramBot", r.UserAgent())
		require.Empty(t, r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/a/status/1/zh":
			return twitterResponse(500, "failed"), nil
		case "/b/status/2/zh":
			return twitterResponse(200, twitterPage("你死了 💀")), nil
		case "/c/status/3/zh":
			resp := twitterResponse(302, `<meta property="og:description" content="📑 翻译自英语<br>bad">`)
			resp.Header.Set("Location", "https://example.org/private")
			return resp, nil
		case "/d/status/4/zh":
			return twitterResponse(200, strings.Repeat("x", twitterBodyLimit+1)), nil
		}
		return twitterResponse(404, ""), nil
	}))
	translator.wait = func(context.Context) error { return nil }
	for i, want := range []string{"", "你死了 💀", "", ""} {
		url := "https://fxtwitter.com/" + string(rune('a'+i)) + "/status/" + string(rune('1'+i)) + "/zh"
		require.Equal(t, want, translator.fetch(t.Context(), url))
	}
	require.Equal(t, []string{"https://fxtwitter.com/a/status/1/zh", "https://fxtwitter.com/b/status/2/zh", "https://fxtwitter.com/c/status/3/zh", "https://fxtwitter.com/d/status/4/zh"}, urls)
}

type twitterBlockingBody struct {
	ctx    context.Context
	closed *atomic.Bool
}

func (b twitterBlockingBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b twitterBlockingBody) Close() error {
	b.closed.Store(true)
	return nil
}

func TestTwitterFetchHeaderAndBodyTimeout(t *testing.T) {
	for _, phase := range []string{"header", "body"} {
		t.Run(phase, func(t *testing.T) {
			var closed atomic.Bool
			var attempts atomic.Int32
			translator := NewTwitterTranslator(twitterRoundTrip(func(r *http.Request) (*http.Response, error) {
				attempts.Add(1)
				if phase == "header" {
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				return &http.Response{StatusCode: 200, Body: twitterBlockingBody{r.Context(), &closed}, Header: make(http.Header)}, nil
			}))
			translator.client.Timeout = 40 * time.Millisecond
			require.Empty(t, translator.fetch(t.Context(), "https://fxtwitter.com/u/status/1/zh"))
			require.Equal(t, int32(1), attempts.Load())
			if phase == "body" {
				require.True(t, closed.Load())
			}
		})
	}
}

func TestTwitterDescription(t *testing.T) {
	require.Equal(t, "你死了 💀", twitterDescription(`<meta property="og:description" content="📑 翻译自英语<br><br>你死了 💀<br><br>">`))
	require.Equal(t, "第一段\n第二段 &amp; &lt;3", twitterDescription(`<meta content='📑 翻译自英语&lt;br /&gt;第一段&lt;br&gt;第二段 &amp;amp; &amp;lt;3' property='og:description'>`))
	require.Equal(t, "甲 <T> & 乙\n下段 <T> &lt;T&gt;", twitterDescription(`<meta content='📑 翻译自英语<br />甲 &lt;T&gt; &amp; 乙&lt;br/&gt;下段 &lt;T&gt; &amp;lt;T&amp;gt;' property='og:description'>`))
	require.Equal(t, "字 <T>\n下段", twitterDescription(`<meta property="og:description" content="📑 翻译自英语&lt;br&gt;字 &lt;T&gt;&lt;br /&gt;下段">`))
	require.Equal(t, "raw <T>\n下一行", twitterDescription(`<meta property='og:description' content='📑 翻译自英语<br>raw &lt;T&gt;<b><br/></b>下一行'>`))
	require.Empty(t, twitterDescription(`<meta content="普通说明" property="og:description">`))
	require.Empty(t, twitterDescription(`<meta content="📑 翻译自英语<br><br>" property="og:description">`))
	require.Empty(t, twitterDescription(`<meta property="og:title" content="📑 翻译自英语<br>wrong">`))
	require.Empty(t, twitterDescription(`<metadata property="og:description" content="📑 翻译自英语<br>wrong">`))
}

func TestTwitterReplyUTF16Limit(t *testing.T) {
	text := twitterReplyText("https://fxtwitter.com/u/status/1/zh", strings.Repeat("💀", 3000))
	require.LessOrEqual(t, len(utf16.Encode([]rune(text))), 4000)
	require.True(t, strings.HasSuffix(text, "…（译文已截断）"))
	require.Empty(t, twitterReplyText(strings.Repeat("1", 3999), "translation"))
}

func TestTwitterGateWaitDoesNotConsumeDeadline(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var inFlight, peak, attempts atomic.Int32
	translator := NewTwitterTranslator(twitterRoundTrip(func(r *http.Request) (*http.Response, error) {
		current := inFlight.Add(1)
		if current > peak.Load() {
			peak.Store(current)
		}
		defer inFlight.Add(-1)
		attempts.Add(1)
		require.Greater(t, time.Until(func() time.Time { deadline, _ := r.Context().Deadline(); return deadline }()), 4*time.Second)
		return twitterResponse(200, twitterPage("ok")), nil
	}))
	translator.wait = func(context.Context) error { entered <- struct{}{}; <-release; return nil }
	var wg sync.WaitGroup
	for i := range 3 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			translator.fetch(t.Context(), "https://fxtwitter.com/u/status/1/zh")
		}(i)
	}
	for range 3 {
		select {
		case <-entered:
			release <- struct{}{}
		case <-time.After(time.Second):
			t.Fatal("next request did not acquire gate")
		}
	}
	wg.Wait()
	require.Equal(t, int32(1), peak.Load())
	require.Equal(t, int32(3), attempts.Load())
}
