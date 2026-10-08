package agentv3

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"csust-got/config"
	"csust-got/log"
	"csust-got/util"

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
	rejectSameText  bool
	editError       string
	responses       []string
	texts           map[int]string
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
		if !placeholder && (method == "editMessageText" || method == "sendMessage") && len(d.responses) > 0 {
			body := d.responses[0]
			d.responses = d.responses[1:]
			if body != "" {
				_, _ = w.Write([]byte(body))
				return
			}
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
		text, _ := payload["text"].(string)
		if method == "editMessageText" && d.rejectSameText && d.texts[id] == text {
			_, _ = fmt.Fprintf(w, `{"ok":false,"error_code":400,"description":%q}`, tb.ErrMessageNotModified.Description)
			return
		}
		if method == "editMessageText" || method == "sendMessage" {
			if d.texts == nil {
				d.texts = map[int]string{}
			}
			d.texts[id] = text
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

func (d *deliveryTelegram) overwrite(id int, text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.texts == nil {
		d.texts = map[int]string{}
	}
	d.texts[id] = text
}

func (d *deliveryTelegram) currentText(id int) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.texts[id]
}

func isDeliveryPlaceholder(text any) bool {
	return text == "..." || text == `\.\.\.`
}

func (d *deliveryTelegram) script(bodies ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.responses = append(d.responses, bodies...)
}

func deliveryFloodBody(retryAfter int) string {
	return fmt.Sprintf(`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after %d","parameters":{"retry_after":%d}}`, retryAfter, retryAfter)
}

const deliveryParseErrorBody = `{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities: fixture"}`

func deliveryText(call deliveryTelegramCall) string {
	text, _ := call.payload["text"].(string)
	return text
}

func deliveryParseMode(call deliveryTelegramCall) string {
	mode, _ := call.payload["parse_mode"].(string)
	return mode
}

func deliveryMessageID(call deliveryTelegramCall) int {
	var id int
	if value, ok := call.payload["message_id"]; ok {
		_, _ = fmt.Sscan(fmt.Sprint(value), &id)
	}
	return id
}

func deliveryReplyTo(call deliveryTelegramCall) int {
	var id int
	if value, ok := call.payload["reply_to_message_id"]; ok {
		_, _ = fmt.Sscan(fmt.Sprint(value), &id)
	}
	return id
}

func newDeliveryStreamProcessor(t *testing.T, d *deliveryTelegram, format *config.AgentOutputConfig) *streamProcessor {
	t.Helper()
	return &streamProcessor{
		ctx:            t.Context(),
		tbCtx:          d.bot.NewContext(tb.Update{Message: sessionMessage(10, 7, 0, "input")}),
		format:         format,
		editInterval:   time.Second,
		placeholderMsg: &tb.Message{ID: 42, Chat: &tb.Chat{ID: -100}},
	}
}

func stubFloodSleep(t *testing.T) *[]time.Duration {
	t.Helper()
	var slept []time.Duration
	old := telegramFloodSleep
	telegramFloodSleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	t.Cleanup(func() { telegramFloodSleep = old })
	return &slept
}

func setupDeliveryConfig(t *testing.T) {
	t.Helper()
	old := config.BotConfig
	config.BotConfig = config.NewBotConfig()
	log.InitLogger()
	t.Cleanup(func() { config.BotConfig = old })
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

func TestStreamFinalEditRetriesOnceAfterFlood(t *testing.T) {
	setupDeliveryConfig(t)
	d := newDeliveryTelegram(t)
	d.script(deliveryFloodBody(2))
	slept := stubFloodSleep(t)
	sp := newDeliveryStreamProcessor(t, d, &config.AgentOutputConfig{})
	sp.processChunk(schema.AssistantMessage("final answer", nil))

	response, _, sent, err := sp.finalize()

	require.NoError(t, err)
	require.Equal(t, "final answer", response)
	require.Equal(t, []time.Duration{2 * time.Second}, *slept)
	calls := d.finalCalls()
	require.Len(t, calls, 2)
	require.Equal(t, "editMessageText", calls[0].method)
	require.Equal(t, "editMessageText", calls[1].method)
	require.NotNil(t, sp.deliveredMsg)
	require.Equal(t, 42, sp.deliveredMsg.ID)
	require.Equal(t, 42, sent.ID)
}

func TestStreamFinalEditGivesUpAfterRepeatedFlood(t *testing.T) {
	setupDeliveryConfig(t)
	d := newDeliveryTelegram(t)
	d.script(deliveryFloodBody(1), deliveryFloodBody(1))
	stubFloodSleep(t)
	sp := newDeliveryStreamProcessor(t, d, &config.AgentOutputConfig{})
	sp.processChunk(schema.AssistantMessage("final answer", nil))

	_, _, _, err := sp.finalize()

	require.Error(t, err)
	_, flooded := util.FloodRetryAfter(err)
	require.True(t, flooded)
	require.Len(t, d.finalCalls(), 2, "formatted edit is retried once after the flood window and never falls back to raw text on flood")
	require.Nil(t, sp.deliveredMsg)
}

func longDeliveryText(lines int) string {
	var b strings.Builder
	for i := range lines {
		fmt.Fprintf(&b, "line %03d: %s\n", i, strings.Repeat("x", 80))
	}
	return strings.TrimRight(b.String(), "\n")
}

func TestStreamFinalLongOutputIsSplitIntoOrderedMessages(t *testing.T) {
	setupDeliveryConfig(t)
	d := newDeliveryTelegram(t)
	sp := newDeliveryStreamProcessor(t, d, &config.AgentOutputConfig{Format: "markdown"})
	text := longDeliveryText(100)
	require.Greater(t, util.UTF16Len(text), 2*telegramMessageLimit)
	sp.processChunk(schema.AssistantMessage(text, nil))

	response, _, sent, err := sp.finalize()

	require.NoError(t, err)
	require.Equal(t, text, response)
	calls := d.finalCalls()
	require.Len(t, calls, 3)
	require.Equal(t, "editMessageText", calls[0].method)
	require.Equal(t, 42, deliveryMessageID(calls[0]))
	require.Equal(t, "sendMessage", calls[1].method)
	require.Equal(t, "sendMessage", calls[2].method)
	for _, call := range calls {
		require.LessOrEqual(t, util.UTF16Len(deliveryText(call)), telegramMessageLimit)
		require.Equal(t, "MarkdownV2", deliveryParseMode(call))
	}
	require.Len(t, sp.deliveredMsgs, 3)
	ids := []int{sp.deliveredMsgs[0].ID, sp.deliveredMsgs[1].ID, sp.deliveredMsgs[2].ID}
	require.Equal(t, 42, ids[0])
	require.Less(t, ids[1], ids[2])
	require.Equal(t, ids[0], deliveryReplyTo(calls[1]), "second chunk replies to the edited placeholder")
	require.Equal(t, ids[1], deliveryReplyTo(calls[2]), "third chunk replies to the second")
	require.Equal(t, ids[2], sent.ID, "the last chunk becomes the response message")
	require.Equal(t, ids[2], sp.deliveredMsg.ID)
	joined := make([]string, 0, len(calls))
	for _, call := range calls {
		joined = append(joined, deliveryText(call))
	}
	require.Equal(t, util.EscapeTgMDv2ReservedChars(text), strings.Join(joined, "\n"), "chunks are escaped individually and lose nothing")
}

func TestStreamFinalLongOutputStopsAtFirstFailedChunk(t *testing.T) {
	setupDeliveryConfig(t)
	d := newDeliveryTelegram(t)
	d.script("", "", `{"ok":false,"error_code":500,"description":"fixture send failure"}`, `{"ok":false,"error_code":500,"description":"fixture send failure"}`)
	sp := newDeliveryStreamProcessor(t, d, &config.AgentOutputConfig{})
	sp.processChunk(schema.AssistantMessage(longDeliveryText(100), nil))

	_, _, _, err := sp.finalize()

	require.Error(t, err)
	require.Nil(t, sp.deliveredMsg, "a partially delivered final is not proof of delivery")
	require.NotNil(t, sp.placeholderMsg, "the placeholder already carries the first chunk and stays")
}

func TestStreamFinalRichFailureFallsBackToPlainText(t *testing.T) {
	setupDeliveryConfig(t)
	d := newDeliveryTelegram(t)
	tc := &TurnContext{}
	authorizeRichMessageForFinal(t, tc)
	sp := newDeliveryStreamProcessor(t, d, &config.AgentOutputConfig{Format: "markdown"})
	sp.richEnabled = true
	sp.rawCaller = &stubTelegramRawCaller{err: errTelegramRichRawTestFailure}
	sp.tc = tc
	sp.processChunk(schema.AssistantMessage(mustTelegramRichEnvelope("# Title\n\n**Body**"), nil))

	response, reasoning, sent, err := sp.finalize()

	require.NoError(t, err)
	require.Equal(t, "Title\n\nBody", response)
	require.Empty(t, reasoning)
	require.Equal(t, 42, sent.ID)
	calls := d.finalCalls()
	require.Len(t, calls, 1)
	require.Equal(t, "editMessageText", calls[0].method)
	require.Equal(t, "Title\n\nBody", deliveryText(calls[0]))
	require.True(t, tc.finalized.Load())
	require.NotNil(t, sp.deliveredMsg)
}

func TestNonStreamRichFailureFallsBackToPlainText(t *testing.T) {
	setupDeliveryConfig(t)
	d := newDeliveryTelegram(t)
	tbCtx := d.bot.NewContext(tb.Update{Message: sessionMessage(10, 7, 0, "input")})
	raw := &stubTelegramRawCaller{err: errTelegramRichRawTestFailure}
	placeholder := &tb.Message{ID: 42, Chat: &tb.Chat{ID: -100}}

	result, err := nonStreamResponseWithDelivery(t.Context(), raw, tbCtx, mustTelegramRichEnvelope("# Title\n\n**Body**"), "", &config.AgentOutputConfig{Format: "markdown"}, placeholder, true, true)

	require.NoError(t, err)
	require.Equal(t, "Title\n\nBody", result.response)
	require.Equal(t, telegramSendRichMessageMethod, raw.method)
	calls := d.finalCalls()
	require.Len(t, calls, 1)
	require.Equal(t, "editMessageText", calls[0].method)
	require.Equal(t, "Title\n\nBody", deliveryText(calls[0]))
	require.NotNil(t, result.delivered)
	require.Equal(t, 42, result.delivered.ID)
	require.Equal(t, 42, result.sent.ID)
}

func TestNonStreamLongOutputIsSplitIntoOrderedMessages(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("placeholder=%t", existing), func(t *testing.T) {
			setupDeliveryConfig(t)
			d := newDeliveryTelegram(t)
			tbCtx := d.bot.NewContext(tb.Update{Message: sessionMessage(10, 7, 0, "input")})
			var placeholder *tb.Message
			if existing {
				placeholder = &tb.Message{ID: 42, Chat: &tb.Chat{ID: -100}}
			}
			text := longDeliveryText(100)

			result, err := nonStreamResponseWithDelivery(t.Context(), d.bot, tbCtx, text, "", &config.AgentOutputConfig{}, placeholder, false, false)

			require.NoError(t, err)
			calls := d.finalCalls()
			require.Len(t, calls, 3)
			require.Len(t, result.deliveredAll, 3)
			if existing {
				require.Equal(t, "editMessageText", calls[0].method)
				require.Equal(t, 42, result.deliveredAll[0].ID)
			} else {
				require.Equal(t, "sendMessage", calls[0].method)
				require.Equal(t, 10, deliveryReplyTo(calls[0]), "first chunk replies to the user message")
			}
			for i := 1; i < 3; i++ {
				require.Equal(t, "sendMessage", calls[i].method)
				require.Equal(t, result.deliveredAll[i-1].ID, deliveryReplyTo(calls[i]))
				require.Less(t, result.deliveredAll[i-1].ID, result.deliveredAll[i].ID)
			}
			for _, call := range calls {
				require.LessOrEqual(t, util.UTF16Len(deliveryText(call)), telegramMessageLimit)
			}
			require.Equal(t, result.deliveredAll[2], result.delivered)
			require.Equal(t, result.deliveredAll[2], result.sent)
		})
	}
}

