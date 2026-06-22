//go:build !386 && !arm

package chatv2

import (
	"errors"
	"reflect"
	"testing"

	"csust-got/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errTelegramRichRawTestFailure = errors.New("raw failed")

func TestSplitOutputWithReasonMatchesExistingThinkBehavior(t *testing.T) {
	useNative := true
	noNative := false

	tests := []struct {
		name         string
		text         string
		nativeReason string
		format       *config.ChatOutputFormatConfig
		want         outputParts
	}{
		{
			name:         "native reasoning uses protocol field",
			text:         "answer",
			nativeReason: "native reason",
			format:       &config.ChatOutputFormatConfig{UseNativeReasoning: &useNative},
			want:         outputParts{reason: "native reason", payload: "answer"},
		},
		{
			name:   "think tag is split when native reasoning disabled",
			text:   "<think>reason</think>answer",
			format: &config.ChatOutputFormatConfig{UseNativeReasoning: &noNative},
			want:   outputParts{reason: "reason", payload: "answer"},
		},
		{
			name:   "plain payload has no reason",
			text:   "answer",
			format: &config.ChatOutputFormatConfig{UseNativeReasoning: &noNative},
			want:   outputParts{payload: "answer"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, splitOutputWithReason(tt.text, tt.nativeReason, tt.format))
		})
	}
}

func TestParseTelegramRichMessageEnvelope(t *testing.T) {
	t.Run("raw markdown envelope", func(t *testing.T) {
		input := mustTelegramRichEnvelope("# Title\n\n**Body**")

		parsed, ok := parseTelegramRichMessageEnvelope(input)

		require.True(t, ok)
		require.NoError(t, parsed.Err)
		assert.Equal(t, "# Title\n\n**Body**", parsed.RichMessage.Markdown)
		assert.Equal(t, "Title\n\nBody", parsed.FallbackText)
	})

	t.Run("ignores text outside envelope", func(t *testing.T) {
		input := "before " + mustTelegramRichEnvelope("**Title**") + " after"

		parsed, ok := parseTelegramRichMessageEnvelope(input)

		require.True(t, ok)
		require.NoError(t, parsed.Err)
		assert.Equal(t, "**Title**", parsed.RichMessage.Markdown)
		assert.Equal(t, "Title", parsed.FallbackText)
	})

	t.Run("unclosed envelope treats rest as current markdown", func(t *testing.T) {
		parsed, ok := parseTelegramRichMessageEnvelope("prefix " + telegramRichEnvelopeStart + "- **item**")

		require.True(t, ok)
		require.NoError(t, parsed.Err)
		assert.Equal(t, "- **item**", parsed.RichMessage.Markdown)
		assert.Equal(t, "item", parsed.FallbackText)
	})

	t.Run("normal text is not envelope", func(t *testing.T) {
		parsed, ok := parseTelegramRichMessageEnvelope("normal text")
		assert.False(t, ok)
		assert.Empty(t, parsed)
	})

	t.Run("empty inner uses safe fallback", func(t *testing.T) {
		parsed, ok := parseTelegramRichMessageEnvelope(mustTelegramRichEnvelope("  "))

		require.True(t, ok)
		assert.ErrorIs(t, parsed.Err, errTelegramRichMissingContent)
		assert.Equal(t, telegramRichInvalidFallbackText, parsed.FallbackText)
	})

	t.Run("fallback removes common rich markdown markers", func(t *testing.T) {
		parsed, ok := parseTelegramRichMessageEnvelope(mustTelegramRichEnvelope("# Head\n- [link](https://example.com)\n> `quote`"))

		require.True(t, ok)
		require.NoError(t, parsed.Err)
		assert.Equal(t, "Head\nlink\nquote", parsed.FallbackText)
	})
}

func TestShouldSuppressPartialRichEnvelope(t *testing.T) {
	assert.True(t, shouldSuppressPartialRichEnvelope("<telegram_rich_m", false))
	assert.True(t, shouldSuppressPartialRichEnvelope("<telegram_rich_m", true))
	assert.True(t, shouldSuppressPartialRichEnvelope(telegramRichEnvelopeStart+"# Title", true))
	assert.True(t, shouldSuppressPartialRichEnvelope(mustTelegramRichEnvelope("**visible**"), true))
	assert.False(t, shouldSuppressPartialRichEnvelope("ordinary text", true))
}

