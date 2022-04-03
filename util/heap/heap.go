package heap

import "sort"

// CompareFunction is generic function for comparing two value.
type CompareFunction[T any] func(a, b T) bool

// Heap is a heap.
// Depending on `less` function implement, it can be min heap or max heap.
type Heap[T any] struct {
	d     []T
	less  CompareFunction[T]
	equal CompareFunction[T]
}

// NewHeap takes a slice and returns a heap not initialized.
// It don't take ownership of the slice.
func NewHeap[T any](ps []T, less, equal CompareFunction[T]) *Heap[T] {
	d := make([]T, len(ps))
	copy(d, ps)

	return &Heap[T]{d, less, equal}
}

// NewHeapInit takes a slice and returns a heap initialized.
// It don't take ownership of the slice.
func NewHeapInit[T any](ps []T, less, equal CompareFunction[T]) *Heap[T] {
	heap := NewHeap(ps, less, equal)
	heap.Init()
	return heap
}

// TakeAsHeap takes a slice and returns a heap not initialized.
// It **takes** ownership of the slice.
func TakeAsHeap[T any](ps []T, less, equal CompareFunction[T]) *Heap[T] {
	heap := Heap[T]{ps, less, equal}
	return &heap
}

// SortTopN sorts the top N elements of a slice on front, else in random order.
// It **takes** ownership of `ps`, and acts on origin slice.
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
	sort.Slice(maxHeap.d, maxHeap.lt)

	// sum of `len(headN)` and `len(rest)` equals `len(ps)`
	// `rest` is always on the base array of `ps`
	// but `minN` isn't
	// copy(ps[:n], minN)
	copy(ps[n:], rest)
	return ps
	// return append(minN, rest...)
}

// Init initializes the heap.
func (h *Heap[T]) Init() {
	if h.Len() <= 1 {
		return
	}

	for i := (h.Len() / 2) - 1; i >= 0; i-- {
		h.down(i)
	}
}

// Len returns the length of the heap.
func (h *Heap[T]) Len() int {
	return len(h.d)
}

// Swap swap two elements of the heap.
func (h *Heap[T]) Swap(i, j int) {
	h.d[i], h.d[j] = h.d[j], h.d[i]
}

// gt takes two index of element, and returns if the first one is larger than the other.
func (h *Heap[T]) gt(i, j int) bool {
	return !h.less(h.d[i], h.d[j]) && !h.equal(h.d[i], h.d[j])
}

// lt takes two index of element, and returns if the first one is less than the other.
func (h *Heap[T]) lt(i, j int) bool {
	return h.less(h.d[i], h.d[j])
}

// ltEq takes two index of element, and returns if the first one is less than or equal the other.
func (h *Heap[T]) ltEq(i, j int) bool {
	return h.less(h.d[i], h.d[j]) || h.equal(h.d[i], h.d[j])
}

// gtEq takes two index of element, and returns if the first one is larger than or equal the other.
func (h *Heap[T]) gtEq(i, j int) bool {
	return !h.less(h.d[i], h.d[j]) || h.equal(h.d[i], h.d[j])
}

// eq takes two index of element, and returns true they are equal.
func (h *Heap[T]) eq(i, j int) bool {
	return h.equal(h.d[i], h.d[j])
}

// Empty returns true if the heap is empty.
func (h *Heap[T]) Empty() bool {
	return h.Len() == 0
}

// IsHeap checks if the heap is a heap.
func (h *Heap[T]) IsHeap() bool {
	if h.Empty() {
		return true
	}

	for i := 0; i < h.Len()/2; i++ {
		left, right := 2*i+1, 2*i+2
		if left >= h.Len() {
			break
		}
		if h.gt(i, left) || right < h.Len() && h.gt(i, right) {
			return false
		}
	}
	return true
}

// min2 takes 2 element index, and return the minimum one.
func (h *Heap[T]) min2(i, j int) int {
	if h.ltEq(i, j) {
		return i
	}
	return j
}

// min3 takes 3 element index, and return the minimum one.
func (h *Heap[T]) min3(i, j, k int) int {
	less12 := h.min2(i, j)
	return h.min2(less12, k)
}

// Push pushes an element into the heap.
func (h *Heap[T]) Push(e T) {
	h.d = append(h.d, e)
	h.up(h.Len() - 1)
}

// Pop pops an element from the heap.
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

// Replace replaces the element at index `n` with `e`.
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

// Top returns the top element of the heap.
func (h *Heap[T]) Top() (top T) {
	if h.Empty() {
		return
	}
	return h.d[0]
}

// up moves the element at index `n` up to its proper position.
func (h *Heap[T]) up(i int) {
	// loop when `e` is not top and `e` > child
	for p := (i - 1) / 2; i > 0 && h.gt(p, i); p, i = (p-1)/2, p {
		h.Swap(p, i)
	}
}

// down moves the element at index `n` down to its proper position.
// Returns true if the element at `i` is not in the initial position.
func (h *Heap[T]) down(i int) bool {
	init := i

	for {
		left := i*2 + 1
		right := left + 1

		// `left` < 0 means it overflowed
		if left >= h.Len() || left < 0 {
			break
		}

		var min int
		// left cannot out of range
		if right >= h.Len() {
			min = h.min2(i, left)
		} else {
			min = h.min3(i, left, right)
		}

		// `min` == `i` will end process
		if min == i {
			break
		}
		h.Swap(i, min)
		i = min
	}

	return i != init
}
