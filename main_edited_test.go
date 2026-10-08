package main

import (
	"sync"
	"testing"
	"time"

	"csust-got/config"
	"csust-got/log"
	"csust-got/orm"

	"github.com/alicebob/miniredis/v2"
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
		{name: "shutdown", middleware: shutdownMiddleware},
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

type newMessageTestContext struct {
	Context
	message *Message
}

func (c *newMessageTestContext) Update() Update    { return Update{ID: 1, Message: c.message} }
func (c *newMessageTestContext) Message() *Message { return c.message }
func (c *newMessageTestContext) Chat() *Chat       { return c.message.Chat }
func (c *newMessageTestContext) Sender() *User     { return c.message.Sender }

func setupMainTestRedis(t *testing.T) {
	t.Helper()
	oldConfig := config.BotConfig
	miniRedis := miniredis.RunT(t)
	testConfig := config.NewBotConfig()
	testConfig.RedisConfig.RedisAddr = miniRedis.Addr()
	testConfig.RedisConfig.KeyPrefix = "main-test:"
	config.BotConfig = testConfig
	orm.InitRedis()
	log.InitLogger()
	t.Cleanup(func() {
		config.BotConfig = oldConfig
		if oldConfig != nil && oldConfig.RedisConfig != nil {
			orm.InitRedis()
		}
	})
}

func trackMessageStore(t *testing.T) {
	t.Helper()
	pending := &sync.WaitGroup{}
	messageStoreAsync = pending
	t.Cleanup(func() {
		pending.Wait()
		messageStoreAsync = nil
	})
}

func TestShutdownMiddlewarePassesEditsInShutdownChat(t *testing.T) {
	setupMainTestRedis(t)
	chat := &Chat{ID: -100, Type: ChatSuperGroup, Title: "group"}
	orm.Shutdown(chat.ID)
	message := &Message{ID: 7, Chat: chat, Sender: &User{ID: 42}, Text: "text", Unixtime: 1700000000}

	tests := []struct {
		name string
		ctx  Context
		want bool
	}{
		{name: "edited", ctx: &editedTestContext{update: Update{ID: 1, EditedMessage: &Message{ID: 7, Chat: chat, Sender: &User{ID: 42}, Text: "edited", LastEdit: 1700000100}}}, want: true},
		{name: "new message", ctx: &newMessageTestContext{message: message}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			handler := shutdownMiddleware(func(Context) error {
				called = true
				return nil
			})
			require.NoError(t, handler(tt.ctx))
			require.Equal(t, tt.want, called)
		})
	}
}

func TestMessageStoreMiddlewareOrdersSameSecondEditsByUpdateID(t *testing.T) {
	setupMainTestRedis(t)
	trackMessageStore(t)
	chat := &Chat{ID: -100, Type: ChatSuperGroup, Title: "group"}
	handler := messageStoreMiddleware(func(Context) error { return nil })
	before := messageStoreSeq.Load()
	for _, edit := range []struct {
		updateID int
		text     string
	}{{updateID: 3, text: "second edit"}, {updateID: 2, text: "first edit"}} {
		edited := &Message{ID: 7, Chat: chat, Sender: &User{ID: 42}, Text: edit.text, Unixtime: 1700000000, LastEdit: 1700000100}
		require.NoError(t, handler(&editedTestContext{update: Update{ID: edit.updateID, EditedMessage: edited}}))
	}
	require.Equal(t, before, messageStoreSeq.Load())

	require.Eventually(t, func() bool {
		stream, err := orm.GetMessagesFromStream(chat.ID, orm.MessageStreamQuery{})
		if err != nil || len(stream) != 1 || stream[0].Text != "second edit" {
			return false
		}
		full, err := orm.GetMessage(chat.ID, 7)
		return err == nil && full.Text == "second edit"
	}, 2*time.Second, 10*time.Millisecond)
}

func TestMessageStoreMiddlewareFallsBackToCounterWithoutUpdateID(t *testing.T) {
	setupMainTestRedis(t)
	trackMessageStore(t)
	chat := &Chat{ID: -101, Type: ChatSuperGroup, Title: "group"}
	handler := messageStoreMiddleware(func(Context) error { return nil })
	before := messageStoreSeq.Load()
	for _, text := range []string{"first edit", "second edit"} {
		edited := &Message{ID: 7, Chat: chat, Sender: &User{ID: 42}, Text: text, Unixtime: 1700000000, LastEdit: 1700000100}
		require.NoError(t, handler(&editedTestContext{update: Update{EditedMessage: edited}}))
	}
	require.Equal(t, before+2, messageStoreSeq.Load())

	require.Eventually(t, func() bool {
		full, err := orm.GetMessage(chat.ID, 7)
		return err == nil && full.Text == "second edit"
	}, 2*time.Second, 10*time.Millisecond)
}
