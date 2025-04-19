package util

import "sync"

// Mutexed is a pack of value with mutex.
type Mutexed[T any] struct {
	sync.Mutex
	v T
}

// LockSet locks, sets the value, and then unlocks.
func (m *Mutexed[T]) LockSet(value T) {
	m.Lock()
	m.v = value
	m.Unlock()
}

// LockGet locks, gets the value, and then unlocks.
// It returns the value stored.
func (m *Mutexed[T]) LockGet() (ret T) {
	m.Lock()
	defer m.Unlock()
	ret = m.v
	return
}

// Set sets the value without locking.
// This is not thread-safe unless the caller handles locking.
func (m *Mutexed[T]) Set(value T) {
	m.v = value
}

// Get gets the value without locking.
// This is not thread-safe unless the caller handles locking.
func (m *Mutexed[T]) Get() (ret T) {
	return m.v
}

// RWMutexed is a pack of value with rwmutex.
type RWMutexed[T any] struct {
	sync.RWMutex
	v T
}

// LockSet locks for writing, sets the value, and then unlocks.
func (m *RWMutexed[T]) LockSet(value T) {
	m.Lock()
	m.v = value
	m.Unlock()
}

// LockGet locks for reading, gets the value, and then unlocks.
// It returns the value stored.
func (m *RWMutexed[T]) LockGet() (ret T) {
	m.RLock()
	defer m.RUnlock()
	ret = m.v
	return
}

// Set sets the value without locking.
// This is not thread-safe unless the caller handles locking.
func (m *RWMutexed[T]) Set(value T) {
	m.v = value
}

// Get gets the value without locking.
// This is not thread-safe unless the caller handles locking.
func (m *RWMutexed[T]) Get() (ret T) {
	return m.v
}
