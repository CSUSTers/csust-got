package restrict

import (
	"sync"
	"testing"
	"time"

	"csust-got/config"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
	. "gopkg.in/telebot.v3"
)

func TestBanAndGetMessageNilReplyDoesNotPanic(t *testing.T) {
	msgNoReply := &Message{
		Chat:   &Chat{ID: 1},
		Sender: &User{ID: 2},
	}
	require.NotPanics(t, func() {
		got := banAndGetMessage(msgNoReply, nil, true, time.Minute)
		require.Equal(t, "ban 谁呀，咋 ban 呀， 你到底会不会用啊:)", got)
	})

	msgNilReplySender := &Message{
		Chat:    &Chat{ID: 1},
		Sender:  &User{ID: 2},
		ReplyTo: &Message{ID: 10},
	}
	require.NotPanics(t, func() {
		got := banAndGetMessage(msgNilReplySender, nil, true, time.Minute)
		require.NotEmpty(t, got)
	})
}

func TestCheckRateExpiredMessageUsesSubsecondNanos(t *testing.T) {
	origConfig := config.BotConfig
	t.Cleanup(func() { config.BotConfig = origConfig })

	config.BotConfig = config.NewBotConfig()
	config.BotConfig.RateLimitConfig.ExpireTime = time.Second
	config.BotConfig.RateLimitConfig.Cost = 1

	limiter := rate.NewLimiter(rate.Limit(1), 1)
	oldUnix := time.Now().Add(-10 * time.Minute).Unix()
	m1 := &Message{Unixtime: oldUnix}
	m2 := &Message{Unixtime: oldUnix}

	require.True(t, checkRate(m1, limiter))
	require.False(t, checkRate(m2, limiter))
}

func TestCheckLimitConcurrentAccess(t *testing.T) {
	origConfig := config.BotConfig
	t.Cleanup(func() {
		config.BotConfig = origConfig
		limitMu.Lock()
		limitMap = make(map[string]*rate.Limiter, 16)
		limitMu.Unlock()
	})

	config.BotConfig = config.NewBotConfig()
	config.BotConfig.RateLimitConfig.Limit = 1000
	config.BotConfig.RateLimitConfig.MaxToken = 1000
	config.BotConfig.RateLimitConfig.Cost = 1
	config.BotConfig.RateLimitConfig.ExpireTime = time.Minute

	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := &Message{
				Chat:     &Chat{ID: int64(i % 4)},
				Sender:   &User{ID: int64(i)},
				Unixtime: time.Now().Unix(),
			}
			require.True(t, CheckLimit(m))
			require.True(t, CheckLimit(m))
		}()
	}
	wg.Wait()
}
