package heap

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
)

var (
	testLen = []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 126, 127, 128}
	maxInt  = []int{1, 10, 100, 1000, 100000}
	minInt  = make([]int, len(maxInt))
)

func init() {
	for i := range maxInt {
		minInt[i] = -maxInt[i]
	}
}

func TestHeap(t *testing.T) {
	ass := assert.New(t)

	d := []int{43, 56, 33, 55, 23, 44, 12, 34, 45, 67, 89, 90, 33}
	h := TakeAsHeap(d, func(a, b int) bool {
		return a < b
	}, func(a, b int) bool {
		return a == b
	})
	h.Init()
	ass.True(h.IsHeap())
	t.Log(h.d)
}

func TestNewHeapInit(t *testing.T) {
	t.Parallel()
	for i := 0; i < len(testLen); i++ { // for each testLen
		for j := 0; j < len(maxInt); j++ { // for each minInt ~ maxInt
			ti, tj := i, j
			tName := fmt.Sprintf("TestNewHeapInit len=%d, min=%d, max=%d", testLen[i], minInt[j], maxInt[j])
			t.Run(tName, func(t *testing.T) {
				t.Parallel()
				ass := assert.New(t)
				// run each test multiple times
				for k := 0; k < 2*testLen[ti]+1; k++ {
					d := make([]int, testLen[ti])
					for v := range d {
						d[v] = minInt[tj] + rand.Intn(maxInt[tj]-minInt[tj]+1)
					}
					// test NewHeapInit
					var h *Heap[int]
					ass.NotPanics(func() {
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
					for v := 0; v < 3; v++ {
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
	for i := 0; i < len(testLen); i++ { // for each testLen
		for j := 0; j < len(maxInt); j++ { // for each minInt ~ maxInt
			ti, tj := i, j
			tName := fmt.Sprintf("TestHeapPush len=%d, min=%d, max=%d", testLen[i], minInt[j], maxInt[j])
			t.Run(tName, func(t *testing.T) {
				t.Parallel()
				ass := assert.New(t)
				for k := 0; k < 20; k++ {
					d := make([]int, testLen[ti])
					for v := range d {
						d[v] = minInt[tj] + rand.Intn(maxInt[tj]-minInt[tj]+1)
					}
					// test NewHeapInit
					h := NewHeapInit(d, func(a, b int) bool {
						return a < b
					}, func(a, b int) bool {
						return a == b
					})
					// test push
					var ok bool
					for v := 0; v < testLen[ti]/2+1; v++ {
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
	for i := 0; i < len(testLen); i++ { // for each testLen
		for j := 0; j < len(maxInt); j++ { // for each minInt ~ maxInt
			ti, tj := i, j
			tName := fmt.Sprintf("TestHeapPop len=%d, min=%d, max=%d", testLen[i], minInt[j], maxInt[j])
			t.Run(tName, func(t *testing.T) {
				t.Parallel()
				ass := assert.New(t)
				for k := 0; k < 20; k++ {
					d := make([]int, testLen[ti])
					for v := range d {
						d[v] = minInt[tj] + rand.Intn(maxInt[tj]-minInt[tj]+1)
					}
					// test NewHeapInit
					h := NewHeapInit(d, func(a, b int) bool {
						return a < b
					}, func(a, b int) bool {
						return a == b
					})
					sort.Slice(d, func(i, j int) bool {
						return d[i] < d[j]
					})
					// test pop
					for _, v := range d {
						var u int
						ass.NotPanics(func() { u = h.Pop() })
						ass.Equal(v, u)
					}
					// reverse
					h = NewHeapInit(d, func(a, b int) bool {
						return a > b
					}, func(a, b int) bool {
						return a == b
					})
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
	for i := 0; i < len(testLen); i++ { // for each testLen
		for j := 0; j < len(maxInt); j++ { // for each minInt ~ maxInt
			ti, tj := i, j
			tName := fmt.Sprintf("TestHeapInitAndPushAndPop len=%d, min=%d, max=%d", testLen[i], minInt[j], maxInt[j])
			t.Run(tName, func(t *testing.T) {
				t.Parallel()
				ass := assert.New(t)
				for k := 0; k < 20; k++ {
					d := make([]int, testLen[ti])
					for v := range d {
						d[v] = minInt[tj] + rand.Intn(maxInt[tj]-minInt[tj]+1)
					}
					// test NewHeapInit
					h := NewHeapInit(d, func(a, b int) bool {
						return a < b
					}, func(a, b int) bool {
						return a == b
					})
					// test push
					var ok bool
					for v := 0; v < testLen[ti]+1; v++ {
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