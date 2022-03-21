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

// NewRangeInt return a Range[Int] object.
func NewRangeInt[T Interger](min, max T) Range[T] {
	return Range[T]{
		max: max,
		min: min,
	}
}

// NewEmptyRangeInt return a empty Range[Int] object.
func NewEmptyRangeInt[T Interger]() Range[T] {
	return Range[T]{
		min: 0,
		max: 0,
	}
}

func NewRange[T Ordered](min, max T) Range[T] {
	return Range[T]{
		max: max,
		min: min,
	}
}
