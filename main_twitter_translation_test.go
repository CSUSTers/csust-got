package main

import (
	agentv3 "csust-got/agent"
	"csust-got/base"
	"csust-got/config"
	"csust-got/log"
	"csust-got/orm"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	. "gopkg.in/telebot.v3"
)

type twitterFixtureTransport func(*http.Request) (*http.Response, error)

func (f twitterFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newTwitterDispatchFixture(t *testing.T, fetch http.RoundTripper, send func(*http.Request, map[string]string, int) bool) (*Bot, *[]map[string]string, *int) {
	t.Helper()
	previousConfig, previousRegex := config.BotConfig, regexHandlers
	previousTranslator, previousReplyBot := twitterTranslator, twitterReplyBot
	t.Cleanup(func() {
		config.BotConfig, regexHandlers = previousConfig, previousRegex
		twitterTranslator, twitterReplyBot = previousTranslator, previousReplyBot
	})
	redisServer := miniredis.RunT(t)
	config.BotConfig = config.NewBotConfig()
	log.InitLogger()
	config.BotConfig.RedisConfig.RedisAddr = redisServer.Addr()
	orm.InitRedis()
	regexHandlers = nil
	twitterTranslator = base.NewTwitterTranslator(fetch)
	var payloads []map[string]string
	var attempts int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		attempts++
		payloads = append(payloads, payload)
		if send != nil && !send(r, payload, attempts) {
			_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"failed"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	t.Cleanup(api.Close)
	bot, err := NewBot(Settings{Token: "test", URL: api.URL, Offline: true, Client: &http.Client{Timeout: time.Second}, Synchronous: true})
	require.NoError(t, err)
	bot.Me.Username = "test_bot"
	config.BotConfig.Bot = bot
	replyBot, err := NewBot(Settings{Token: "test", URL: api.URL, Offline: true, Client: &http.Client{Timeout: time.Second}})
	require.NoError(t, err)
	twitterReplyBot = replyBot
	bot.Use(loggerMiddleware, skipMiddleware, blockMiddleware, fakeBanMiddleware, rateMiddleware, noStickerMiddleware, shutdownMiddleware, byeWorldMiddleware, mcMiddleware)
	registerBaseHandler(bot)
	registerEventHandler(bot)
	return bot, &payloads, &attempts
}

func twitterDispatchMessage(text string) *Message {
	return &Message{ID: 41, ThreadID: 7, Text: text, Chat: &Chat{ID: 1234, Type: ChatPrivate}, Sender: &User{ID: 9}, Unixtime: time.Now().Unix()}
}

func TestTwitterDispatchMultipleAndFailureIsolation(t *testing.T) {
	for _, failure := range []string{"", "fetch", "send"} {
		t.Run(failure, func(t *testing.T) {
			var fetched []string
			bot, payloads, attempts := newTwitterDispatchFixture(t, twitterFixtureTransport(func(r *http.Request) (*http.Response, error) {
				fetched = append(fetched, r.URL.String())
				if failure == "fetch" && strings.Contains(r.URL.Path, "/a/") {
					return &http.Response{StatusCode: 502, Header: make(http.Header), Body: http.NoBody}, nil
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: httpNoCloseReader{strings.NewReader(`<meta property="og:description" content="📑 翻译自英语<br><br>你好 💀">`)}}, nil
			}), func(_ *http.Request, _ map[string]string, n int) bool { return failure != "send" || n != 1 })
			msg := twitterDispatchMessage("https://x.com/a/status/1?secret=not-sent https://fxtwitter.com/a/status/1/zh https://x.com/b/status/2")
			bot.ProcessUpdate(Update{Message: msg})
			require.Equal(t, []string{"https://fxtwitter.com/a/status/1/zh", "https://fxtwitter.com/b/status/2/zh"}, fetched)
			wantCount := 2
			if failure == "fetch" {
				wantCount = 1
			}
			require.Equal(t, wantCount, *attempts)
			for i, p := range *payloads {
				want := fetched[i]
				if failure == "fetch" {
					want = fetched[1]
				}
				require.Equal(t, want+"\n\n你好 💀", p["text"])
				require.Equal(t, "1234", p["chat_id"])
				require.Equal(t, "41", p["reply_to_message_id"])
				require.Equal(t, "7", p["message_thread_id"])
				require.Empty(t, p["parse_mode"])
				require.Empty(t, p["entities"])
				require.Equal(t, "true", p["disable_web_page_preview"])
			}
		})
	}
}

func TestTwitterDispatchSendTimeoutDoesNotBlockNextLink(t *testing.T) {
	bot, payloads, _ := newTwitterDispatchFixture(t, twitterFixtureTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: httpNoCloseReader{strings.NewReader(`<meta property='og:description' content='📑 翻译自英语<br>好'>`)}}, nil
	}), func(r *http.Request, _ map[string]string, attempt int) bool {
		if attempt == 1 {
			<-r.Context().Done()
		}
		return true
	})
	bot.ProcessUpdate(Update{Message: twitterDispatchMessage("https://x.com/a/status/1 https://x.com/b/status/2")})
	require.Len(t, *payloads, 2)
	require.Contains(t, (*payloads)[1]["text"], "https://fxtwitter.com/b/status/2/zh")
}

type httpNoCloseReader struct{ *strings.Reader }

func (r httpNoCloseReader) Close() error { return nil }

