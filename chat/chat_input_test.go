package chat

import (
	"testing"

	"csust-got/config"

	"github.com/stretchr/testify/assert"
	tb "gopkg.in/telebot.v3"
)

func TestExtractInputFromMessage_RemovesCommandPrefix(t *testing.T) {
	trigger := &config.ChatTrigger{Command: "think"}
	tests := []struct {
		name string
		msg  *tb.Message
		want string
	}{
		{
			name: "plain command prefix stripped",
			msg:  &tb.Message{Text: "/think hello world"},
			want: "hello world",
		},
		{
			name: "command with bot mention stripped",
			msg:  &tb.Message{Text: "/think@bot hello world"},
			want: "hello world",
		},
		{
			name: "no command prefix kept",
			msg:  &tb.Message{Text: "hello world"},
			want: "hello world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractInputFromMessage(tt.msg, trigger)
			assert.Equal(t, tt.want, got)
		})
	}
}
