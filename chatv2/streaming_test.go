//go:build !386 && !arm

package chatv2

import (
	"testing"
	"time"

	"csust-got/config"
	"csust-got/log"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestGetEditInterval(t *testing.T) {
	tests := []struct {
		name   string
		format *config.ChatOutputFormatConfig
		want   time.Duration
	}{
		{
			name:   "empty defaults to one second",
			format: &config.ChatOutputFormatConfig{},
			want:   time.Second,
		},
		{
			name: "invalid duration defaults to one second",
			format: &config.ChatOutputFormatConfig{
				EditInterval: "not-a-duration",
			},
			want: time.Second,
		},
		{
			name: "custom duration is respected",
			format: &config.ChatOutputFormatConfig{
				EditInterval: "2.5s",
			},
			want: 2500 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, getEditInterval(tt.format))
		})
	}
}

func TestUnquoteJSONString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain text unchanged",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "json quoted single char",
			input: `"用"`,
			want:  "用",
		},
		{
			name:  "json quoted with escaped newline",
			input: `"\n"`,
			want:  "\n",
		},
		{
			name:  "json quoted text with escapes",
			input: `"用户在对话\n中提到"`,
			want:  "用户在对话\n中提到",
		},
		{
			name:  "empty json string",
			input: `""`,
			want:  "",
		},
		{
			name:  "already unquoted text",
			input: "用户在对话中提到",
			want:  "用户在对话中提到",
		},
		{
			name:  "single quote char not treated as json",
			input: `"`,
			want:  `"`,
		},
		{
			name:  "invalid json string passthrough",
			input: `"unterminated`,
			want:  `"unterminated`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, unquoteJSONString(tt.input))
		})
	}
}

func TestProcessChunkClearsAccumulatedOutputOnToolBoundary(t *testing.T) {
	sp := &streamProcessor{}

	sp.processChunk(&schema.Message{Role: schema.Assistant, Content: "搜索今日金价。\n\n"})
	assert.Equal(t, "搜索今日金价。\n\n", sp.getResponse())

	sp.processChunk(&schema.Message{Extra: map[string]any{"csust-got:clear-stream-output": true}})
	assert.Empty(t, sp.getResponse())

	sp.processChunk(&schema.Message{Role: schema.Assistant, Content: "已获取到今日金价。"})
	assert.Equal(t, "已获取到今日金价。", sp.getResponse())
}

func TestUpdateMessageSuppressesPartialRichEnvelope(t *testing.T) {
	sp := &streamProcessor{
		format:      &config.ChatOutputFormatConfig{},
		richEnabled: false,
	}
	sp.processChunk(schema.AssistantMessage("<telegram_rich_message>{", nil))

	assert.True(t, shouldSuppressPartialRichEnvelope(sp.getResponse(), sp.richEnabled))
}

func TestUpdateMessageSuppressesCompleteRichEnvelopeUntilFinalize(t *testing.T) {
	const rawMarkdown = "# Title\n\n**Body**"

	bot, err := tb.NewBot(tb.Settings{Token: "test-token", Offline: true})
	require.NoError(t, err)
	raw := &stubTelegramRawCaller{body: []byte(`{"result":{"message_id":7,"chat":{"id":100}}}`)}
	tc := &TurnContext{}
	sp := &streamProcessor{
		ctx:            t.Context(),
		tbCtx:          &mockStreamingContext{bot: bot},
		format:         &config.ChatOutputFormatConfig{Format: "markdown", Payload: "markdown-block"},
		richEnabled:    true,
		rawCaller:      raw,
		placeholderMsg: &tb.Message{ID: 7, Chat: &tb.Chat{ID: 100}},
		tc:             tc,
	}
	sp.processChunk(schema.AssistantMessage(mustTelegramRichEnvelope(rawMarkdown), nil))

	sp.updateMessage()

	assert.Empty(t, raw.method)
	assert.Nil(t, raw.payload)
	assert.Equal(t, int64(0), tc.lastEditAt.Load())
}

