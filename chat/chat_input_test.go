package chat

import (
	"testing"

	"csust-got/config"

	"github.com/stretchr/testify/assert"
	tb "gopkg.in/telebot.v3"
)

func TestExtractInputFromMessage_RemovesCommandPrefix(t *testing.T) {
	trigger := &config.ChatTrigger{Command: "think"}
	msg := &tb.Message{Text: "/think hello world"}

	got := extractInputFromMessage(msg, trigger)

	assert.Equal(t, "hello world", got)
}
