package util

import "sync"

// Mutexed is a pack of value with mutex.
type Mutexed[T any] struct {
	mu sync.Mutex
	v  T
}

func (m *Mutexed[T]) Set(value T) {
	m.mu.Lock()
	m.v = value
	m.mu.Unlock()
}

func (m *Mutexed[T]) Get() (ret T) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ret = m.v
	return
}

// RWMutexed is a pack of value with rwmutex.
type RWMutexed[T any] struct {
	mu sync.RWMutex
	v  T
}

func (m *RWMutexed[T]) Set(value T) {
	m.mu.Lock()
	m.v = value
	m.mu.Unlock()
}

func (m *RWMutexed[T]) Get() (ret T) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ret = m.v
	return
}