func TestNonStreamFinalSendRetriesOnceAfterFlood(t *testing.T) {
	setupDeliveryConfig(t)
	d := newDeliveryTelegram(t)
	d.script(deliveryFloodBody(90))
	slept := stubFloodSleep(t)
	tbCtx := d.bot.NewContext(tb.Update{Message: sessionMessage(10, 7, 0, "input")})

	result, err := nonStreamResponseWithDelivery(t.Context(), d.bot, tbCtx, "answer", "", &config.AgentOutputConfig{}, nil, false, false)

	require.NoError(t, err)
	require.Equal(t, []time.Duration{telegramFloodRetryCap}, *slept, "flood waits are capped")
	require.Len(t, d.finalCalls(), 2)
	require.NotNil(t, result.delivered)
}

func TestNonStreamFormattedFailureRetriesWithRawText(t *testing.T) {
	setupDeliveryConfig(t)
	d := newDeliveryTelegram(t)
	d.script(deliveryParseErrorBody)
	tbCtx := d.bot.NewContext(tb.Update{Message: sessionMessage(10, 7, 0, "input")})
	placeholder := &tb.Message{ID: 42, Chat: &tb.Chat{ID: -100}}

	result, err := nonStreamResponseWithDelivery(t.Context(), d.bot, tbCtx, "a.b", "", &config.AgentOutputConfig{Format: "markdown"}, placeholder, false, false)

	require.NoError(t, err)
	calls := d.finalCalls()
	require.Len(t, calls, 2)
	require.Equal(t, `a\.b`, deliveryText(calls[0]))
	require.Equal(t, "MarkdownV2", deliveryParseMode(calls[0]))
	require.Equal(t, "a.b", deliveryText(calls[1]), "the retry uses the raw text, not the escaped one")
	require.Empty(t, deliveryParseMode(calls[1]))
	require.Equal(t, 42, result.delivered.ID)
}

