package agentv3

import (
	"context"
	"sync"
	"sync/atomic"
)

type inflightTurns struct {
	mu    sync.Mutex
	count atomic.Int64
	idle  chan struct{}
}

var inflight = newInflightTurns()

func newInflightTurns() *inflightTurns {
	idle := make(chan struct{})
	close(idle)
	return &inflightTurns{idle: idle}
}

func (t *inflightTurns) begin() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.count.Add(1) == 1 {
		t.idle = make(chan struct{})
	}
}

func (t *inflightTurns) end() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.count.Load() == 0 {
		return
	}
	if t.count.Add(-1) == 0 {
		close(t.idle)
	}
}

func (t *inflightTurns) wait(ctx context.Context) error {
	t.mu.Lock()
	idle := t.idle
	t.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// BeginInflightTurn marks the start of a top-level interactive agent turn.
func BeginInflightTurn() {
	inflight.begin()
}

// EndInflightTurn marks the end of a turn started with BeginInflightTurn.
func EndInflightTurn() {
	inflight.end()
}

// InflightTurns returns the number of interactive agent turns still running.
func InflightTurns() int64 {
	return inflight.count.Load()
}

// WaitInflight blocks until no interactive turns are in flight or ctx is done.
func WaitInflight(ctx context.Context) error {
	return inflight.wait(ctx)
}
