package heap

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"slices"
)

const (
	// benchmark 10K
	b10K = 10_000
	// benchmark 10M
	b10M = 10_000_000
	// benchmark min and max value
	bMin, bMax = -1_000_000, 1_000_001
	// benchmark max pop and push
	bmp10K = 10_000
)

var (
	testLen = []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 126, 127, 128}
	maxInt  = []int{1, 10, 100, 1000, 100000}
	minInt  = make([]int, len(maxInt))

	// slice for benchmark len 10M
	s10M = make([]int, b10M)
	// slice for benchmark len 10K
	s10K = s10M[:b10K]

	randNum chan int
)

func TestMain(m *testing.M) {
	for i := range maxInt {
		minInt[i] = -maxInt[i]
	}

	randNum = make(chan int, 1024)
	go func() {
		for {
			randNum <- randInt(bMin, bMax)
		}
	}()

	m.Run()
}

func TestHeap(t *testing.T) {
	ass := assert.New(t)

	d := []int{43, 56, 33, 55, 23, 44, 12, 34, 45, 67, 89, 90, 33}
	h := TakeAsHeap(d, funLess, funEqual)
	h.Init()
	ass.True(h.IsHeap())
	t.Log(h.d)
}

func TestNewHeapInit(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skip heavy test in short mode")
	}

	for i := range testLen { // for each testLen
		for j := range maxInt { // for each minInt ~ maxInt
			ti, tj := i, j
			tName := fmt.Sprintf("TestNewHeapInit len=%d, min=%d, max=%d", testLen[i], minInt[j], maxInt[j])
			t.Run(tName, func(t *testing.T) {
				t.Parallel()
				ass := assert.New(t)
				// run each test multiple times
				for range 2*testLen[ti] + 1 {
					d := make([]int, testLen[ti])
					for v := range d {
						d[v] = minInt[tj] + rand.Intn(maxInt[tj]-minInt[tj]+1)
					}
					// test NewHeapInit
					var h *Heap[int]
					require.NotPanics(t, func() {
						h = NewHeapInit(d, func(a, b int) bool {
							return a < b
						}, func(a, b int) bool {
							return a == b
						})
					})
					// test IsHeap after init
					var ok bool
					ass.NotPanics(func() { ok = h.IsHeap() })
					ass.True(ok)
					ass.Equal(len(d), h.Len())
					// test Init can be called multiple times
					for range 3 {
						ass.NotPanics(func() { h.Init() })
						ass.NotPanics(func() { ok = h.IsHeap() })
						ass.True(ok)
						ass.Equal(len(d), h.Len())
					}
				}
			})
		}
	}
}

func TestHeapPush(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skip heavy test in short mode")
	}

	for i := range testLen { // for each testLen
		for j := range maxInt { // for each minInt ~ maxInt
			ti, tj := i, j
			tName := fmt.Sprintf("TestHeapPush len=%d, min=%d, max=%d", testLen[i], minInt[j], maxInt[j])
			t.Run(tName, func(t *testing.T) {
				t.Parallel()
				ass := assert.New(t)
				for range 20 {
					d := make([]int, testLen[ti])
					for v := range d {
						d[v] = minInt[tj] + rand.Intn(maxInt[tj]-minInt[tj]+1)
					}
					// test NewHeapInit
					h := NewHeapInit(d, funLess, funEqual)
					// test push
					var ok bool
					for v := range testLen[ti]/2 + 1 {
						// push random value
						rd := minInt[tj] + rand.Intn(maxInt[tj]-minInt[tj]+1)
						ass.NotPanics(func() { h.Push(rd) })
						ass.NotPanics(func() { ok = h.IsHeap() })
						ass.True(ok)
						// push max value
						ass.NotPanics(func() { h.Push(maxInt[tj] + v*2) })
						ass.NotPanics(func() { ok = h.IsHeap() })
						ass.True(ok)
						// push second max value
						ass.NotPanics(func() { h.Push(maxInt[tj] + v*2 - 1) })
						ass.NotPanics(func() { ok = h.IsHeap() })
						ass.True(ok)
						// push min value
						ass.NotPanics(func() { h.Push(minInt[tj] - v*2) })
						ass.NotPanics(func() { ok = h.IsHeap() })
						ass.True(ok)
						// push second min value
						ass.NotPanics(func() { h.Push(minInt[tj] - v*2 + 1) })
						ass.NotPanics(func() { ok = h.IsHeap() })
						ass.True(ok)
						// push max value once again
						ass.NotPanics(func() { h.Push(maxInt[tj] + v*2) })
						ass.NotPanics(func() { ok = h.IsHeap() })
						ass.True(ok)
						// push min value once again
						ass.NotPanics(func() { h.Push(minInt[tj] - v*2) })
						ass.NotPanics(func() { ok = h.IsHeap() })
						ass.True(ok)
					}
					ass.Equal(len(d)+(testLen[ti]/2+1)*7, h.Len())
				}
			})
		}
	}
}

