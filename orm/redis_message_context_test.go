package orm

import (
	"context"
	"strings"
	"testing"
	"time"

	"csust-got/config"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGetMessageContextInterruptsBlockedRedisRead(t *testing.T) {
	for _, action := range []string{"cancel", "deadline"} {
		t.Run(action, func(t *testing.T) {
			originalClient, originalConfig := rc, config.BotConfig
			mini := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: mini.Addr(), ReadTimeout: 20 * time.Second, MaxRetries: -1})
			rc, config.BotConfig = client, config.NewBotConfig()
			t.Cleanup(func() { _ = client.Close(); rc, config.BotConfig = originalClient, originalConfig })
			started, release := make(chan struct{}, 1), make(chan struct{})
			mini.Server().SetPreHook(func(peer *server.Peer, command string, args ...string) bool {
				if command != "GET" || len(args) == 0 || !strings.Contains(args[0], "message_full:") {
					return false
				}
				started <- struct{}{}
				<-release
				peer.WriteNull()
				return true
			})
			defer close(release)
			ctx, cancel := context.WithCancel(t.Context())
			if action == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 100*time.Millisecond)
			}
			defer cancel()
			returned := make(chan error, 1)
			go func() { _, err := GetMessageContext(ctx, -100, 42); returned <- err }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("Redis GET did not reach blocked socket read")
			}
			if action == "cancel" {
				cancel()
			}
			select {
			case err := <-returned:
				if action == "cancel" {
					require.ErrorIs(t, err, context.Canceled)
				} else {
					require.ErrorIs(t, err, context.DeadlineExceeded)
				}
			case <-time.After(time.Second):
				t.Fatal("context did not interrupt blocked Redis read")
			}
			require.NoError(t, client.Ping(t.Context()).Err(), "cancelling a query must not close the shared Redis client")
			require.Equal(t, 20*time.Second, client.Options().ReadTimeout)
			require.False(t, client.Options().ContextTimeoutEnabled)
		})
	}
}
