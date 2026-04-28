//go:build !386 && !arm

package chatv2

import (
	"context"
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
		ctx: context.Background(),
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

type mockStreamingContext struct {
	tb.Context
	bot *tb.Bot
}

func (m *mockStreamingContext) Bot() *tb.Bot {
	return m.bot
}