func TestUpdateMessageSkipsUnchangedText(t *testing.T) {
	setupDeliveryConfig(t)
	d := newDeliveryTelegram(t)
	sp := newDeliveryStreamProcessor(t, d, &config.AgentOutputConfig{})
	sp.processChunk(schema.AssistantMessage("hello", nil))

	sp.updateMessage()
	sp.updateMessage()
	require.Len(t, d.finalCalls(), 1, "identical text is not re-sent")

	sp.processChunk(schema.AssistantMessage(" world", nil))
	sp.updateMessage()
	require.Len(t, d.finalCalls(), 2)

	d.rejectSameText = true
	_, _, _, err := sp.finalize()
	require.NoError(t, err)
	calls := d.finalCalls()
	require.Len(t, calls, 3, "the final edit is always issued even when the preview already shows the final text")
	require.Equal(t, "editMessageText", calls[2].method)
	require.NotNil(t, sp.deliveredMsg, "message is not modified is a delivery proof")
	require.Equal(t, 42, sp.deliveredMsg.ID)
}

func TestStreamFinalEditIgnoresStaleDedupeAfterExternalOverwrite(t *testing.T) {
	setupDeliveryConfig(t)
	d := newDeliveryTelegram(t)
	d.rejectSameText = true
	sp := newDeliveryStreamProcessor(t, d, &config.AgentOutputConfig{})
	sp.processChunk(schema.AssistantMessage("final answer", nil))

	sp.updateMessage()
	require.Len(t, d.finalCalls(), 1)
	require.Equal(t, "final answer", d.currentText(42))
	sp.updateMessage()
	require.Len(t, d.finalCalls(), 1, "a periodic tick still skips identical text")

	d.overwrite(42, "正在搜索…")
	sp.processChunk(newClearStreamOutputMessage())
	require.Empty(t, sp.getResponse())
	sp.processChunk(schema.AssistantMessage("final answer", nil))

	_, _, sent, err := sp.finalize()
	require.NoError(t, err)
	calls := d.finalCalls()
	require.Len(t, calls, 2, "the forced final edit must not trust the dedupe cache")
	require.Equal(t, "editMessageText", calls[1].method)
	require.Equal(t, 42, deliveryMessageID(calls[1]))
	require.Equal(t, "final answer", deliveryText(calls[1]))
	require.Equal(t, "final answer", d.currentText(42))
	require.NotNil(t, sp.deliveredMsg)
	require.Equal(t, 42, sp.deliveredMsg.ID)
	require.Equal(t, 42, sent.ID)
}

