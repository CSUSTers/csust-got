package main

import (
	"testing"
	"time"

	"csust-got/config"
	"csust-got/log"

	"github.com/stretchr/testify/require"
	. "gopkg.in/telebot.v3"
)

type skipTestContext struct {
	Context
	update Update
}

func (c *skipTestContext) Update() Update { return c.update }
func (c *skipTestContext) Query() *Query  { return nil }
func (c *skipTestContext) Message() *Message {
	if c.update.EditedMessage != nil {
		return c.update.EditedMessage
	}
	return c.update.Message
}

func TestSkipMiddlewareUsesEditTimeForEditedUpdates(t *testing.T) {
	originalConfig := config.BotConfig
	t.Cleanup(func() { config.BotConfig = originalConfig })
	config.BotConfig = config.NewBotConfig()
	log.InitLogger()

	now := time.Now().Unix()
	old := now - 3600
	tests := []struct {
		name     string
		skipSec  int64
		update   Update
		wantNext bool
	}{
		{name: "fresh message passes", skipSec: 60, update: Update{Message: &Message{Unixtime: now}}, wantNext: true},
		{name: "expired message skipped", skipSec: 60, update: Update{Message: &Message{Unixtime: old}}, wantNext: false},
		{name: "fresh edit of expired message passes", skipSec: 60, update: Update{EditedMessage: &Message{Unixtime: old, LastEdit: now}}, wantNext: true},
		{name: "fresh edited channel post passes", skipSec: 60, update: Update{EditedChannelPost: &Message{Unixtime: old, LastEdit: now}}, wantNext: true},
		{name: "expired edit skipped", skipSec: 60, update: Update{EditedMessage: &Message{Unixtime: old, LastEdit: old + 1}}, wantNext: false},
		{name: "edit without edit date uses message time", skipSec: 60, update: Update{EditedMessage: &Message{Unixtime: old}}, wantNext: false},
		{name: "skip disabled passes expired", skipSec: 0, update: Update{Message: &Message{Unixtime: old}}, wantNext: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config.BotConfig = config.NewBotConfig()
			config.BotConfig.SkipDuration = tt.skipSec
			ctx := &skipTestContext{update: tt.update}
			if tt.update.EditedChannelPost != nil {
				ctx.update.EditedMessage = tt.update.EditedChannelPost
			}
			called := false
			handler := skipMiddleware(func(Context) error {
				called = true
				return nil
			})
			require.NoError(t, handler(ctx))
			require.Equal(t, tt.wantNext, called)
		})
	}
}
