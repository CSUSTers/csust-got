package orm

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"csust-got/config"
	"csust-got/log"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestMessageReaderBatchReusesOperationConnection(t *testing.T) {
	mini, client, dials, closes := setupMessageReaderRedis(t, nil, nil)
	require.NoError(t, client.Ping(t.Context()).Err())
	mini.Set(wrapKeyWithChatMsg("message_full", -100, 10), string(cachedMessageJSON(t, cachedPollMessage(10, "quiz"))))
	mini.Set(wrapKeyWithChatMsg("message_full", -100, 11), `{"message_id":`)
	var mgets, gets atomic.Int32
	mini.Server().SetPreHook(func(_ *server.Peer, command string, _ ...string) bool {
		switch command {
		case "MGET":
			mgets.Add(1)
		case "GET":
			gets.Add(1)
		}
		return false
	})
	reader := NewMessageReader(t.Context())
	t.Cleanup(reader.Close)
	for round := range 6 {
		if round == 1 {
			mini.Set(wrapKeyWithChatMsg("message_full", -100, 12), string(cachedMessageJSON(t, cachedPollMessage(12, "regular"))))
		}
		messages, err := reader.GetMessages(-100, []int{10, 11, 12})
		require.NoError(t, err)
		require.Equal(t, 10, messages[10].ID)
		require.Nil(t, messages[11], "corrupt record does not hide healthy batch siblings")
		if round == 0 {
			require.Nil(t, messages[12])
		} else {
			require.Equal(t, 12, messages[12].ID, "a prior miss is retryable")
		}
	}
	reader.Close()
	reader.Close()
	require.EqualValues(t, 2, dials.Load(), "one shared connection plus one operation connection, not one per round/ID")
	require.EqualValues(t, 1, closes.Load(), "operation connection is synchronously closed")
	require.EqualValues(t, 6, mgets.Load())
	require.Zero(t, gets.Load())
	require.NoError(t, client.Ping(t.Context()).Err())
	require.EqualValues(t, 2, dials.Load(), "shared Ping reuses the untouched connection")
	require.Equal(t, 20*time.Second, client.Options().ReadTimeout)
	require.Equal(t, 3, client.Options().PoolSize)
	require.False(t, client.Options().ContextTimeoutEnabled)
	t.Logf("six batches: private dials=%d, closed=%d, MGET=%d, GET=%d; shared Ping reused", dials.Load()-1, closes.Load(), mgets.Load(), gets.Load())
}

func TestMessageReaderBlockedBatchCancellationWaitsForCleanup(t *testing.T) {
	for _, action := range []string{"cancel", "deadline", "close"} {
		t.Run(action, func(t *testing.T) {
			closeStarted, cleanupRelease := make(chan struct{}), make(chan struct{})
			mini, client, dials, closes := setupMessageReaderRedis(t, closeStarted, cleanupRelease)
			require.NoError(t, client.Ping(t.Context()).Err())
			started, release := make(chan struct{}, 1), make(chan struct{})
			mini.Server().SetPreHook(func(peer *server.Peer, command string, args ...string) bool {
				if command != "MGET" || len(args) == 0 || !strings.Contains(args[0], "message_full:") {
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
			if action == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 150*time.Millisecond)
			}
			defer cancel()
			reader := NewMessageReader(ctx)
			returned := make(chan error, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				_, err := reader.GetMessages(-100, []int{41, 42, 43})
				reader.Close()
				returned <- err
			}()
			var releaseCleanup sync.Once
			t.Cleanup(func() {
				cancel()
				releaseCleanup.Do(func() { close(cleanupRelease) })
				close(release)
				select {
				case <-finished:
				case <-time.After(time.Second):
					t.Error("query worker did not stop")
				}
			})
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("MGET did not reach the blocked Redis socket")
			}
			closed := make(chan struct{})
			switch action {
			case "cancel":
				cancel()
			case "close":
				go func() { reader.Close(); close(closed) }()
			}
			select {
			case <-closeStarted:
			case <-time.After(time.Second):
				t.Fatal("operation did not actively close the blocked socket")
			}
			select {
			case <-returned:
				t.Fatal("operation returned before cancellation cleanup completed")
			default:
			}
			releaseCleanup.Do(func() { close(cleanupRelease) })
			select {
			case err := <-returned:
				switch action {
				case "cancel":
					require.ErrorIs(t, err, context.Canceled)
				case "deadline":
					require.ErrorIs(t, err, context.DeadlineExceeded)
				case "close":
					require.Error(t, err)
					<-closed
				}
			case <-time.After(time.Second):
				t.Fatal("operation waited for the 20-second socket timeout")
			}
			require.EqualValues(t, 2, dials.Load())
			require.EqualValues(t, 1, closes.Load())
			require.NoError(t, client.Ping(t.Context()).Err())
			require.False(t, client.Options().ContextTimeoutEnabled)
			t.Logf("%s interrupted MGET: one operation dial/close, cleanup joined, shared Ping healthy", action)
		})
	}
}

type messageReaderTestConn struct {
	net.Conn
	once    sync.Once
	closes  *atomic.Int32
	started chan struct{}
	release <-chan struct{}
}

func (c *messageReaderTestConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		c.closes.Add(1)
		if c.started != nil {
			close(c.started)
			<-c.release
		}
	})
	return err
}

func setupMessageReaderRedis(t *testing.T, closeStarted chan struct{}, cleanupRelease <-chan struct{}) (*miniredis.Miniredis, *redis.Client, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	originalClient, originalConfig := rc, config.BotConfig
	mini := miniredis.RunT(t)
	dials, closes := &atomic.Int32{}, &atomic.Int32{}
	client := redis.NewClient(&redis.Options{
		Addr: mini.Addr(), ReadTimeout: 20 * time.Second, MaxRetries: -1, PoolSize: 3,
		Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			wrapped := &messageReaderTestConn{Conn: conn, closes: closes}
			if dials.Add(1) == 2 {
				wrapped.started, wrapped.release = closeStarted, cleanupRelease
			}
			return wrapped, nil
		},
	})
	rc, config.BotConfig = client, config.NewBotConfig()
	log.InitLogger()
	t.Cleanup(func() { _ = client.Close(); rc, config.BotConfig = originalClient, originalConfig })
	return mini, client, dials, closes
}