func TestUpdateMessageResendsAfterPlaceholderOverwrittenHook(t *testing.T) {
	setupDeliveryConfig(t)
	d := newDeliveryTelegram(t)
	sp := newDeliveryStreamProcessor(t, d, &config.AgentOutputConfig{})
	sp.editInterval = 0
	sp.tc = &TurnContext{}
	sp.processChunk(schema.AssistantMessage("hello", nil))

	sp.updateMessage()
	sp.updateMessage()
	require.Len(t, d.finalCalls(), 1, "identical text is not re-sent while the placeholder still shows it")

	d.overwrite(42, "正在搜索…")
	sp.tc.MarkPlaceholderOverwritten()
	sp.updateMessage()
	require.Len(t, d.finalCalls(), 2, "an overwritten placeholder invalidates the dedupe cache")
	require.Equal(t, "hello", d.currentText(42))
	sp.updateMessage()
	require.Len(t, d.finalCalls(), 2)
}

func TestUpdateMessagePausesEditsUntilFloodWindowPasses(t *testing.T) {
	setupDeliveryConfig(t)
	d := newDeliveryTelegram(t)
	d.script(deliveryFloodBody(5))
	now := time.Unix(1_700_000_000, 0)
	sp := newDeliveryStreamProcessor(t, d, &config.AgentOutputConfig{})
	sp.now = func() time.Time { return now }
	sp.processChunk(schema.AssistantMessage("first", nil))

	sp.updateMessage()
	require.Len(t, d.finalCalls(), 1)
	require.Equal(t, 2*time.Second, sp.floodBackoff, "edit interval doubles after a flood error")

	sp.processChunk(schema.AssistantMessage(" second", nil))
	now = now.Add(3 * time.Second)
	sp.updateMessage()
	require.Len(t, d.finalCalls(), 1, "no edits before retry_after passes")

	now = now.Add(2 * time.Second)
	sp.updateMessage()
	require.Len(t, d.finalCalls(), 2, "edits resume once retry_after has passed")
	require.Zero(t, sp.floodBackoff, "backoff resets after a successful edit")
	require.True(t, sp.floodNotBefore.IsZero())
}