func TestSendTelegramRichMessageUsesRawMethodAndPayload(t *testing.T) {
	raw := &stubTelegramRawCaller{body: []byte(`{"result":{"message_id":123,"chat":{"id":456}}}`)}

	message, err := sendTelegramRichMessage(raw, 456, 99, inputRichMessage{Markdown: "**hello**"})

	require.NoError(t, err)
	assert.Equal(t, 123, message.ID)
	assert.Equal(t, telegramSendRichMessageMethod, raw.method)
	require.IsType(t, telegramSendRichMessagePayload{}, raw.payload)
	payload := raw.payload.(telegramSendRichMessagePayload)
	assert.Equal(t, int64(456), payload.ChatID)
	assert.Equal(t, "**hello**", payload.RichMessage.Markdown)
	require.NotNil(t, payload.ReplyParameters)
	assert.Equal(t, 99, payload.ReplyParameters.MessageID)
}

func TestEditTelegramRichMessageUsesEditMessageTextWithRichMessage(t *testing.T) {
	raw := &stubTelegramRawCaller{body: []byte(`{"result":{"message_id":321,"chat":{"id":654}}}`)}

	message, err := editTelegramRichMessage(raw, 654, 321, inputRichMessage{Markdown: "# hello"})

	require.NoError(t, err)
	assert.Equal(t, 321, message.ID)
	assert.Equal(t, telegramEditMessageTextMethod, raw.method)
	require.IsType(t, telegramEditRichMessagePayload{}, raw.payload)
	payload := raw.payload.(telegramEditRichMessagePayload)
	assert.Equal(t, int64(654), payload.ChatID)
	assert.Equal(t, 321, payload.MessageID)
	assert.Equal(t, "# hello", payload.RichMessage.Markdown)
}

func TestTelegramRichRawFailureIsReturned(t *testing.T) {
	raw := &stubTelegramRawCaller{err: errTelegramRichRawTestFailure}

	message, err := sendTelegramRichMessage(raw, 1, 0, inputRichMessage{Markdown: "hello"})

	assert.ErrorIs(t, err, errTelegramRichRawTestFailure)
	assert.Nil(t, message)
}