func TestUpdateMessageSuppressesRichPlainMarkdownUntilFinalize(t *testing.T) {
	bot, err := tb.NewBot(tb.Settings{Token: "test-token", Offline: true})
	require.NoError(t, err)
	raw := &stubTelegramRawCaller{body: []byte(`{"result":{"message_id":7,"chat":{"id":100}}}`)}
	tc := &TurnContext{}
	sp := &streamProcessor{
		ctx:            t.Context(),
		tbCtx:          &mockStreamingContext{bot: bot},
		format:         &config.ChatOutputFormatConfig{Format: "markdown", Payload: "markdown-block"},
		richEnabled:    true,
		rawCaller:      raw,
		placeholderMsg: &tb.Message{ID: 7, Chat: &tb.Chat{ID: 100}},
		tc:             tc,
	}
	sp.processChunk(schema.AssistantMessage("# Title\n\n**Body**", nil))

	sp.updateMessage()

	assert.Empty(t, raw.method)
	assert.Nil(t, raw.payload)
	assert.Equal(t, int64(0), tc.lastEditAt.Load())
}

func TestFinalizeRichBypassesMarkdownBlockFormatting(t *testing.T) {
	const rawMarkdown = "# Title\n\n**Body**"
	const wantFallback = "Title\n\nBody"

	bot, err := tb.NewBot(tb.Settings{Token: "test-token", Offline: true})
	require.NoError(t, err)
	raw := &stubTelegramRawCaller{body: []byte(`{"result":{"message_id":8,"chat":{"id":100}}}`)}
	tc := &TurnContext{}
	sp := &streamProcessor{
		ctx:            t.Context(),
		tbCtx:          &mockStreamingContext{bot: bot},
		format:         &config.ChatOutputFormatConfig{Format: "markdown", Payload: "markdown-block"},
		richEnabled:    true,
		rawCaller:      raw,
		placeholderMsg: &tb.Message{ID: 7, Chat: &tb.Chat{ID: 100}},
		tc:             tc,
	}
	sp.processChunk(schema.AssistantMessage(mustTelegramRichEnvelope(rawMarkdown), nil))

	response, reasoning, sentMsg, err := sp.finalize()

	require.NoError(t, err)
	assert.Equal(t, wantFallback, response)
	assert.Empty(t, reasoning)
	require.NotNil(t, sentMsg)
	assert.Equal(t, 8, sentMsg.ID)
	assert.Equal(t, telegramSendRichMessageMethod, raw.method)
	require.IsType(t, telegramSendRichMessagePayload{}, raw.payload)
	payload := raw.payload.(telegramSendRichMessagePayload)
	assert.Equal(t, rawMarkdown, payload.RichMessage.Markdown)
	assert.NotContains(t, payload.RichMessage.Markdown, "```")
	assert.True(t, tc.finalized.Load())
}

func TestFinalizeRichPlainMarkdownBypassesMarkdownBlockFormatting(t *testing.T) {
	const rawMarkdown = "# Title\n\n**Body**"
	const wantFallback = "Title\n\nBody"

	bot, err := tb.NewBot(tb.Settings{Token: "test-token", Offline: true})
	require.NoError(t, err)
	raw := &stubTelegramRawCaller{body: []byte(`{"result":{"message_id":8,"chat":{"id":100}}}`)}
	tc := &TurnContext{}
	sp := &streamProcessor{
		ctx:            t.Context(),
		tbCtx:          &mockStreamingContext{bot: bot},
		format:         &config.ChatOutputFormatConfig{Format: "markdown", Payload: "markdown-block"},
		richEnabled:    true,
		rawCaller:      raw,
		placeholderMsg: &tb.Message{ID: 7, Chat: &tb.Chat{ID: 100}},
		tc:             tc,
	}
	sp.processChunk(schema.AssistantMessage(rawMarkdown, nil))

	response, reasoning, sentMsg, err := sp.finalize()

	require.NoError(t, err)
	assert.Equal(t, wantFallback, response)
	assert.Empty(t, reasoning)
	require.NotNil(t, sentMsg)
	assert.Equal(t, telegramSendRichMessageMethod, raw.method)
	require.IsType(t, telegramSendRichMessagePayload{}, raw.payload)
	payload := raw.payload.(telegramSendRichMessagePayload)
	assert.Equal(t, rawMarkdown, payload.RichMessage.Markdown)
	assert.NotContains(t, payload.RichMessage.Markdown, "```")
	assert.True(t, tc.finalized.Load())
}