func TestUpdateMessageBackoffDoublesUpToCap(t *testing.T) {
	setupDeliveryConfig(t)
	d := newDeliveryTelegram(t)
	d.script(deliveryFloodBody(1), deliveryFloodBody(1), deliveryFloodBody(1), deliveryFloodBody(1))
	now := time.Unix(1_700_000_000, 0)
	sp := newDeliveryStreamProcessor(t, d, &config.AgentOutputConfig{})
	sp.now = func() time.Time { return now }
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, streamingEditBackoffCap}
	for i, backoff := range want {
		sp.processChunk(schema.AssistantMessage(fmt.Sprintf("chunk %d ", i), nil))
		sp.updateMessage()
		require.Len(t, d.finalCalls(), i+1)
		require.Equal(t, backoff, sp.floodBackoff)
		now = now.Add(streamingEditBackoffCap)
	}
}

func TestUpdateMessageRetriesRawTextOnceAndMarksEditTime(t *testing.T) {
	setupDeliveryConfig(t)
	d := newDeliveryTelegram(t)
	d.script(deliveryParseErrorBody, deliveryParseErrorBody)
	tc := &TurnContext{}
	sp := newDeliveryStreamProcessor(t, d, &config.AgentOutputConfig{Format: "markdown"})
	sp.tc = tc
	sp.processChunk(schema.AssistantMessage("a.b", nil))

	sp.updateMessage()

	calls := d.finalCalls()
	require.Len(t, calls, 2, "one formatted attempt and one raw retry")
	require.Equal(t, `a\.b`, deliveryText(calls[0]))
	require.Equal(t, "a.b", deliveryText(calls[1]))
	require.Empty(t, deliveryParseMode(calls[1]))
	require.NotZero(t, tc.lastEditAt.Load(), "a failed edit still advances the edit gate")
	require.False(t, tc.ShouldAllowEdit(time.Hour))
}

func TestUpdateMessageShowsTailWhenPreviewExceedsLimit(t *testing.T) {
	setupDeliveryConfig(t)
	d := newDeliveryTelegram(t)
	sp := newDeliveryStreamProcessor(t, d, &config.AgentOutputConfig{})
	text := longDeliveryText(100)
	sp.processChunk(schema.AssistantMessage(text, nil))

	sp.updateMessage()

	calls := d.finalCalls()
	require.Len(t, calls, 1)
	shown := deliveryText(calls[0])
	require.LessOrEqual(t, util.UTF16Len(shown), telegramMessageLimit)
	require.True(t, strings.HasPrefix(shown, "…"))
	require.True(t, strings.HasSuffix(text, strings.TrimPrefix(shown, "…")))
}
