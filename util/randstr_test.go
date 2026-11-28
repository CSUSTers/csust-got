package util

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRandBytesLenHandlesNegativeAndLazyInit(t *testing.T) {
	r := &randstr{seedRunes: []byte("abc"), length: 4}
	require.Len(t, r.RandBytesLen(-1), defaultRandStrLength)
	require.Empty(t, r.RandBytesLen(0))
	require.Len(t, r.RandBytes(), 4)

	emptySeed := &randstr{}
	require.Empty(t, emptySeed.RandBytesLen(4))
}

func TestRandStrConcurrentAccess(t *testing.T) {
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				require.Len(t, RandStr(), defaultRandStrLength)
			}
		}()
	}
	wg.Wait()
}
