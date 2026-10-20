package orm

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"csust-got/config"
	"csust-got/log"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func lifecycleTestMessage(id int) *tb.Message {
	return &tb.Message{ID: id, Chat: &tb.Chat{ID: -100}, Sender: &tb.User{ID: 99, IsBot: true}, Text: "in flight"}
}

func TestMessageReaderLifecycleBatchCancellationUsesOperationConnection(t *testing.T) {
	mini, client, dials, closes := setupMessageReaderRedis(t, nil, nil)
	require.NoError(t, client.Ping(t.Context()).Err())
	started, release := make(chan struct{}, 1), make(chan struct{})
	mini.Server().SetPreHook(func(peer *server.Peer, command string, args ...string) bool {
		if command != "MGET" || len(args) == 0 || !strings.Contains(args[0], "message_bot_lifecycle:") {
			return false
		}
		started <- struct{}{}
		<-release
		peer.WriteLen(len(args))
		for range args {
			peer.WriteNull()
		}
		return true
	})
	ctx, cancel := context.WithCancel(t.Context())
	reader := NewMessageReader(ctx)
	returned := make(chan error, 1)
	go func() {
		_, err := reader.GetBotMessageLifecycles(-100, []int{1, 2, 3})
		reader.Close()
		returned <- err
	}()
	t.Cleanup(func() { cancel(); close(release); reader.Close() })
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("lifecycle batch did not reach Redis")
	}
	cancel()
	select {
	case err := <-returned:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("lifecycle batch waited for socket timeout")
	}
	require.EqualValues(t, 2, dials.Load())
	require.EqualValues(t, 1, closes.Load())
	require.NoError(t, client.Ping(t.Context()).Err())
}

func TestBotLifecycleExpiresAfterStreamReferencesAreTrimmed(t *testing.T) {
	mini := setupMessageCacheRedis(t)
	process := lifecycleTestMessage(1)
	require.NoError(t, StoreBotMessageLifecycle(process, 99, false))
	require.NoError(t, PushMessageToStream(process))
	mini.FastForward(23 * time.Hour)
	for id := 2; id <= int(messageStreamMaxLen)+1; id++ {
		require.NoError(t, PushMessageToStream(lifecycleTestMessage(id)))
	}
	mini.FastForward(23 * time.Hour)
	require.NoError(t, PushMessageToStream(lifecycleTestMessage(2000)))
	mini.FastForward(2 * time.Hour)
	require.NoError(t, PushMessageToStream(lifecycleTestMessage(2001)))
	state, err := GetBotMessageLifecycle(t.Context(), -100, 1)
	require.NoError(t, err)
	require.Nil(t, state)
	messages, err := GetMessagesFromStream(-100, MessageStreamQuery{})
	require.NoError(t, err)
	require.NotEmpty(t, messages)
}

func TestBotLifecycleFollowsFullAndCanonicalAncestorReferences(t *testing.T) {
	mini := setupMessageCacheRedis(t)
	ancestor := lifecycleTestMessage(1)
	require.NoError(t, StoreBotMessageLifecycle(ancestor, 99, false))
	final := lifecycleTestMessage(2)
	final.ReplyTo = ancestor
	final.Text = "final answer"
	require.NoError(t, StoreBotMessageLifecycle(final, 99, true))
	mini.FastForward(23 * time.Hour)
	// A newly cached input has only the final ID; its canonical snapshot still references the ancestor.
	input := &tb.Message{ID: 3, Chat: final.Chat, ReplyTo: &tb.Message{ID: final.ID}}
	require.NoError(t, SetMessage(input))
	mini.FastForward(2 * time.Hour)
	for _, id := range []int{1, 2} {
		state, err := GetBotMessageLifecycle(t.Context(), -100, id)
		require.NoError(t, err)
		require.NotNil(t, state)
	}
	mini.FastForward(23 * time.Hour)
	for _, id := range []int{1, 2} {
		state, err := GetBotMessageLifecycle(t.Context(), -100, id)
		require.NoError(t, err)
		require.Nil(t, state)
	}
}

