package heap

import "csust-got/util"

// OrderedHeap is heap-utils for []Ordered.
type OrderedHeap[T util.Ordered] struct {
	Push func(x T)
	Pop  func() T
	Top  func() T
}

// NewOrderHeap returns heap-utils for taken slice.
func NewOrderHeap[T util.Ordered](xs []T, maxHeap bool) *OrderedHeap[T] {
	heap := &OrderedHeap[T]{
		Top: func() T {
			return TopHeap(xs)
		},
	}

	if maxHeap {
		InitMaxheap(xs)
		heap.Push = func(x T) {
			xs = PushMaxheap(xs, x)
		}
		heap.Pop = func() T {
			xxs, popped := PopMaxheap(xs)
			xs = xxs
			return popped
		}
	} else {
		InitMinheap(xs)
		heap.Push = func(x T) {
			xs = PushMinheap(xs, x)
		}
		heap.Pop = func() T {
			xxs, popped := PopMinheap(xs)
			xs = xxs
			return popped
		}
	}

	return heap
}

// InitMinheap initializes the slice as heap.
func InitMinheap[T util.Ordered](xs []T) {
	if len(xs) <= 1 {
		return
	}

	for i := (len(xs) / 2) - 1; i >= 0; i-- {
		minheapDown(xs, i)
	}
}

// PushMinheap pushes an element into the heap.
func PushMinheap[T util.Ordered](xs []T, e T) []T {
	xs = append(xs, e)
	minheapUp(xs, len(xs)-1)
	return xs
}

// PopMinheap pops an element from the heap.
func PopMinheap[T util.Ordered](xs []T) (xxs []T, popped T) {
	if len(xs) == 0 {
		xxs = xs
		return
	}
	if len(xs) == 1 {
		popped = xs[0]
		xs = xs[:0]
		return xs, popped
	}

	popped = xs[0]
	xs[0] = xs[len(xs)-1]
	xs = xs[:len(xs)-1]
	minheapDown(xs, 0)
	return xs, popped
}

// TopHeap returns the top element of the heap.
func TopHeap[T util.Ordered](xs []T) (top T) {
	if len(xs) == 0 {
		return
	}
	return xs[0]
}

// SliceIsMinheap checks if the heap is a heap.
func SliceIsMinheap[T util.Ordered](xs []T) bool {
	if len(xs) <= 1 {
		return true
	}

	for i := 0; i < len(xs)/2; i++ {
		left, right := 2*i+1, 2*i+2
		if left >= len(xs) {
			break
		}
		if xs[i] > xs[left] || right < len(xs) && xs[i] > xs[right] {
			return false
		}
	}
	return true
}

// minheapUp moves the element at index `n` minheapUp to its proper position.
func minheapUp[T util.Ordered](xs []T, i int) {
	// loop when `e` is not top and `e` > child
	for p := (i - 1) / 2; i > 0 && xs[i] < xs[p]; p, i = (p-1)/2, p {
		xs[p], xs[i] = xs[i], xs[p]
	}
}

// minheapDown moves the element at index `n` minheapDown to its proper position.
func minheapDown[T util.Ordered](xs []T, i int) bool {
	init := i

	for {
		left := i*2 + 1
		right := left + 1

		// `left` < 0 means it overflowed
		if left >= len(xs) || left < 0 {
			break
		}

		var min int
		// left cannot out of range
		if right >= len(xs) {
			min = min2(xs, i, left)
		} else {
			min = min3(xs, i, left, right)
		}

		// `min` == `i` will end process
		if min == i {
			break
		}
		xs[i], xs[min] = xs[min], xs[i]
		i = min
	}

	return i != init
}

// min2 takes 2 element index, and return the minimum one.
func min2[T util.Ordered](xs []T, i, j int) int {
	if xs[i] <= xs[j] {
		return i
	}
	return j
}

// min3 takes 3 element index, and return the minimum one.
func min3[T util.Ordered](xs []T, i, j, k int) int {
	less12 := min2(xs, i, j)
	return min2(xs, less12, k)
}
