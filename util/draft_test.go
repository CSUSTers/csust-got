package util

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestBuildDraftParams(t *testing.T) {
	tests := []struct {
		name      string
		chatID    int64
		draftID   int
		text      string
		parseMode tb.ParseMode
		wantKeys  map[string]string
	}{
		{
			name:      "basic draft params with markdown",
			chatID:    123456789,
			draftID:   42,
			text:      "Hello, streaming...",
			parseMode: tb.ModeMarkdownV2,
			wantKeys: map[string]string{
				"chat_id":    "123456789",
				"draft_id":   "42",
				"text":       "Hello, streaming...",
				"parse_mode": "MarkdownV2",
			},
		},
		{
			name:      "draft params with HTML",
			chatID:    987654321,
			draftID:   1,
			text:      "<b>bold</b> text",
			parseMode: tb.ModeHTML,
			wantKeys: map[string]string{
				"chat_id":    "987654321",
				"draft_id":   "1",
				"text":       "<b>bold</b> text",
				"parse_mode": "HTML",
			},
		},
		{
			name:      "draft params with default parse mode (no parse_mode param)",
			chatID:    111222333,
			draftID:   99,
			text:      "plain text",
			parseMode: tb.ModeDefault,
			wantKeys: map[string]string{
				"chat_id":  "111222333",
				"draft_id": "99",
				"text":     "plain text",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]string{
				"chat_id":  strconv.FormatInt(tt.chatID, 10),
				"draft_id": strconv.Itoa(tt.draftID),
				"text":     tt.text,
			}
			if tt.parseMode != tb.ModeDefault {
				params["parse_mode"] = tt.parseMode
			}

			for key, want := range tt.wantKeys {
				got, ok := params[key]
				require.True(t, ok, "expected key %q in params", key)
				require.Equal(t, want, got, "param %q mismatch", key)
			}

			if tt.parseMode == tb.ModeDefault {
				_, ok := params["parse_mode"]
				require.False(t, ok, "parse_mode should not be set for ModeDefault")
			}
		})
	}
}
