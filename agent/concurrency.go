package agentv3

import (
	"context"
	"sync"
	"sync/atomic"

	"csust-got/config"
)

// slotLimiter bounds concurrent holders of a resource; a zero capacity means unlimited.
type slotLimiter struct {
	capacity int
	slots    chan struct{}
	inUse    atomic.Int64
}

func newSlotLimiter(capacity int) *slotLimiter {
	l := &slotLimiter{capacity: max(capacity, 0)}
	if l.capacity > 0 {
		l.slots = make(chan struct{}, l.capacity)
	}
	return l
}

func (l *slotLimiter) acquire(ctx context.Context) error {
	if l == nil || l.slots == nil {
		if l != nil {
			l.inUse.Add(1)
		}
		return nil
	}
	select {
	case l.slots <- struct{}{}:
		l.inUse.Add(1)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *slotLimiter) tryAcquire() bool {
	if l == nil || l.slots == nil {
		if l != nil {
			l.inUse.Add(1)
		}
		return true
	}
	select {
	case l.slots <- struct{}{}:
		l.inUse.Add(1)
		return true
	default:
		return false
	}
}

func (l *slotLimiter) release() {
	if l == nil {
		return
	}
	l.inUse.Add(-1)
	if l.slots != nil {
		<-l.slots
	}
}

func (l *slotLimiter) current() int64 {
	if l == nil {
		return 0
	}
	return l.inUse.Load()
}

type agentLimiters struct {
	mu     sync.Mutex
	runs   *slotLimiter
	models *slotLimiter
}

var limiters agentLimiters

func configuredConcurrency() config.AgentV3ConcurrencyConfig {
	if config.BotConfig == nil || config.BotConfig.AgentV3 == nil {
		return config.AgentV3ConcurrencyConfig{}
	}
	return config.BotConfig.AgentV3.Concurrency
}

// Limiters are rebuilt when their configured capacity changes; holders release into the limiter they acquired from.
func (s *agentLimiters) run() *slotLimiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := max(configuredConcurrency().MaxRuns, 0)
	if s.runs == nil || s.runs.capacity != want {
		s.runs = newSlotLimiter(want)
	}
	return s.runs
}

func (s *agentLimiters) model() *slotLimiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := max(configuredConcurrency().MaxModelCalls, 0)
	if s.models == nil || s.models.capacity != want {
		s.models = newSlotLimiter(want)
	}
	return s.models
}

// AgentConcurrencySnapshot reports in-flight agent runs and model calls for traces and diagnostics.
func AgentConcurrencySnapshot() (runs, modelCalls int64) {
	limiters.mu.Lock()
	defer limiters.mu.Unlock()
	return limiters.runs.current(), limiters.models.current()
}
