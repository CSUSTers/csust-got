package util

// Range is a range.
type Range[T Ordered] struct {
	max T
	min T
}

// Cover return if pattern `x` is in range.
func (r Range[T]) Cover(x T) bool {
	return x > r.min && x < r.max
}

// IsEmpty tells if the range is available.
func (r Range[T]) IsEmpty() bool {
	return r.max <= r.min
}

// NewEmptyRangeInt return a empty Range[Int] object.
func NewEmptyRange[T Ordered]() Range[T] {
	e := *new(T)
	return Range[T]{
		min: e,
		max: e,
	}
}

// NewRange returns a Range[T] object.
func NewRange[T Ordered](min, max T) Range[T] {
	return Range[T]{
		max: max,
		min: min,
	}
}
