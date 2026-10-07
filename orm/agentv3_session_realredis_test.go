//go:build session_realredis

package orm

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type sessionRealRedis struct {
	client  *redis.Client
	address string
	mu      sync.RWMutex
	now     time.Time
}

func newSessionTestRedis(t *testing.T) sessionTestRedis {
	t.Helper()
	executable, err := exec.LookPath("redis-server")
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	dir := t.TempDir()
	cmd := exec.Command(executable, "--bind", "127.0.0.1", "--port", strconv.Itoa(port), "--save", "", "--appendonly", "no", "--dir", dir, "--loglevel", "warning", "--maxclients", "64")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	client := redis.NewClient(&redis.Options{Addr: address, MaxRetries: -1, DialTimeout: time.Second})
	t.Cleanup(func() { _ = client.Close() })
	require.Eventually(t, func() bool { return client.Ping(t.Context()).Err() == nil }, 5*time.Second, 20*time.Millisecond)
	serverTime, err := client.Time(t.Context()).Result()
	require.NoError(t, err)
	require.False(t, serverTime.IsZero())
	return &sessionRealRedis{client: client, address: address, now: time.Now()}
}

func (r *sessionRealRedis) Addr() string          { return r.address }
func (r *sessionRealRedis) SetTime(now time.Time) { r.mu.Lock(); r.now = now; r.mu.Unlock() }
func (r *sessionRealRedis) Keys() []string        { return r.client.Keys(context.Background(), "*").Val() }
func (r *sessionRealRedis) TTL(key string) time.Duration {
	ttl := r.client.TTL(context.Background(), key).Val()
	if ttl < 0 {
		return 0
	}
	return ttl
}

func (r *sessionRealRedis) configureClient(client *redis.Client)        { client.AddHook(r) }
func (r *sessionRealRedis) DialHook(next redis.DialHook) redis.DialHook { return next }
func (r *sessionRealRedis) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (r *sessionRealRedis) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if clock, ok := cmd.(*redis.TimeCmd); ok {
			r.mu.RLock()
			now := r.now
			r.mu.RUnlock()
			clock.SetVal(now)
			return nil
		}
		return next(ctx, cmd)
	}
}