func TestResolveTelegramRichDelivery(t *testing.T) {
	cfg := &config.ChatOutputFormatConfig{}
	setConfigField(t, cfg, "UseNativeReasoning", false)

	t.Run("plain text stays visible for legacy formatting", func(t *testing.T) {
		text := "<think>reason</think>plain text"
		delivery := resolveTelegramRichDelivery(text, "", cfg, true)
		assert.Equal(t, text, delivery.VisibleText)
		assert.False(t, delivery.ShouldSendRich)
		assert.Empty(t, delivery.RichMessage)
		assert.NoError(t, delivery.Err)
	})

	t.Run("gate disabled returns derived fallback", func(t *testing.T) {
		text := mustTelegramRichEnvelope("*hello*")

		delivery := resolveTelegramRichDelivery(text, "", cfg, false)

		assert.Equal(t, "hello", delivery.VisibleText)
		assert.False(t, delivery.ShouldSendRich)
		assert.NoError(t, delivery.Err)
	})

	t.Run("gate enabled keeps raw markdown and derived fallback", func(t *testing.T) {
		text := mustTelegramRichEnvelope("# hello")

		delivery := resolveTelegramRichDelivery(text, "", cfg, true)

		assert.Equal(t, "hello", delivery.VisibleText)
		assert.True(t, delivery.ShouldSendRich)
		assert.Equal(t, "# hello", delivery.RichMessage.Markdown)
	})

	t.Run("surrounding prose is ignored for valid envelope", func(t *testing.T) {
		text := "Here is it:\n" + mustTelegramRichEnvelope("**hello**") + "\nthanks"

		delivery := resolveTelegramRichDelivery(text, "", cfg, true)

		assert.NoError(t, delivery.Err)
		assert.True(t, delivery.ShouldSendRich)
		assert.Equal(t, "**hello**", delivery.RichMessage.Markdown)
		assert.Equal(t, "hello", delivery.VisibleText)
	})

	t.Run("unclosed envelope uses current inner markdown", func(t *testing.T) {
		delivery := resolveTelegramRichDelivery("prefix "+telegramRichEnvelopeStart+"## heading", "", cfg, true)

		assert.NoError(t, delivery.Err)
		assert.True(t, delivery.ShouldSendRich)
		assert.Equal(t, "## heading", delivery.RichMessage.Markdown)
		assert.Equal(t, "heading", delivery.VisibleText)
	})

	t.Run("empty envelope uses safe fallback", func(t *testing.T) {
		delivery := resolveTelegramRichDelivery(mustTelegramRichEnvelope(""), "", cfg, true)

		assert.ErrorIs(t, delivery.Err, errTelegramRichMissingContent)
		assert.False(t, delivery.ShouldSendRich)
		assert.Equal(t, telegramRichInvalidFallbackText, delivery.VisibleText)
	})

	t.Run("resolver parses payload after splitting reasoning", func(t *testing.T) {
		text := "<think>reason</think>" + mustTelegramRichEnvelope("*hello*")

		delivery := resolveTelegramRichDelivery(text, "", cfg, true)

		assert.True(t, delivery.ShouldSendRich)
		assert.Equal(t, "*hello*", delivery.RichMessage.Markdown)
		assert.Equal(t, "hello", delivery.VisibleText)
	})

	t.Run("rich envelope bypasses payload formatting hooks", func(t *testing.T) {
		const rawMarkdown = "# Title\n\n**Body**"
		const wantFallback = "Title\n\nBody"

		payloadFormats := []string{"markdown-block", "quote", "collapse", "block"}

		for _, payloadFmt := range payloadFormats {
			t.Run(payloadFmt, func(t *testing.T) {
				formatCfg := &config.ChatOutputFormatConfig{
					Format:  "markdown",
					Payload: payloadFmt,
				}
				setConfigField(t, formatCfg, "UseNativeReasoning", false)

				text := mustTelegramRichEnvelope(rawMarkdown)
				delivery := resolveTelegramRichDelivery(text, "", formatCfg, true)

				assert.True(t, delivery.ShouldSendRich,
					"ShouldSendRich must be true for payload format %q", payloadFmt)
				assert.Equal(t, rawMarkdown, delivery.RichMessage.Markdown,
					"RichMessage.Markdown must equal the exact original markdown for payload format %q — no fences, prefixes, escaping, or collapse markers", payloadFmt)
				assert.Equal(t, wantFallback, delivery.VisibleText,
					"VisibleText must be the derived plain-text fallback for payload format %q", payloadFmt)
				assert.NoError(t, delivery.Err,
					"no error expected for valid envelope with payload format %q", payloadFmt)
			})
		}
	})
}

type stubTelegramRawCaller struct {
	method  string
	payload any
	body    []byte
	err     error
}

func (s *stubTelegramRawCaller) Raw(method string, payload any) ([]byte, error) {
	s.method = method
	s.payload = payload
	if s.err != nil {
		return nil, s.err
	}
	return s.body, nil
}

func mustTelegramRichEnvelope(content string) string {
	return telegramRichEnvelopeStart + content + telegramRichEnvelopeEnd
}

func setConfigField(t *testing.T, cfg *config.ChatOutputFormatConfig, name string, value any) {
	t.Helper()

	field := reflect.ValueOf(cfg).Elem().FieldByName(name)
	require.True(t, field.IsValid(), "config field %q not found", name)

	input := reflect.ValueOf(value)
	if field.Kind() == reflect.Pointer {
		ptr := reflect.New(field.Type().Elem())
		switch {
		case input.Type().AssignableTo(field.Type().Elem()):
			ptr.Elem().Set(input)
		case input.Type().ConvertibleTo(field.Type().Elem()):
			ptr.Elem().Set(input.Convert(field.Type().Elem()))
		default:
			t.Fatalf("cannot assign %s to %s", input.Type(), field.Type())
		}
		field.Set(ptr)
		return
	}

	if input.Type().AssignableTo(field.Type()) {
		field.Set(input)
		return
	}
	if input.Type().ConvertibleTo(field.Type()) {
		field.Set(input.Convert(field.Type()))
		return
	}
	t.Fatalf("cannot assign %s to %s", input.Type(), field.Type())
}
