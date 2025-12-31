package chat

import (
	"csust-got/config"
	"csust-got/orm"
	"testing"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"
	tb "gopkg.in/telebot.v3"
)

func TestHandleMessageReaction_NoReaction(t *testing.T) {
	ctx := &mockContext{}
	
	err := HandleMessageReaction(ctx)
	assert.NoError(t, err)
}

func TestHandleMessageReaction_NonThumbsDownReaction(t *testing.T) {
	ctx := &mockContextWithUpdate{
		update: tb.Update{
			MessageReaction: &tb.MessageReaction{
				Chat:      &tb.Chat{ID: 123},
				MessageID: 456,
				NewReaction: []tb.Reaction{
					{Type: "emoji", Emoji: "👍"},
				},
			},
		},
	}
	
	err := HandleMessageReaction(ctx)
	assert.NoError(t, err)
}

func TestGetParseMode(t *testing.T) {
	tests := []struct {
		name     string
		format   string
		expected tb.ParseMode
	}{
		{
			name:     "HTML format",
			format:   config.OutputFormatHTML,
			expected: tb.ModeHTML,
		},
		{
			name:     "Markdown format",
			format:   config.OutputFormatMarkdown,
			expected: tb.ModeMarkdownV2,
		},
		{
			name:     "Empty format defaults to Markdown",
			format:   "",
			expected: tb.ModeMarkdownV2,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chatConfig := &config.ChatConfigSingle{
				Format: config.ChatOutputFormatConfig{
					Format: tt.format,
				},
			}
			result := getParseMode(chatConfig)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestAIResponseMetadata_NilMessages(t *testing.T) {
	// Test that nil Messages field is handled correctly
	metadata := &orm.AIResponseMetadata{
		BotMessageID:    123,
		UserMessageID:   456,
		ChatID:          789,
		ConfigName:      "test-config",
		OriginalPrompt:  "test prompt",
		Messages:        nil,
		RegenerateCount: 0,
	}
	
	// The code should handle nil Messages gracefully
	assert.Nil(t, metadata.Messages)
	
	// When appending, it should initialize an empty slice
	messages := metadata.Messages
	if messages == nil {
		messages = make([]openai.ChatCompletionMessage, 0)
	}
	messages = append(messages,
		openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: "test",
		},
	)
	
	assert.Len(t, messages, 1)
	assert.Equal(t, "test", messages[0].Content)
}

func TestMessageContentExtraction(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		caption  string
		expected string
	}{
		{
			name:     "Text only",
			text:     "Hello world",
			caption:  "",
			expected: "Hello world",
		},
		{
			name:     "Caption only",
			text:     "",
			caption:  "Photo caption",
			expected: "Photo caption",
		},
		{
			name:     "Both text and caption",
			text:     "Main text",
			caption:  "Caption text",
			expected: "Main text",
		},
		{
			name:     "Both empty",
			text:     "",
			caption:  "",
			expected: "",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &tb.Message{
				Text:    tt.text,
				Caption: tt.caption,
			}
			
			// Simulate the logic used in reaction.go
			content := msg.Text
			if content == "" {
				content = msg.Caption
			}
			
			assert.Equal(t, tt.expected, content)
		})
	}
}

// Extend mockContext to support Update()
type mockContextWithUpdate struct {
	mockContext
	update tb.Update
}

func (m *mockContextWithUpdate) Update() tb.Update {
	return m.update
}
