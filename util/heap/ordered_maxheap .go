package heap

import "csust-got/util"

// InitMaxheap initializes the slice as heap.
func InitMaxheap[T util.Ordered](xs []T) {
	if len(xs) <= 1 {
		return
	}

	for i := (len(xs) / 2) - 1; i >= 0; i-- {
		maxheapDown(xs, i)
	}
}

// PushMaxheap pushes an element into the heap.
func PushMaxheap[T util.Ordered](xs []T, e T) []T {
	xs = append(xs, e)
	maxheapUp(xs, len(xs)-1)
	return xs
}

// PopMaxheap pops an element from the heap.
func PopMaxheap[T util.Ordered](xs []T) (xxs []T, popped T) {
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
	maxheapDown(xs, 0)
	return xs, popped
}

// SliceIsMaxheap checks if the heap is a heap.
func SliceIsMaxheap[T util.Ordered](xs []T) bool {
	if len(xs) <= 1 {
		return true
	}

	for i := range len(xs) / 2 {
		left, right := 2*i+1, 2*i+2
		if left >= len(xs) {
			break
		}
		if xs[i] < xs[left] || right < len(xs) && xs[i] < xs[right] {
			return false
		}
	}
	return true
}

// maxheapUp moves the element at index `n` maxheapUp to its proper position.
func maxheapUp[T util.Ordered](xs []T, i int) {
	// loop when `e` is not top and `e` < child
	for p := (i - 1) / 2; i > 0 && xs[i] > xs[p]; p, i = (p-1)/2, p {
		xs[p], xs[i] = xs[i], xs[p]
	}
}

// maxheapDown moves the element at index `n` maxheapDown to its proper position.
func maxheapDown[T util.Ordered](xs []T, i int) bool {
	init := i

	for {
		left := i*2 + 1
		right := left + 1

		// `left` < 0 means it overflowed
		if left >= len(xs) || left < 0 {
			break
		}

		var max int
		// left cannot out of range
		if right >= len(xs) {
			max = max2(xs, i, left)
		} else {
			max = max3(xs, i, left, right)
		}

		// `min` == `i` will end process
		if max == i {
			break
		}
		xs[i], xs[max] = xs[max], xs[i]
		i = max
	}

	return i != init
}

// max2 takes 2 element index, and return the maximum one.
func max2[T util.Ordered](xs []T, i, j int) int {
	if xs[i] <= xs[j] {
		return i
	}
	return j
}

// max3 takes 3 element index, and return the maximum one.
func max3[T util.Ordered](xs []T, i, j, k int) int {
	less12 := max2(xs, i, j)
	return max2(xs, less12, k)
}
