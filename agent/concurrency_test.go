package agentv3

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"csust-got/config"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestSlotLimiter(t *testing.T) {
	unlimited := newSlotLimiter(0)
	for range 50 {
		require.True(t, unlimited.tryAcquire())
	}
	assert.EqualValues(t, 50, unlimited.current())
	unlimited.release()
	assert.EqualValues(t, 49, unlimited.current())

	limited := newSlotLimiter(2)
	require.True(t, limited.tryAcquire())
	require.True(t, limited.tryAcquire())
	assert.False(t, limited.tryAcquire(), "third holder is rejected")
	assert.EqualValues(t, 2, limited.current())

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, limited.acquire(cancelled), context.Canceled)

	done := make(chan error, 1)
	go func() { done <- limited.acquire(t.Context()) }()
	select {
	case <-done:
		t.Fatal("acquire must block while the limiter is full")
	case <-time.After(30 * time.Millisecond):
	}
	limited.release()
	require.NoError(t, <-done)
	assert.EqualValues(t, 2, limited.current())

	var nilLimiter *slotLimiter
	require.NoError(t, nilLimiter.acquire(t.Context()))
	assert.True(t, nilLimiter.tryAcquire())
	nilLimiter.release()
}

func withConcurrencyConfig(t *testing.T, cfg config.AgentV3ConcurrencyConfig) {
	t.Helper()
	old := config.BotConfig
	t.Cleanup(func() { config.BotConfig = old })
	config.BotConfig = &config.Config{AgentV3: &config.AgentV3Config{Concurrency: cfg}}
}

func TestLimitersRebuildWhenCapacityChanges(t *testing.T) {
	withConcurrencyConfig(t, config.AgentV3ConcurrencyConfig{MaxRuns: 1, MaxModelCalls: 2})
	runs := limiters.run()
	assert.Equal(t, 1, runs.capacity)
	assert.Same(t, runs, limiters.run())
	assert.Equal(t, 2, limiters.model().capacity)

	config.BotConfig.AgentV3.Concurrency.MaxRuns = 3
	rebuilt := limiters.run()
	assert.NotSame(t, runs, rebuilt)
	assert.Equal(t, 3, rebuilt.capacity)

	config.BotConfig = nil
	assert.Equal(t, 0, limiters.run().capacity)
	assert.Equal(t, 0, limiters.model().capacity)
	assert.Equal(t, "当前任务太多，稍后再试。", configuredConcurrency().GetBusyMessage())
}

// concurrencyProbeModel tracks how many Stream calls overlap.
type concurrencyProbeModel struct {
	active atomic.Int64
	peak   atomic.Int64
	hold   time.Duration
}

func (m *concurrencyProbeModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	stream, err := m.Stream(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	return stream.Recv()
}

func (m *concurrencyProbeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	n := m.active.Add(1)
	for {
		peak := m.peak.Load()
		if n <= peak || m.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	time.Sleep(m.hold)
	m.active.Add(-1)
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
}

func (m *concurrencyProbeModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func TestModelCallLimitSerializesModelStreams(t *testing.T) {
	withConcurrencyConfig(t, config.AgentV3ConcurrencyConfig{MaxModelCalls: 1})
	mdl := &concurrencyProbeModel{hold: 15 * time.Millisecond}
	agent, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Name: "probe", Model: mdl, MaxSteps: 2})
	require.NoError(t, err)

	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			result, err := agent.Generate(t.Context(), []*schema.Message{schema.UserMessage("request")})
			assert.NoError(t, err)
			assert.Equal(t, "ok", result.Content)
		})
	}
	wg.Wait()
	assert.EqualValues(t, 1, mdl.peak.Load())
	_, inFlight := AgentConcurrencySnapshot()
	assert.Zero(t, inFlight)
}

func TestModelCallLimitWaitHonoursContext(t *testing.T) {
	withConcurrencyConfig(t, config.AgentV3ConcurrencyConfig{MaxModelCalls: 1})
	limiter := limiters.model()
	require.True(t, limiter.tryAcquire())
	defer limiter.release()

	mdl := &concurrencyProbeModel{}
	agent, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Name: "probe", Model: mdl, MaxSteps: 2})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err = agent.Generate(ctx, []*schema.Message{schema.UserMessage("request")})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Zero(t, mdl.peak.Load(), "the model is never called while the slot is held")
}

type replyCaptureContext struct {
	tb.Context
	mu      sync.Mutex
	replies []string
}

func (c *replyCaptureContext) Reply(what any, _ ...any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if text, ok := what.(string); ok {
		c.replies = append(c.replies, text)
	}
	return nil
}

func TestChatRejectsTurnWhenRunLimitIsFull(t *testing.T) {
	f := newAgentSessionFixture(t)
	config.BotConfig.AgentV3.Concurrency = config.AgentV3ConcurrencyConfig{MaxRuns: 1, BusyMessage: "busy, try later"}
	cfg := &config.AgentConfig{Name: "limited", ContextMode: "reply_chain"}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("unused", nil)}}}
	f.compile(t, cfg, mdl)

	held := limiters.run()
	require.True(t, held.tryAcquire())
	message := sessionMessage(100, 7, 0, "root input")
	tbCtx := &replyCaptureContext{Context: f.bot.NewContext(tb.Update{Message: message})}
	before := InflightTurns()
	require.NoError(t, Chat(tbCtx, cfg, nil))
	assert.Equal(t, []string{"busy, try later"}, tbCtx.replies)
	assert.Empty(t, mdl.capturedInputs(), "the rejected turn never builds context or calls the model")
	assert.Equal(t, before, InflightTurns())
	held.release()

	mdl2 := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("served", nil)}}}
	f.compile(t, cfg, mdl2)
	tbCtx = &replyCaptureContext{Context: f.bot.NewContext(tb.Update{Message: sessionMessage(200, 7, 0, "second input")})}
	require.NoError(t, Chat(tbCtx, cfg, nil))
	assert.Empty(t, tbCtx.replies)
	assert.Len(t, mdl2.capturedInputs(), 1)
	runs, _ := AgentConcurrencySnapshot()
	assert.Zero(t, runs)
}
