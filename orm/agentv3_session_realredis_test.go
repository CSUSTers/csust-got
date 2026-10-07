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

	"csust-got/agent/session"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type sessionRealRedis struct {
	client    *redis.Client
	address   string
	mu        sync.RWMutex
	now       time.Time
	realClock bool
}

func newSessionTestRedis(t testing.TB) sessionTestRedis {
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

func (r *sessionRealRedis) configureClient(client *redis.Client) { client.AddHook(r) }
func (r *sessionRealRedis) interceptsTime() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return !r.realClock
}
func (r *sessionRealRedis) advanceClock() time.Time {
	r.mu.Lock()
	if !r.realClock {
		r.now = r.now.Add(time.Millisecond)
		now := r.now
		r.mu.Unlock()
		return now
	}
	previous := r.now
	r.mu.Unlock()
	for {
		now, err := r.client.Time(context.Background()).Result()
		if err != nil {
			panic(err)
		}
		if now.UnixMilli() > previous.UnixMilli() {
			r.SetTime(now)
			return now
		}
		time.Sleep(time.Millisecond)
	}
}
func (r *sessionRealRedis) DialHook(next redis.DialHook) redis.DialHook { return next }
func (r *sessionRealRedis) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		r.mu.RLock()
		now, realClock := r.now, r.realClock
		r.mu.RUnlock()
		if realClock {
			return next(ctx, cmds)
		}
		forward := make([]redis.Cmder, 0, len(cmds))
		for _, cmd := range cmds {
			if clock, ok := cmd.(*redis.TimeCmd); ok {
				clock.SetVal(now)
			} else {
				forward = append(forward, cmd)
			}
		}
		if len(forward) == 0 {
			return nil
		}
		return next(ctx, forward)
	}
}
func (r *sessionRealRedis) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if clock, ok := cmd.(*redis.TimeCmd); ok {
			r.mu.RLock()
			now := r.now
			realClock := r.realClock
			r.mu.RUnlock()
			if realClock {
				return next(ctx, cmd)
			}
			clock.SetVal(now)
			return nil
		}
		return next(ctx, cmd)
	}
}

func sessionServerClock(t testing.TB, f *sessionFixture) {
	t.Helper()
	r := f.mr.(*sessionRealRedis)
	r.mu.Lock()
	r.realClock = true
	r.mu.Unlock()
	now, err := r.client.Time(t.Context()).Result()
	require.NoError(t, err)
	f.now = now
	r.SetTime(now)
}

func TestAgentV3SessionUninterceptedServerTime(t *testing.T) {
	f := newSessionFixture(t, session.Options{TTL: time.Millisecond, LeaseDuration: time.Hour})
	sessionServerClock(t, f)
	root, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "server time"))
	require.NoError(t, err)
	pin, err := f.repo.ResolveAndPin(t.Context(), session.Selection{Scope: f.scope, Mode: session.SelectReply, ReplyMessageID: 101}, sessionRun(t), time.Hour)
	require.NoError(t, err)
	counter := sessionCountCommands(t, f)
	require.False(t, counter.timeIntercepted)
	sessionAdvanceFixture(t, f)
	require.NoError(t, f.repo.Renew(t.Context(), f.scope, pin.Lease, time.Hour))
	require.Greater(t, sessionReadState(t, f).DAGs[root.Ref.DAGID].Leases[pin.Lease.Token].Deadline, pin.Lease.Deadline)
	require.NoError(t, f.repo.Release(t.Context(), f.scope, pin.Lease))
	time.Sleep(3 * time.Millisecond)
	require.NoError(t, f.service.Collect(t.Context()))
	require.Empty(t, sessionReadState(t, f).DAGs)
	require.Greater(t, counter.snapshot().commands["time"], int64(0))
}

func BenchmarkAgentV3SessionServerTime(b *testing.B) {
	f := newSessionFixture(b, session.Options{})
	sessionServerClock(b, f)
	state := sessionPartitionFixture(b, f, "roots", 1000)
	pin, err := f.repo.ResolveAndPin(b.Context(), session.Selection{Scope: f.scope, Mode: session.SelectReply, ReplyMessageID: 101}, sessionRun(b), time.Hour)
	require.NoError(b, err)
	counter := sessionCountCommands(b, f)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		sessionAdvanceFixture(b, f)
		b.StartTimer()
		if err := f.repo.Renew(b.Context(), f.scope, pin.Lease, time.Hour); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	counter.snapshot().report(b)
	require.Len(b, state.DAGs, 1000)
}

func BenchmarkAgentV3SessionMaintenanceServerTime(b *testing.B) {
	benchmarkSessionMaintenance(b, sessionServerClock)
}
