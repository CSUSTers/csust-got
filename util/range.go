package util

// RangeInt is a int range.
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

// NewRangeInt return a RangeInt object.
func NewRangeInt(min, max int) Range[int] {
	return Range[int]{
		max: max,
		min: min,
	}
}

// NewEmptyRangeInt return a empty RangeInt object.
func NewEmptyRangeInt() Range[int] {
	return Range[int]{
		min: 0,
		max: 0,
	}
}