func TestTwitterDispatchMediaRoutingAndMiddleware(t *testing.T) {
	var requests int
	bot, payloads, _ := newTwitterDispatchFixture(t, twitterFixtureTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: httpNoCloseReader{strings.NewReader(`<meta content='📑 翻译自英语<br>翻译' property='og:description'>`)}}, nil
	}), nil)
	var regexCalls int
	regexHandlers = []struct {
		Regex *regexp.Regexp
		Func  func(Context) error
	}{{regexp.MustCompile("https|hidden"), func(Context) error { regexCalls++; return nil }}}
	for i, endpoint := range []string{OnAnimation, OnVideo, OnAudio, OnVoice, OnDocument} {
		msg := twitterDispatchMessage("")
		msg.ID += i
		msg.Caption = "😀hidden"
		msg.CaptionEntities = Entities{{Type: EntityTextLink, Offset: 2, Length: 6, URL: "https://x.com/u/status/1"}}
		switch endpoint {
		case OnAnimation:
			msg.Animation = &Animation{}
		case OnVideo:
			msg.Video = &Video{}
		case OnAudio:
			msg.Audio = &Audio{}
		case OnVoice:
			msg.Voice = &Voice{}
		case OnDocument:
			msg.Document = &Document{}
		}
		bot.ProcessUpdate(Update{Message: msg})
	}
	msg := twitterDispatchMessage("")
	msg.ID = 47
	msg.Caption = "https://x.com/u/status/1"
	require.NoError(t, bot.Trigger(OnMedia, bot.NewContext(Update{Message: msg})))
	require.Equal(t, 6, requests)
	require.Len(t, *payloads, 6)
	require.Zero(t, regexCalls)
	photo := twitterDispatchMessage("")
	photo.ID = 48
	photo.Photo = &Photo{}
	photo.Caption = "https://x.com/u/status/1"
	require.NoError(t, bot.Trigger(OnPhoto, bot.NewContext(Update{Message: photo})))
	require.Equal(t, 1, regexCalls)
	require.Equal(t, 7, requests)
	text := twitterDispatchMessage("/decode https://x.com/u/status/1")
	text.ID = 49
	text.Entities = Entities{{Type: EntityCommand, Offset: 0, Length: 7}}
	_ = bot.Trigger(OnText, bot.NewContext(Update{Message: text}))
	require.Equal(t, 7, requests)
	config.BotConfig.BlockListConfig.Enabled = true
	config.BotConfig.BlockListConfig.Chats = []int64{1234}
	blocked := twitterDispatchMessage("")
	blocked.ID = 50
	blocked.Document = &Document{}
	blocked.Caption = "https://x.com/u/status/1"
	require.NoError(t, bot.Trigger(OnDocument, bot.NewContext(Update{Message: blocked})))
	require.Equal(t, 7, requests)
}

func TestTwitterDispatchPreservesRegexError(t *testing.T) {
	var requests int
	bot, payloads, _ := newTwitterDispatchFixture(t, twitterFixtureTransport(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: httpNoCloseReader{strings.NewReader(`<meta property='og:description' content='📑 翻译自英语<br>好'>`)}}, nil
	}), nil)
	wantErr := http.ErrAbortHandler
	regexHandlers = []struct {
		Regex *regexp.Regexp
		Func  func(Context) error
	}{{regexp.MustCompile("https"), func(Context) error { return wantErr }}}
	msg := twitterDispatchMessage("https://x.com/u/status/1")
	require.ErrorIs(t, bot.Trigger(OnText, bot.NewContext(Update{Message: msg})), wantErr)
	require.Equal(t, 1, requests)
	require.Len(t, *payloads, 1)
}

func TestTwitterDispatchReplyToBotPreservesAgentBranch(t *testing.T) {
	const agentName = "uncompiled-reply-translation-fixture"
	previousLogger := zap.L()
	t.Cleanup(func() { zap.ReplaceGlobals(previousLogger) })
	core, logs := observer.New(zap.InfoLevel)
	var fetched []string
	bot, payloads, attempts := newTwitterDispatchFixture(t, twitterFixtureTransport(func(r *http.Request) (*http.Response, error) {
		require.Len(t, logs.FilterMessage("agent ignore by white list").All(), 1, "agent branch must run before translation fetch")
		fetched = append(fetched, r.URL.String())
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: httpNoCloseReader{strings.NewReader(`<meta property='og:description' content='📑 翻译自英语<br>你好'>`)}}, nil
	}), nil)
	zap.ReplaceGlobals(zap.New(core))
	config.BotConfig.AgentV3.Enable = true
	config.BotConfig.WhiteListConfig.Enabled = true
	config.BotConfig.WhiteListConfig.Chats = []int64{9876}
	agent := &config.AgentConfig{Name: agentName, Agent: &config.AgentOptions{Enable: true}, Trigger: []*config.AgentTrigger{{Reply: true}}}
	*config.BotConfig.Agents = config.AgentV3Configs{agent}
	require.True(t, agent.IsAgentV3Enabled())
	_, replyTrigger := agent.TriggerOnReply()
	require.True(t, replyTrigger)
	require.False(t, agentv3.HasCompiledAgent(agentName))
	msg := twitterDispatchMessage("https://x.com/u/status/1")
	msg.ReplyTo = &Message{ID: 40, Sender: &User{Username: "test_bot"}}
	bot.ProcessUpdate(Update{Message: msg})
	agentLogs := logs.FilterMessage("agent ignore by white list").All()
	require.Len(t, agentLogs, 1)
	require.Equal(t, msg.Chat.ID, agentLogs[0].ContextMap()["chat_id"])
	require.Equal(t, agentName, agentLogs[0].ContextMap()["agent"])
	require.Equal(t, []string{"https://fxtwitter.com/u/status/1/zh"}, fetched)
	require.Equal(t, 1, *attempts)
	require.Len(t, *payloads, 1)
	require.Equal(t, "https://fxtwitter.com/u/status/1/zh\n\n你好", (*payloads)[0]["text"])
	require.Equal(t, "41", (*payloads)[0]["reply_to_message_id"])
}
