package main

import (
	"csust-got/config"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
)

type mainTestContext struct {
	Context
	msg *Message
	bot *Bot
}

func (m *mainTestContext) Message() *Message { return m.msg }
func (m *mainTestContext) Chat() *Chat       { return m.msg.Chat }
func (m *mainTestContext) Sender() *User     { return m.msg.Sender }
func (m *mainTestContext) Bot() *Bot         { return m.bot }

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

func TestCustomHandler_EnabledGlobalWhitelistRejectsUnlistedAgentBeforeFallback(t *testing.T) {
	originalConfig := config.BotConfig
	originalRegexHandlers := regexHandlers
	restoreLogger := zap.ReplaceGlobals(zap.NewNop())
	t.Cleanup(func() {
		config.BotConfig = originalConfig
		regexHandlers = originalRegexHandlers
		restoreLogger()
	})

	config.BotConfig = config.NewBotConfig()
	config.BotConfig.ChatEngine = "v2"
	config.BotConfig.WhiteListConfig.Enabled = true
	config.BotConfig.WhiteListConfig.Chats = []int64{300}
	*config.BotConfig.Agents = config.ChatConfigV2{
		&config.ChatConfigSingle{
			Name:    "uncompiled-agent",
			Agent:   &config.AgentConfig{Enable: true},
			Trigger: []*config.ChatTrigger{{Reply: true}},
		},
	}
	regexHandlers = nil

	ctx := &mainTestContext{
		msg: &Message{
			Text:   "hello",
			Chat:   &Chat{ID: 200},
			Sender: &User{ID: 100},
			ReplyTo: &Message{
				Sender: &User{Username: "test_bot"},
			},
		},
		bot: &Bot{Me: &User{Username: "test_bot"}},
	}

	require.NotPanics(t, func() {
		require.NoError(t, customHandler(ctx))
	}, "unlisted Agent chat must be rejected before legacy fallback")
}
