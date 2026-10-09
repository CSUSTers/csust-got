package agentv3

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"csust-got/config"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestInflightTurnsWaitReturnsImmediatelyWhenIdle(t *testing.T) {
	turns := newInflightTurns()
	require.Zero(t, turns.count.Load())
	require.NoError(t, turns.wait(t.Context()))
}

func TestInflightTurnsWaitBlocksUntilAllEnd(t *testing.T) {
	turns := newInflightTurns()
	turns.begin()
	turns.begin()
	require.EqualValues(t, 2, turns.count.Load())

	done := make(chan error, 1)
	go func() { done <- turns.wait(t.Context()) }()

	turns.end()
	select {
	case err := <-done:
		t.Fatalf("wait returned early with %v while one turn still in flight", err)
	case <-time.After(50 * time.Millisecond):
	}

	turns.end()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("wait did not return after the last turn ended")
	}
	require.Zero(t, turns.count.Load())
}

func TestInflightTurnsWaitHonorsContext(t *testing.T) {
	turns := newInflightTurns()
	turns.begin()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, turns.wait(ctx), context.DeadlineExceeded)
	turns.end()
	require.NoError(t, turns.wait(t.Context()))
}

func TestInflightTurnsEndWithoutBeginIsNoop(t *testing.T) {
	turns := newInflightTurns()
	turns.end()
	require.Zero(t, turns.count.Load())
	turns.begin()
	turns.end()
	require.NoError(t, turns.wait(t.Context()))
}

func TestInflightTurnsExportedCounter(t *testing.T) {
	before := InflightTurns()
	require.True(t, BeginInflightTurn())
	require.Equal(t, before+1, InflightTurns())
	EndInflightTurn()
	require.Equal(t, before, InflightTurns())
}

func TestInflightTurnsClosedGateRejectsRegistration(t *testing.T) {
	turns := newInflightTurns()
	require.True(t, turns.begin())
	turns.close()
	require.False(t, turns.begin())
	require.EqualValues(t, 1, turns.count.Load())
	turns.end()
	require.Zero(t, turns.count.Load())
	require.NoError(t, turns.wait(t.Context()))
	require.False(t, turns.begin())
	require.Zero(t, turns.count.Load())
}

func TestBeginShutdownClosesGlobalIntake(t *testing.T) {
	old := inflight
	inflight = newInflightTurns()
	t.Cleanup(func() { inflight = old })

	BeginShutdown()
	require.False(t, BeginInflightTurn())
	require.Zero(t, InflightTurns())
	require.NoError(t, WaitInflight(t.Context()))
}

func TestInflightTurnsNoRegistrationEscapesShutdownDrain(t *testing.T) {
	for range 20 {
		turns := newInflightTurns()
		var inside, violations atomic.Int64
		var drained atomic.Bool
		var workers sync.WaitGroup
		start := make(chan struct{})
		for range 32 {
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				for turns.begin() {
					if drained.Load() {
						violations.Add(1)
					}
					inside.Add(1)
					time.Sleep(time.Microsecond)
					inside.Add(-1)
					turns.end()
				}
			}()
		}
		close(start)
		time.Sleep(time.Millisecond)
		turns.close()
		require.NoError(t, turns.wait(t.Context()))
		drained.Store(true)
		require.Zero(t, inside.Load())
		workers.Wait()
		require.Zero(t, violations.Load())
		require.Zero(t, inside.Load())
		require.Zero(t, turns.count.Load())
	}
}

func TestChatDropsTurnAfterShutdownBegins(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "draining", ContextMode: "reply_chain"}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("unused", nil)}}}
	f.compile(t, cfg, mdl)

	old := inflight
	inflight = newInflightTurns()
	t.Cleanup(func() { inflight = old })
	BeginShutdown()

	tbCtx := &replyCaptureContext{Context: f.bot.NewContext(tb.Update{Message: sessionMessage(100, 7, 0, "late input")})}
	require.NoError(t, Chat(tbCtx, cfg, nil))
	require.Empty(t, tbCtx.replies)
	require.Empty(t, mdl.capturedInputs())
	require.Zero(t, InflightTurns())
}