func TestHeapPop(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skip heavy test in short mode")
	}

	for i := range testLen { // for each testLen
		for j := range maxInt { // for each minInt ~ maxInt
			ti, tj := i, j
			tName := fmt.Sprintf("TestHeapPop len=%d, min=%d, max=%d", testLen[i], minInt[j], maxInt[j])
			t.Run(tName, func(t *testing.T) {
				t.Parallel()
				ass := assert.New(t)
				for range 20 {
					d := make([]int, testLen[ti])
					for v := range d {
						d[v] = minInt[tj] + rand.Intn(maxInt[tj]-minInt[tj]+1)
					}
					// test NewHeapInit
					h := NewHeapInit(d, funLess, funEqual)
					slices.Sort(d)
					// test pop
					for _, v := range d {
						var u int
						ass.NotPanics(func() { u = h.Pop() })
						ass.Equal(v, u)
					}
					// reverse
					h = NewHeapInit(d, func(a, b int) bool {
						return a > b
					}, funEqual)
					sort.Slice(d, func(i, j int) bool {
						return d[i] > d[j]
					})
					// test pop
					var u int
					for _, v := range d {
						ass.NotPanics(func() { u = h.Pop() })
						ass.Equal(v, u)
					}
					// empty heap
					ass.Equal(0, h.Len())
					ass.NotPanics(func() { u = h.Pop() })
					ass.Equal(0, u)
				}
			})
		}
	}
}

func TestHeapInitAndPushAndPop(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skip heavy test in short mode")
	}

	for i := range testLen { // for each testLen
		for j := range maxInt { // for each minInt ~ maxInt
			ti, tj := i, j
			tName := fmt.Sprintf("TestHeapInitAndPushAndPop len=%d, min=%d, max=%d", testLen[i], minInt[j], maxInt[j])
			t.Run(tName, func(t *testing.T) {
				t.Parallel()
				ass := assert.New(t)
				for range 20 {
					d := make([]int, testLen[ti])
					for v := range d {
						d[v] = minInt[tj] + rand.Intn(maxInt[tj]-minInt[tj]+1)
					}
					// test NewHeapInit
					h := NewHeapInit(d, funLess, funEqual)
					// test push
					var ok bool
					for range testLen[ti] + 1 {
						// push random value
						rd := minInt[tj] + rand.Intn(maxInt[tj]-minInt[tj]+1)
						ass.NotPanics(func() { h.Push(rd) })
						ass.NotPanics(func() { ok = h.IsHeap() })
						ass.True(ok)
						// pop
						ass.NotPanics(func() { h.Pop() })
						ass.NotPanics(func() { ok = h.IsHeap() })
						ass.True(ok)
					}
					mi, u := minInt[tj]-1, 0
					for h.Len() > 0 {
						ass.NotPanics(func() { u = h.Pop() })
						ass.LessOrEqual(mi, u)
						mi = u
					}
				}
			})
		}
	}
}

func BenchmarkHeapInit10K(b *testing.B) {
	heap := TakeAsHeap(s10K, funLess, funEqual)

	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		randSlice(s10K, bMin, bMax)
		b.StartTimer()

		heap.Init()
	}
}

func BenchmarkHeapInit10M(b *testing.B) {
	heap := TakeAsHeap(s10M, funLess, funEqual)

	for b.Loop() {
		b.StopTimer()
		randSlice(s10M, bMin, bMax)
		b.StartTimer()

		heap.Init()
	}
}

func BenchmarkHeapPopPush10K(b *testing.B) {
	randSlice(s10K, bMin, bMax)
	heap := TakeAsHeap(s10K, funLess, funEqual)
	heap.Init()

	b.ResetTimer()
	for range b.N {
		heap.Pop()
		heap.Push(<-randNum)
	}
}

func BenchmarkHeapPopPush10M(b *testing.B) {
	randSlice(s10M, bMin, bMax)
	heap := TakeAsHeap(s10M, funLess, funEqual)
	heap.Init()

	b.ResetTimer()
	for range b.N {
		heap.Pop()
		heap.Push(<-randNum)
	}
}

func BenchmarkHeapPop10M(b *testing.B) {
	randSlice(s10M, bMin, bMax)
	heap := TakeAsHeap(s10M, funLess, funEqual)
	heap.Init()

	b.ResetTimer()
	for i := 0; i < b.N && i < bmp10K; i++ {
		heap.Pop()
	}

	//nolint:staticcheck // set `b.N` is necessary
	b.N = bmp10K
}

func funLess(a, b int) bool {
	return a < b
}

func funEqual(a, b int) bool {
	return a == b
}

func randSlice(xs []int, min, max int) {
	for i := range xs {
		xs[i] = <-randNum
	}
}

// func shuffSlice(xs []int) {
// 	for i := range xs {
// 		j := randInt(i, len(xs))
// 		xs[i], xs[j] = xs[j], xs[i]
// 	}
// }

func randInt(min, max int) int {
	return min + rand.Intn(max-min)
}
