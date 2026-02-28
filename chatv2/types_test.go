package chatv2

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	tb "gopkg.in/telebot.v3"
)

func TestWithTurnContext(t *testing.T) {
	tc := &TurnContext{
		ChatID:  12345,
		Message: &tb.Message{Text: "hello"},
	}

	ctx := WithTurnContext(t.Context(), tc)
	got := GetTurnContext(ctx)

	assert.NotNil(t, got)
	assert.Equal(t, int64(12345), got.ChatID)
	assert.Equal(t, "hello", got.Message.Text)
}

func TestGetTurnContext_Missing(t *testing.T) {
	ctx := t.Context()
	got := GetTurnContext(ctx)
	assert.Nil(t, got)
}

func TestGetTurnContext_WrongType(t *testing.T) {
	ctx := context.WithValue(t.Context(), turnContextKey{}, "wrong type")
	got := GetTurnContext(ctx)
	assert.Nil(t, got)
}
