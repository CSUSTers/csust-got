package agentv3

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"sync"
	"testing"

	"csust-got/config"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

type deliveryTelegramCall struct {
	method  string
	payload map[string]any
}

type deliveryTelegram struct {
	bot             *tb.Bot
	mu              sync.Mutex
	calls           []deliveryTelegramCall
	finalID         int
	failPlaceholder bool
	failFinal       bool
	failFormatted   bool
	invalidFinalID  bool
	editError       string
}

func newDeliveryTelegram(t *testing.T) *deliveryTelegram {
	t.Helper()
	d := &deliveryTelegram{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad fixture request", http.StatusBadRequest)
			return
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		method := path.Base(r.URL.Path)
		d.calls = append(d.calls, deliveryTelegramCall{method: method, payload: payload})
		if method == "sendChatAction" || method == "deleteMessage" {
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}
		placeholder := method == "sendMessage" && isDeliveryPlaceholder(payload["text"])
		if (placeholder && d.failPlaceholder) || (!placeholder && d.failFinal) || (!placeholder && d.failFormatted && payload["parse_mode"] != nil && payload["parse_mode"] != "") {
			_, _ = w.Write([]byte(`{"ok":false,"error_code":500,"description":"fixture delivery failure"}`))
			return
		}
		if method == "editMessageText" && d.editError != "" {
			_, _ = fmt.Sscan(fmt.Sprint(payload["message_id"]), &d.finalID)
			_, _ = fmt.Fprintf(w, `{"ok":false,"error_code":400,"description":%q}`, d.editError)
			return
		}
		id := 2000 + len(d.calls)
		if value, ok := payload["message_id"]; ok {
			_, _ = fmt.Sscan(fmt.Sprint(value), &id)
		}
		if !placeholder {
			if d.invalidFinalID {
				id = 0
			}
			d.finalID = id
		}
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"chat":{"id":-100},"from":{"id":99,"is_bot":true}}}`, id)
	}))
	t.Cleanup(server.Close)
	var err error
	d.bot, err = tb.NewBot(tb.Settings{Token: "delivery-fixture-token", Offline: true, URL: server.URL, Client: server.Client()})
	require.NoError(t, err)
	d.bot.Me = &tb.User{ID: 99, Username: "sessionbot", IsBot: true}
	config.BotConfig.Bot = d.bot
	return d
}

func (d *deliveryTelegram) finalCalls() []deliveryTelegramCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	var calls []deliveryTelegramCall
	for _, call := range d.calls {
		if call.method == "editMessageText" || call.method == telegramSendRichMessageMethod || (call.method == "sendMessage" && !isDeliveryPlaceholder(call.payload["text"])) {
			calls = append(calls, call)
		}
	}
	return calls
}

func isDeliveryPlaceholder(text any) bool {
	return text == "..." || text == `\.\.\.`
}

func TestNonStreamDeliveryHiddenThinkDoesNotSendOrEdit(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("placeholder=%t", existing), func(t *testing.T) {
			newAgentSessionFixture(t)
			d := newDeliveryTelegram(t)
			var placeholder *tb.Message
			if existing {
				placeholder = &tb.Message{ID: 42, Chat: &tb.Chat{ID: -100}}
			}
			useNative := false
			format := &config.AgentOutputConfig{UseNativeReasoning: &useNative}
			_, response, err := NonStreamResponse(d.bot.NewContext(tb.Update{Message: sessionMessage(10, 7, 0, "input")}), "<think>hidden</think>", "", format, placeholder, false, false)
			require.NoError(t, err)
			require.Equal(t, "<think>hidden</think>", response, "legacy response text stays raw")
			require.Empty(t, d.finalCalls(), "formatted-empty final must not call Telegram or fall back to raw hidden text")
		})
	}
}

func TestStreamDeliveryPartialEditDoesNotProveFinalDelivery(t *testing.T) {
	newAgentSessionFixture(t)
	d := newDeliveryTelegram(t)
	sp := &streamProcessor{
		ctx:            t.Context(),
		tbCtx:          d.bot.NewContext(tb.Update{Message: sessionMessage(10, 7, 0, "input")}),
		format:         &config.AgentOutputConfig{},
		placeholderMsg: &tb.Message{ID: 42, Chat: &tb.Chat{ID: -100}},
	}
	sp.processChunk(schema.AssistantMessage("partial", nil))
	sp.updateMessage()
	require.Len(t, d.finalCalls(), 1)
	require.Nil(t, sp.deliveredMsg, "a successful periodic edit is not final delivery")
	d.failFinal = true
	sp.processChunk(schema.AssistantMessage(" final", nil))
	_, _, _, err := sp.finalize()
	require.Error(t, err)
	require.Nil(t, sp.deliveredMsg, "final edit failure must not reuse the periodic edit's success")
}
