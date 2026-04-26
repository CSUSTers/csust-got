package main

import (
	"testing"

	"github.com/stretchr/testify/require"
	. "gopkg.in/telebot.v3"
)

func TestIsAllowedMessageCommand(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		allowed []string
		want    bool
	}{
		{
			name:    "allows info during shutdown",
			text:    "/info",
			allowed: []string{"boot", "info"},
			want:    true,
		},
		{
			name:    "allows boot during shutdown",
			text:    "/boot",
			allowed: []string{"boot", "info"},
			want:    true,
		},
		{
			name:    "does not allow other shutdown commands",
			text:    "/hello",
			allowed: []string{"boot", "info"},
		},
		{
			name:    "allows info during mc dead",
			text:    "/info",
			allowed: []string{"reburn", "info"},
			want:    true,
		},
		{
			name:    "allows reburn during mc dead",
			text:    "/reburn",
			allowed: []string{"reburn", "info"},
			want:    true,
		},
		{
			name:    "supports bot username suffix",
			text:    "/info@csust_got_bot",
			allowed: []string{"boot", "info"},
			want:    true,
		},
		{
			name:    "ignores ordinary text",
			text:    "info",
			allowed: []string{"boot", "info"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &Message{Text: tt.text}
			require.Equal(t, tt.want, isAllowedMessageCommand(msg, tt.allowed...))
		})
	}
}

func TestIsAllowedMcDeadCommand(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		shutdown  bool
		wantAllow bool
	}{
		{
			name:      "allows info",
			command:   "info",
			wantAllow: true,
		},
		{
			name:      "allows reburn",
			command:   "reburn",
			wantAllow: true,
		},
		{
			name:      "allows boot when shutdown is the first recovery step",
			command:   "boot",
			shutdown:  true,
			wantAllow: true,
		},
		{
			name:    "blocks boot when only mc dead",
			command: "boot",
		},
		{
			name:    "blocks unrelated commands",
			command: "hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.wantAllow, isAllowedMcDeadCommand(tt.command, tt.shutdown))
		})
	}
}
