package heap

import "sort"

type CompareFunction[T any] func(a, b T) bool

type Heap[T any] struct {
	d     []T
	less  CompareFunction[T]
	eqaul CompareFunction[T]
}

func NewHeap[T any](ps []T, less, equal CompareFunction[T]) *Heap[T] {
	d := make([]T, len(ps))
	copy(d, ps)

	return &Heap[T]{d, less, equal}
}

func NewHeapInit[T any](ps []T, less, equal CompareFunction[T]) *Heap[T] {
	heap := NewHeap(ps, less, equal)
	heap.Init()
	return heap
}

func TakeAsHeap[T any](ps []T, less, equal CompareFunction[T]) *Heap[T] {
	heap := Heap[T]{ps, less, equal}
	return &heap
}

// SortTopN taken ownership of `ps`, DOT NOT use where other
func SortTopN[T any](ps []T, n int, less, equal CompareFunction[T]) []T {
	larger := func(x, y T) bool {
		return !less(x, y) && !equal(x, y)
	}
	if n >= len(ps) {
		heap := TakeAsHeap(ps, less, equal)
		heap.Init()
		return heap.d
	}

	// Notice!!!
	// the slice `headN` and `rest` may share the base array
	// anytime `headN` grows, may cause the data of `rest` being contaminated
	headN, rest := ps[:n], ps[n:]
	maxHeap := TakeAsHeap(headN, larger, equal)
	maxHeap.Init()

	for i, p := range rest {
		if less(p, maxHeap.Top()) {
			// the top of `maxHeap` is the LARGEST one of min N
			// when `p.UseId` < top of `maxHeap`
			// means `p` is one of min N
			rest[i] = maxHeap.Replace(p, 0)
		}
	}

	// transfer the base array of `maxHeap` to a `MinPartnerHeap`
	minHeap := TakeAsHeap(maxHeap.d, less, equal)
	minHeap.Init()
	// `minN` is a slice own its NEW array instead of sharing array of `ps`
	sort.Slice(maxHeap.d, maxHeap.Less)

	// sum of `len(headN)` and `len(rest)` equals `len(ps)`
	// `rest` is always on the base array of `ps`
	// but `minN` isn't
	// copy(ps[:n], minN)
	copy(ps[n:], rest)
	return ps
	// return append(minN, rest...)
}

func (h *Heap[T]) Init() {
	if h.Len() <= 1 {
		return
	}

	for i := (h.Len() / 2) - 1; i >= 0; i-- {
		h.down(i)
	}
}

func (h *Heap[T]) Len() int {
	return len(h.d)
}

func (h *Heap[T]) Swap(i, j int) {
	h.d[i], h.d[j] = h.d[j], h.d[i]
}

func (h *Heap[T]) Larger(i, j int) bool {
	return !h.less(h.d[i], h.d[j]) && !h.eqaul(h.d[i], h.d[j])
}

func (h *Heap[T]) Less(i, j int) bool {
	return h.less(h.d[i], h.d[j])
}

func (h *Heap[T]) Empty() bool {
	return h.Len() == 0
}

func (h *Heap[T]) IsHeap() bool {
	if h.Empty() {
		return true
	}

	for i := 0; i < h.Len()/2; i++ {
		if h.Larger(i, 2*i+1) || h.Larger(i, 2*i+2) {
			return false
		}
	}
	return true
}

func (h *Heap[T]) Min(i, j, k int) int {
	var secondLess, thirdLess bool
	if j < h.Len() {
		secondLess = h.Less(j, i)
	}
	if k < h.Len() {
		thirdLess = h.Less(k, i)
	}

	// `j` and `k` Large than `i`
	//   `j` Large `k` => `j`
	//   _ 	      => `k`
	// `which` is Large than `i` => `which`
	// _ => `i`
	if secondLess && thirdLess {
		if h.Less(j, k) {
			return j
		} else {
			return k
		}
	} else if secondLess {
		return j
	} else if thirdLess {
		return k
	}
	return i
}

func (h *Heap[T]) Push(e T) {
	h.d = append(h.d, e)
	h.up(h.Len() - 1)
}

func (h *Heap[T]) Pop() (popped T) {
	if h.Empty() {
		return
	}
	if h.Len() == 1 {
		popped = h.d[0]
		h.d = h.d[:0]
		return popped
	}

	popped = h.d[0]
	h.d[0] = h.d[h.Len()-1]
	h.d = h.d[:h.Len()-1]
	h.down(0)
	return popped
}

func (h *Heap[T]) Replace(e T, n int) (replaced T) {
	if n >= h.Len() {
		return
	}

	replaced = h.d[n]
	h.d[n] = e
	if !h.down(n) {
		h.up(n)
	}
	return replaced
}

func (h *Heap[T]) Top() (top T) {
	if h.Empty() {
		return
	}
	return h.d[0]
}

func (h *Heap[T]) up(i int) {
	// loop when `e` is not top and `e` > child
	for p := (i - 1) / 2; i > 0 && h.Larger(p, i); p, i = (p-1)/2, p {
		h.Swap(p, i)
	}
}

func (h *Heap[T]) down(i int) bool {
	init := i

	for {
		left := i*2 + 1
		right := left + 1

		// `left` < 0 means it overflowed
		if left >= h.Len() || left < 0 {
			break
		}

		min := h.Min(i, left, right)
		// `min` == `i` will end process
		if min == i {
			break
		}
		h.Swap(i, min)
		i = min
	}

	return i != init
}