func TestFinalizeRichSendFailureDoesNotEditPlaceholderFallback(t *testing.T) {
	const rawMarkdown = "# Title\n\n**Body**"
	const wantFallback = "Title\n\nBody"

	bot, err := tb.NewBot(tb.Settings{Token: "test-token", Offline: true})
	require.NoError(t, err)
	raw := &stubTelegramRawCaller{err: errTelegramRichRawTestFailure}
	tc := &TurnContext{}
	placeholder := &tb.Message{ID: 7, Chat: &tb.Chat{ID: 100}}
	sp := &streamProcessor{
		ctx:            t.Context(),
		tbCtx:          &mockStreamingContext{bot: bot},
		format:         &config.ChatOutputFormatConfig{Format: "markdown", Payload: "markdown-block"},
		richEnabled:    true,
		rawCaller:      raw,
		placeholderMsg: placeholder,
		tc:             tc,
	}
	sp.processChunk(schema.AssistantMessage(mustTelegramRichEnvelope(rawMarkdown), nil))

	response, reasoning, sentMsg, err := sp.finalize()

	require.ErrorIs(t, err, errTelegramRichRawTestFailure)
	assert.Equal(t, wantFallback, response)
	assert.Empty(t, reasoning)
	assert.Equal(t, placeholder, sentMsg)
	assert.Equal(t, telegramSendRichMessageMethod, raw.method)
	require.IsType(t, telegramSendRichMessagePayload{}, raw.payload)
	payload := raw.payload.(telegramSendRichMessagePayload)
	assert.Equal(t, rawMarkdown, payload.RichMessage.Markdown)
	assert.NotContains(t, payload.RichMessage.Markdown, "```")
	assert.Equal(t, int64(0), tc.lastEditAt.Load(), "must not edit the placeholder after rich send failure")
	assert.False(t, tc.finalized.Load())
}

func TestFinalizeReturnsErrorWhenFinalEditFails(t *testing.T) {
	bot, err := tb.NewBot(tb.Settings{Token: "test-token", Offline: true})
	require.NoError(t, err)
	oldConfig := config.BotConfig
	config.BotConfig = config.NewBotConfig()
	config.BotConfig.Bot = bot
	log.InitLogger()
	t.Cleanup(func() { config.BotConfig = oldConfig })

	tc := &TurnContext{}
	sp := &streamProcessor{
		ctx: t.Context(),
		tbCtx: &mockStreamingContext{
			bot: bot,
		},
		format:         &config.ChatOutputFormatConfig{},
		placeholderMsg: &tb.Message{ID: 1, Chat: &tb.Chat{ID: 100}},
		tc:             tc,
	}
	sp.processChunk(schema.AssistantMessage("最终答案", nil))

	response, reasoning, sentMsg, err := sp.finalize()

	require.Error(t, err)
	assert.Equal(t, "最终答案", response)
	assert.Empty(t, reasoning)
	assert.Equal(t, sp.placeholderMsg, sentMsg)
	assert.False(t, tc.finalized.Load())
}