func TestBotLifecycleFullReferencesOutliveTrimmedStream(t *testing.T) {
	mini := setupMessageCacheRedis(t)
	ancestor := lifecycleTestMessage(1)
	require.NoError(t, StoreBotMessageLifecycle(ancestor, 99, false))
	require.NoError(t, PushMessageToStream(ancestor))
	final := lifecycleTestMessage(2)
	final.ReplyTo = ancestor
	final.Sender = &tb.User{ID: 9007199254740993, IsBot: true}
	final.Text = "final answer"
	require.NoError(t, StoreBotMessageLifecycle(final, final.Sender.ID, true))
	require.NoError(t, PushMessageToStream(final))
	mini.FastForward(23 * time.Hour)
	require.NoError(t, PushMessageToStream(&tb.Message{ID: 3, Chat: final.Chat, Sender: &tb.User{ID: 7}}))
	mini.FastForward(2 * time.Hour)
	input := &tb.Message{ID: 4, Chat: final.Chat, Sender: &tb.User{ID: 7}, ReplyTo: &tb.Message{ID: final.ID}}
	require.NoError(t, SetMessage(input))
	for id := 5; id < 1005; id++ {
		require.NoError(t, PushMessageToStream(&tb.Message{ID: id, Chat: final.Chat, Sender: &tb.User{ID: 7}}))
	}
	mini.FastForward(23 * time.Hour)
	for _, id := range []int{1, 2} {
		state, err := GetBotMessageLifecycle(t.Context(), -100, id)
		require.NoError(t, err)
		require.NotNil(t, state)
		if id == 2 {
			require.Equal(t, int64(9007199254740993), state.Message.Sender.ID)
		}
	}
	require.NoError(t, PushMessageToStream(&tb.Message{ID: 1005, Chat: final.Chat, Sender: &tb.User{ID: 7}}))
	mini.FastForward(2 * time.Hour)
	for _, id := range []int{1, 2} {
		state, err := GetBotMessageLifecycle(t.Context(), -100, id)
		require.NoError(t, err)
		require.Nil(t, state)
	}
}

func TestBotLifecycleFinalUpdatesActiveAncestorReferencesWithoutAnotherAppend(t *testing.T) {
	mini := setupMessageCacheRedis(t)
	ancestor := lifecycleTestMessage(1)
	require.NoError(t, StoreBotMessageLifecycle(ancestor, 99, false))
	require.NoError(t, PushMessageToStream(ancestor))
	user := &tb.Message{ID: 2, Chat: ancestor.Chat, Sender: &tb.User{ID: 7}, Text: "request"}
	process := lifecycleTestMessage(3)
	process.ReplyTo = user
	require.NoError(t, StoreBotMessageLifecycle(process, 99, false))
	require.NoError(t, PushMessageToStream(process))
	user.ReplyTo = &tb.Message{ID: 1}
	process.Text = "final answer"
	require.NoError(t, StoreBotMessageLifecycle(process, 99, true))
	mini.FastForward(23 * time.Hour)
	for id := 4; id < 1003; id++ {
		require.NoError(t, PushMessageToStream(&tb.Message{ID: id, Chat: user.Chat, Sender: user.Sender}))
	}
	mini.FastForward(2 * time.Hour)
	state, err := GetBotMessageLifecycle(t.Context(), -100, 1)
	require.NoError(t, err)
	require.NotNil(t, state, "the retained final snapshot still references this process ancestor")
	state, err = GetBotMessageLifecycle(t.Context(), -100, 3)
	require.NoError(t, err)
	require.True(t, state.Final)
	require.Equal(t, "final answer", state.Message.Text)
	require.NoError(t, PushMessageToStream(&tb.Message{ID: 1003, Chat: user.Chat, Sender: user.Sender}))
	state, err = GetBotMessageLifecycle(t.Context(), -100, 1)
	require.NoError(t, err)
	require.Nil(t, state)
}

func BenchmarkPushMessageToStreamActiveWindow(b *testing.B) {
	for _, size := range []int{1, 1000} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			mini := miniredis.RunT(b)
			oldClient, oldConfig := rc, config.BotConfig
			rc, config.BotConfig = redis.NewClient(&redis.Options{Addr: mini.Addr()}), config.NewBotConfig()
			log.InitLogger()
			b.Cleanup(func() { _ = rc.Close(); rc, config.BotConfig = oldClient, oldConfig })
			message := &tb.Message{ID: 1, Chat: &tb.Chat{ID: -100}, Sender: &tb.User{ID: 7}, Text: strings.Repeat("x", 4096)}
			encoded, err := json.Marshal(message)
			require.NoError(b, err)
			_, err = rc.Pipelined(b.Context(), func(pipe redis.Pipeliner) error {
				for id := range size {
					pipe.XAdd(b.Context(), &redis.XAddArgs{Stream: wrapKeyWithChat("message_stream", -100), Values: []any{"id", id + 1, "message", encoded}})
				}
				return nil
			})
			require.NoError(b, err)
			b.ResetTimer()
			for i := range b.N {
				message.ID = size + i + 1
				require.NoError(b, PushMessageToStream(message))
			}
		})
	}
}
