package util

// RangeInt is a int range
type RangeInt struct {
	max int
	min int
}

// Cover return if pattern `x` is in range
func (r RangeInt) Cover(x int) bool {
	return x > r.min && x < r.max
}

// IsEmpty tells if the range is available
func (r RangeInt) IsEmpty() bool {
	return r.max <= r.min
}

// NewRangeInt return a RangeInt object
func NewRangeInt(min, max int) RangeInt {
	return RangeInt{
		max: max,
		min: min,
	}
}

// NewEmptyRangeInt return a empty RangeInt object
func NewEmptyRangeInt() RangeInt {
	return RangeInt{
		min: 0,
		max: 0,
	}
}