func TestNonStreamResponseRichBypassesMarkdownBlockFormatting(t *testing.T) {
	const rawMarkdown = "# Title\n\n**Body**"
	const wantFallback = "Title\n\nBody"

	bot, err := tb.NewBot(tb.Settings{Token: "test-token", Offline: true})
	require.NoError(t, err)

	raw := &stubTelegramRawCaller{
		body: []byte(`{"result":{"message_id":42,"chat":{"id":100}}}`),
	}
	tbCtx := &mockStreamingContext{bot: bot}
	existingMsg := &tb.Message{ID: 42, Chat: &tb.Chat{ID: 100}}
	format := &config.ChatOutputFormatConfig{
		Format:  "markdown",
		Payload: "markdown-block",
	}
	text := mustTelegramRichEnvelope(rawMarkdown)

	msg, visibleText, err := nonStreamResponseWithCaller(raw, tbCtx, text, "", format, existingMsg, true)

	require.NoError(t, err)
	assert.Equal(t, telegramSendRichMessageMethod, raw.method,
		"rich send API must be called, not a normal text edit")
	require.IsType(t, telegramSendRichMessagePayload{}, raw.payload,
		"payload must be a rich send payload, not a plain text edit payload")
	payload := raw.payload.(telegramSendRichMessagePayload)
	assert.Equal(t, rawMarkdown, payload.RichMessage.Markdown,
		"raw markdown must pass through unmodified - no ```markdown fences, no escaping")
	assert.NotContains(t, payload.RichMessage.Markdown, "```",
		"markdown-block fencing must not be applied to rich messages")
	assert.Equal(t, wantFallback, visibleText,
		"visible text must be the derived plain-text fallback, not fenced markdown")
	assert.Equal(t, 42, msg.ID)
}

func TestNonStreamResponseRichPlainMarkdownBypassesMarkdownBlockFormatting(t *testing.T) {
	const rawMarkdown = "# Title\n\n**Body**"
	const wantFallback = "Title\n\nBody"

	bot, err := tb.NewBot(tb.Settings{Token: "test-token", Offline: true})
	require.NoError(t, err)

	raw := &stubTelegramRawCaller{
		body: []byte(`{"result":{"message_id":42,"chat":{"id":100}}}`),
	}
	tbCtx := &mockStreamingContext{bot: bot}
	existingMsg := &tb.Message{ID: 42, Chat: &tb.Chat{ID: 100}}
	format := &config.ChatOutputFormatConfig{
		Format:  "markdown",
		Payload: "markdown-block",
	}

	msg, visibleText, err := nonStreamResponseWithCaller(raw, tbCtx, rawMarkdown, "", format, existingMsg, true)

	require.NoError(t, err)
	assert.Equal(t, telegramSendRichMessageMethod, raw.method)
	require.IsType(t, telegramSendRichMessagePayload{}, raw.payload)
	payload := raw.payload.(telegramSendRichMessagePayload)
	assert.Equal(t, rawMarkdown, payload.RichMessage.Markdown)
	assert.NotContains(t, payload.RichMessage.Markdown, "```")
	assert.Equal(t, wantFallback, visibleText)
	assert.Equal(t, 42, msg.ID)
}

func TestNonStreamResponseRichSendFailureDoesNotEditPlaceholderFallback(t *testing.T) {
	const rawMarkdown = "# Title\n\n**Body**"
	const wantFallback = "Title\n\nBody"

	bot, err := tb.NewBot(tb.Settings{Token: "test-token", Offline: true})
	require.NoError(t, err)

	raw := &stubTelegramRawCaller{err: errTelegramRichRawTestFailure}
	tbCtx := &mockStreamingContext{bot: bot}
	existingMsg := &tb.Message{ID: 42, Chat: &tb.Chat{ID: 100}}
	format := &config.ChatOutputFormatConfig{
		Format:  "markdown",
		Payload: "markdown-block",
	}

	msg, visibleText, err := nonStreamResponseWithCaller(raw, tbCtx, mustTelegramRichEnvelope(rawMarkdown), "", format, existingMsg, true)

	require.ErrorIs(t, err, errTelegramRichRawTestFailure)
	assert.Equal(t, existingMsg, msg)
	assert.Equal(t, wantFallback, visibleText)
	assert.Equal(t, telegramSendRichMessageMethod, raw.method)
	require.IsType(t, telegramSendRichMessagePayload{}, raw.payload)
	payload := raw.payload.(telegramSendRichMessagePayload)
	assert.Equal(t, rawMarkdown, payload.RichMessage.Markdown)
	assert.NotContains(t, payload.RichMessage.Markdown, "```")
}

type mockStreamingContext struct {
	tb.Context
	bot *tb.Bot
}

func (m *mockStreamingContext) Bot() *tb.Bot {
	return m.bot
}

func (m *mockStreamingContext) Message() *tb.Message {
	return nil
}
