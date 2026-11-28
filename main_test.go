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
	msg    *Message
	chat   *Chat
	bot    *Bot
	update Update
}

func (m *mainTestContext) Message() *Message { return m.msg }
func (m *mainTestContext) Chat() *Chat {
	if m.chat != nil {
		return m.chat
	}
	if m.msg != nil {
		return m.msg.Chat
	}
	return nil
}
func (m *mainTestContext) Sender() *User {
	if m.msg != nil {
		return m.msg.Sender
	}
	return nil
}
func (m *mainTestContext) Bot() *Bot      { return m.bot }
func (m *mainTestContext) Update() Update { return m.update }

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

func TestCustomHandler_EnabledGlobalWhitelistRejectsUnlistedAgent(t *testing.T) {
	originalConfig := config.BotConfig
	originalRegexHandlers := regexHandlers
	restoreLogger := zap.ReplaceGlobals(zap.NewNop())
	t.Cleanup(func() {
		config.BotConfig = originalConfig
		regexHandlers = originalRegexHandlers
		restoreLogger()
	})

	config.BotConfig = config.NewBotConfig()
	config.BotConfig.AgentV3.Enable = true
	config.BotConfig.WhiteListConfig.Enabled = true
	config.BotConfig.WhiteListConfig.Chats = []int64{300}
	*config.BotConfig.Agents = config.AgentV3Configs{
		&config.AgentConfig{
			Name:    "uncompiled-agent",
			Agent:   &config.AgentOptions{Enable: true},
			Trigger: []*config.AgentTrigger{{Reply: true}},
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
	}, "unlisted agent must be rejected before dispatch")
}

func TestShouldStoreMessageIncludesMediaWithoutText(t *testing.T) {
	tests := []struct {
		name string
		msg  *Message
		want bool
	}{
		{name: "nil"},
		{name: "empty", msg: &Message{}},
		{name: "text", msg: &Message{Text: "hello"}, want: true},
		{name: "caption", msg: &Message{Caption: "caption"}, want: true},
		{name: "photo", msg: &Message{Photo: &Photo{}}, want: true},
		{name: "sticker", msg: &Message{Sticker: &Sticker{}}, want: true},
		{name: "document", msg: &Message{Document: &Document{}}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, shouldStoreMessage(tt.msg))
		})
	}
}

func TestCustomHandlerReplyToNilSenderDoesNotPanic(t *testing.T) {
	originalConfig := config.BotConfig
	originalRegexHandlers := regexHandlers
	t.Cleanup(func() {
		config.BotConfig = originalConfig
		regexHandlers = originalRegexHandlers
	})

	config.BotConfig = config.NewBotConfig()
	regexHandlers = nil

	ctx := &mainTestContext{
		msg: &Message{
			Text:    "replying to channel post",
			Chat:    &Chat{ID: 200},
			Sender:  &User{ID: 100},
			ReplyTo: &Message{ID: 10},
		},
		bot: &Bot{Me: &User{Username: "test_bot"}},
	}

	require.NotPanics(t, func() {
		require.NoError(t, customHandler(ctx))
	})
}

func TestMcMiddlewareSkipsNilMessageAndEditedUpdates(t *testing.T) {
	originalConfig := config.BotConfig
	t.Cleanup(func() { config.BotConfig = originalConfig })

	config.BotConfig = config.NewBotConfig()
	config.BotConfig.McConfig.Mc2Dead = 3

	called := 0
	mw := mcMiddleware(func(Context) error {
		called++
		return nil
	})

	require.NoError(t, mw(&mainTestContext{
		chat: &Chat{ID: -100, Type: ChatSuperGroup},
		msg:  nil,
	}))
	require.Equal(t, 1, called)

	editedMsg := &Message{
		Chat:     &Chat{ID: -100, Type: ChatSuperGroup},
		Text:     "edited",
		LastEdit: 100,
	}
	require.NoError(t, mw(&mainTestContext{
		msg:    editedMsg,
		update: Update{EditedMessage: editedMsg},
	}))
	require.Equal(t, 2, called)
}
