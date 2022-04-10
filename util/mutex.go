package util

import "sync"

// Mutexed is a pack of value with mutex.
type Mutexed[T any] struct {
	sync.Mutex
	v T
}

func (m *Mutexed[T]) LockSet(value T) {
	m.Lock()
	m.v = value
	m.Unlock()
}

func (m *Mutexed[T]) LockGet() (ret T) {
	m.Lock()
	defer m.Unlock()
	ret = m.v
	return
}

func (m *Mutexed[T]) Set(value T) {
	m.v = value
}

func (m *Mutexed[T]) Get() (ret T) {
	return m.v
}

// RWMutexed is a pack of value with rwmutex.
type RWMutexed[T any] struct {
	sync.RWMutex
	v T
}

func (m *RWMutexed[T]) LockSet(value T) {
	m.Lock()
	m.v = value
	m.Unlock()
}

func (m *RWMutexed[T]) LockGet() (ret T) {
	m.RLock()
	defer m.RUnlock()
	ret = m.v
	return
}

func (m *RWMutexed[T]) Set(value T) {
	m.v = value
}

func (m *RWMutexed[T]) Get() (ret T) {
	return m.v
}
