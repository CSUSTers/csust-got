package comap

import (
	"sync"
)

// Map is a concurrent map, alias a generic version of stdlib `sync.Map`
type Map[K comparable, V any] struct {
	m sync.Map
}

// New a map
func New[K comparable, V any]() *Map[K, V] {
	return &Map[K, V]{
		m: sync.Map{},
	}
}

// Get value of one key from map
func (m *Map[K, V]) Get(k K) (V, bool) {
	var ret V
	ok := false

	v, ok := m.m.Load(k)
	if ok {
		ret = v.(V)
		ok = true
	}

	return ret, ok
}

// Set a pair of `(key, value)` to map
func (m *Map[K, V]) Set(k K, v V) {
	m.m.Store(k, v)
}

// Delete a key from map
func (m *Map[K, V]) Delete(k K) {
	m.m.Delete(k)
}
