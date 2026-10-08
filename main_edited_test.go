package main

import (
	"testing"

	"github.com/stretchr/testify/require"
	. "gopkg.in/telebot.v3"
)

type editedTestContext struct {
	Context
	update Update
}

func (c *editedTestContext) Update() Update    { return c.update }
func (c *editedTestContext) Message() *Message { return c.update.EditedMessage }
func (c *editedTestContext) Chat() *Chat       { return c.update.EditedMessage.Chat }
func (c *editedTestContext) Sender() *User     { return c.update.EditedMessage.Sender }

func TestEditedMessagesBypassSideEffectMiddlewares(t *testing.T) {
	edited := &Message{
		ID:       7,
		Chat:     &Chat{ID: -100, Type: ChatSuperGroup, Title: "group"},
		Sender:   &User{ID: 42, Username: "someone"},
		Text:     "edited text",
		Sticker:  &Sticker{},
		LastEdit: 1700000000,
	}
	ctx := &editedTestContext{update: Update{ID: 1, EditedMessage: edited}}
	require.True(t, isEditedUpdate(ctx))
	require.False(t, isEditedUpdate(&editedTestContext{update: Update{ID: 2}}))
	require.True(t, isEditedUpdate(&editedTestContext{update: Update{ID: 3, EditedChannelPost: edited}}))

	tests := []struct {
		name       string
		middleware MiddlewareFunc
	}{
		{name: "rate", middleware: rateMiddleware},
		{name: "fake ban", middleware: fakeBanMiddleware},
		{name: "no sticker", middleware: noStickerMiddleware},
		{name: "bye world", middleware: byeWorldMiddleware},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			handler := tt.middleware(func(Context) error {
				called = true
				return nil
			})
			// Redis is not initialised in this test: any side effect would panic instead of passing through.
			require.NoError(t, handler(ctx))
			require.True(t, called)
		})
	}
}
