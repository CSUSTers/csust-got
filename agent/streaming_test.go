//go:build !386 && !arm

package agentv3

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
		format *config.AgentOutputConfig
		want   time.Duration
	}{
		{
			name:   "empty defaults to one second",
			format: &config.AgentOutputConfig{},
			want:   time.Second,
		},
		{
			name: "invalid duration defaults to one second",
			format: &config.AgentOutputConfig{
				EditInterval: "not-a-duration",
			},
			want: time.Second,
		},
		{
			name: "custom duration is respected",
			format: &config.AgentOutputConfig{
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

func TestStreamProcessorReturnsEmptyResponseAfterClearThenError(t *testing.T) {
	bot, err := tb.NewBot(tb.Settings{Token: "test-token", Offline: true})
	require.NoError(t, err)
	sr, sw := schema.Pipe[*schema.Message](3)
	go func() {
		defer sw.Close()
		sw.Send(schema.AssistantMessage("partial", nil), nil)
		sw.Send(newClearStreamOutputMessage(), nil)
		sw.Send(nil, errRetryStubUpstream500)
	}()
	sp := &streamProcessor{
		ctx:            t.Context(),
		tbCtx:          &mockStreamingContext{bot: bot},
		reader:         sr,
		format:         &config.AgentOutputConfig{},
		editInterval:   time.Hour,
		placeholderMsg: &tb.Message{ID: 7, Chat: &tb.Chat{ID: 100}},
		done:           make(chan struct{}),
	}

	response, reasoning, sentMsg, err := sp.process(sp.placeholderMsg)

	assert.ErrorIs(t, err, errRetryStubUpstream500)
	assert.Empty(t, response)
	assert.Empty(t, reasoning)
	assert.Nil(t, sentMsg)
	assert.Nil(t, sp.placeholderMsg)
}

func TestStreamProcessorDeletesOldPlaceholderWhenPostClearContentErrorsBeforeEdit(t *testing.T) {
	bot, err := tb.NewBot(tb.Settings{Token: "test-token", Offline: true})
	require.NoError(t, err)
	placeholder := &tb.Message{ID: 7, Chat: &tb.Chat{ID: 100}}
	sp := &streamProcessor{
		ctx:            t.Context(),
		tbCtx:          &mockStreamingContext{bot: bot, chat: &tb.Chat{ID: 100}},
		format:         &config.AgentOutputConfig{},
		editInterval:   time.Hour,
		placeholderMsg: placeholder,
		done:           make(chan struct{}),
	}
	require.NotNil(t, sp.placeholderMsg)

	sr, sw := schema.Pipe[*schema.Message](3)
	go func() {
		defer sw.Close()
		sw.Send(newClearStreamOutputMessage(), nil)
		sw.Send(schema.AssistantMessage("new partial before next tick", nil), nil)
		sw.Send(nil, errRetryStubUpstream500)
	}()
	sp.reader = sr

	response, reasoning, sentMsg, err := sp.process(sp.placeholderMsg)

	assert.ErrorIs(t, err, errRetryStubUpstream500)
	assert.Empty(t, response)
	assert.Empty(t, reasoning)
	assert.Nil(t, sentMsg)
	assert.Nil(t, sp.placeholderMsg)
}

func TestFinalizeDeletesOldPlaceholderWhenPostClearEditFails(t *testing.T) {
	bot, err := tb.NewBot(tb.Settings{Token: "test-token", Offline: true})
	require.NoError(t, err)
	oldConfig := config.BotConfig
	config.BotConfig = config.NewBotConfig()
	config.BotConfig.Bot = bot
	log.InitLogger()
	t.Cleanup(func() { config.BotConfig = oldConfig })
	placeholder := &tb.Message{ID: 7, Chat: &tb.Chat{ID: 100}}
	sp := &streamProcessor{
		ctx:            t.Context(),
		tbCtx:          &mockStreamingContext{bot: bot, chat: &tb.Chat{ID: 100}},
		format:         &config.AgentOutputConfig{},
		placeholderMsg: placeholder,
		done:           make(chan struct{}),
	}
	sp.processChunk(newClearStreamOutputMessage())
	sp.processChunk(schema.AssistantMessage("new final before edit failure", nil))

	response, reasoning, sentMsg, err := sp.finalize()

	require.Error(t, err)
	assert.Empty(t, response)
	assert.Empty(t, reasoning)
	assert.Nil(t, sentMsg)
	assert.Nil(t, sp.placeholderMsg)
}

func TestFinalizeDeletesOldPlaceholderWhenPostClearRichSendFails(t *testing.T) {
	bot, err := tb.NewBot(tb.Settings{Token: "test-token", Offline: true})
	require.NoError(t, err)
	placeholder := &tb.Message{ID: 7, Chat: &tb.Chat{ID: 100}}
	sp := &streamProcessor{
		ctx:            t.Context(),
		tbCtx:          &mockStreamingContext{bot: bot, chat: &tb.Chat{ID: 100}},
		format:         &config.AgentOutputConfig{},
		richEnabled:    true,
		rawCaller:      &stubTelegramRawCaller{err: errTelegramRichRawTestFailure},
		placeholderMsg: placeholder,
		tc:             &TurnContext{},
		done:           make(chan struct{}),
	}
	authorizeRichMessageForFinal(t, sp.tc)
	sp.processChunk(newClearStreamOutputMessage())
	sp.processChunk(schema.AssistantMessage(mustTelegramRichEnvelope("# rich final"), nil))

	response, reasoning, sentMsg, err := sp.finalize()

	assert.ErrorIs(t, err, errTelegramRichRawTestFailure)
	assert.Empty(t, response)
	assert.Empty(t, reasoning)
	assert.Nil(t, sentMsg)
	assert.Nil(t, sp.placeholderMsg)
}

func TestUpdateMessageSuppressesPartialRichEnvelope(t *testing.T) {
	sp := &streamProcessor{
		format:      &config.AgentOutputConfig{},
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
		ctx:         t.Context(),
		tbCtx:       &mockStreamingContext{bot: bot},
		format:      &config.AgentOutputConfig{Format: "markdown", Payload: "markdown-block"},
		richEnabled: true,
		rawCaller:   raw,
		tc:          tc,
	}
	sp.processChunk(schema.AssistantMessage(mustTelegramRichEnvelope(rawMarkdown), nil))

	sp.updateMessage()

	assert.Empty(t, raw.method)
	assert.Nil(t, raw.payload)
	assert.Equal(t, int64(0), tc.lastEditAt.Load())
}

func TestUpdateMessageDoesNotSendRichForPlainMarkdown(t *testing.T) {
	bot, err := tb.NewBot(tb.Settings{Token: "test-token", Offline: true})
	require.NoError(t, err)
	raw := &stubTelegramRawCaller{body: []byte(`{"result":{"message_id":7,"chat":{"id":100}}}`)}
	tc := &TurnContext{}
	sp := &streamProcessor{
		ctx:         t.Context(),
		tbCtx:       &mockStreamingContext{bot: bot},
		format:      &config.AgentOutputConfig{Format: "markdown", Payload: "markdown-block"},
		richEnabled: true,
		rawCaller:   raw,
		tc:          tc,
	}
	sp.processChunk(schema.AssistantMessage("# Title\n\n**Body**", nil))

	sp.updateMessage()

	assert.Empty(t, raw.method)
	assert.Nil(t, raw.payload)
}

func TestFinalizeRichBypassesMarkdownBlockFormatting(t *testing.T) {
	const rawMarkdown = "# Title\n\n**Body**"
	const wantFallback = "Title\n\nBody"

	bot, err := tb.NewBot(tb.Settings{Token: "test-token", Offline: true})
	require.NoError(t, err)
	raw := &stubTelegramRawCaller{body: []byte(`{"result":{"message_id":8,"chat":{"id":100}}}`)}
	tc := &TurnContext{}
	authorizeRichMessageForFinal(t, tc)
	sp := &streamProcessor{
		ctx:            t.Context(),
		tbCtx:          &mockStreamingContext{bot: bot},
		format:         &config.AgentOutputConfig{Format: "markdown", Payload: "markdown-block"},
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

func TestFinalizePlainMarkdownDoesNotUseRichAPI(t *testing.T) {
	const rawMarkdown = "# Title\n\n**Body**"

	bot, err := tb.NewBot(tb.Settings{Token: "test-token", Offline: true})
	require.NoError(t, err)
	raw := &stubTelegramRawCaller{body: []byte(`{"result":{"message_id":8,"chat":{"id":100}}}`)}
	tc := &TurnContext{}
	authorizeRichMessageForFinal(t, tc)
	sp := &streamProcessor{
		ctx:         t.Context(),
		tbCtx:       &mockStreamingContext{bot: bot},
		format:      &config.AgentOutputConfig{Format: "markdown", Payload: "markdown-block"},
		richEnabled: true,
		rawCaller:   raw,
		tc:          tc,
	}
	sp.processChunk(schema.AssistantMessage(rawMarkdown, nil))

	response, reasoning, sentMsg, err := sp.finalize()

	require.NoError(t, err)
	assert.Equal(t, rawMarkdown, response)
	assert.Empty(t, reasoning)
	assert.Nil(t, sentMsg)
	assert.Empty(t, raw.method)
	assert.Nil(t, raw.payload)
	assert.True(t, tc.finalized.Load())
}

func TestFinalizeRichSendFailureDoesNotEditPlaceholderFallback(t *testing.T) {
	const rawMarkdown = "# Title\n\n**Body**"
	const wantFallback = "Title\n\nBody"

	bot, err := tb.NewBot(tb.Settings{Token: "test-token", Offline: true})
	require.NoError(t, err)
	raw := &stubTelegramRawCaller{err: errTelegramRichRawTestFailure}
	tc := &TurnContext{}
	authorizeRichMessageForFinal(t, tc)
	placeholder := &tb.Message{ID: 7, Chat: &tb.Chat{ID: 100}}
	sp := &streamProcessor{
		ctx:            t.Context(),
		tbCtx:          &mockStreamingContext{bot: bot},
		format:         &config.AgentOutputConfig{Format: "markdown", Payload: "markdown-block"},
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
		format:         &config.AgentOutputConfig{},
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
	format := &config.AgentOutputConfig{
		Format:  "markdown",
		Payload: "markdown-block",
	}
	text := mustTelegramRichEnvelope(rawMarkdown)

	msg, visibleText, err := nonStreamResponseWithCaller(raw, tbCtx, text, "", format, existingMsg, true, true)

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

func TestNonStreamResponsePlainMarkdownDoesNotUseRichAPI(t *testing.T) {
	const rawMarkdown = "# Title\n\n**Body**"

	bot, err := tb.NewBot(tb.Settings{Token: "test-token", Offline: true})
	require.NoError(t, err)
	oldConfig := config.BotConfig
	config.BotConfig = config.NewBotConfig()
	config.BotConfig.Bot = bot
	t.Cleanup(func() { config.BotConfig = oldConfig })

	raw := &stubTelegramRawCaller{
		body: []byte(`{"result":{"message_id":42,"chat":{"id":100}}}`),
	}
	tbCtx := &mockStreamingContext{bot: bot}
	existingMsg := &tb.Message{ID: 42, Chat: &tb.Chat{ID: 100}}
	format := &config.AgentOutputConfig{
		Format:  "markdown",
		Payload: "markdown-block",
	}

	_, visibleText, _ := nonStreamResponseWithCaller(raw, tbCtx, rawMarkdown, "", format, existingMsg, true, true)

	assert.Empty(t, raw.method)
	assert.Nil(t, raw.payload)
	assert.Equal(t, rawMarkdown, visibleText)
}

func TestNonStreamResponseRichSendFailureDoesNotEditPlaceholderFallback(t *testing.T) {
	const rawMarkdown = "# Title\n\n**Body**"
	const wantFallback = "Title\n\nBody"

	bot, err := tb.NewBot(tb.Settings{Token: "test-token", Offline: true})
	require.NoError(t, err)

	raw := &stubTelegramRawCaller{err: errTelegramRichRawTestFailure}
	tbCtx := &mockStreamingContext{bot: bot}
	existingMsg := &tb.Message{ID: 42, Chat: &tb.Chat{ID: 100}}
	format := &config.AgentOutputConfig{
		Format:  "markdown",
		Payload: "markdown-block",
	}

	msg, visibleText, err := nonStreamResponseWithCaller(raw, tbCtx, mustTelegramRichEnvelope(rawMarkdown), "", format, existingMsg, true, true)

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
	bot  *tb.Bot
	chat *tb.Chat
}

func (m *mockStreamingContext) Bot() *tb.Bot {
	return m.bot
}

func (m *mockStreamingContext) Message() *tb.Message {
	return nil
}

func (m *mockStreamingContext) Chat() *tb.Chat {
	if m.chat != nil {
		return m.chat
	}
	return &tb.Chat{ID: 100}
}

func authorizeRichMessageForFinal(t *testing.T, tc *TurnContext) {
	t.Helper()
	old := config.BotConfig
	config.BotConfig = &config.Config{AgentV3: &config.AgentV3Config{Enable: true}}
	t.Cleanup(func() { config.BotConfig = old })
	tc.Config = richAgentV3ChatConfig()
	tc.V3 = &AgentV3TurnState{}
	tc.markSkillLoaded("rich-message")
}
