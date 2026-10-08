package agentv3

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
	BeginInflightTurn()
	require.Equal(t, before+1, InflightTurns())
	EndInflightTurn()
	require.Equal(t, before, InflightTurns())
}
