package util

// IntervalType tags the type of interval/range.
type IntervalType uint8

const (
	leftClosed  IntervalType = 1 << iota // 0b0001
	rightClosed                          // 0b0010

	// OpenInterval means the range is open.
	OpenInterval IntervalType = 0

	// ClosedInterval means the range is closed.
	ClosedInterval = leftClosed | rightClosed // 0b0011

	// LOpenRClosed means the range is left open.
	LOpenRClosed = rightClosed // 0b0010

	// LClosedROpen means the range is right open.
	LClosedROpen = leftClosed // 0b0001
)

// IRange is interface of range.
type IRange[T Ordered] interface {
	// Cover return if pattern `x` is in range.
	Cover(x T) bool

	// IsEmpty tells if the range is available.
	IsEmpty() bool
}

// Range is a range.
type Range[T Ordered] struct {
	max T
	min T
	t   IntervalType
}

// Cover return if pattern `x` is in range.
func (r Range[T]) Cover(x T) bool {
	return (x > r.min || (r.t&leftClosed == leftClosed) && x == r.min) &&
		(x < r.max || (r.t&rightClosed == rightClosed) && x == r.max)
}

// IsEmpty tells if the range is available.
func (r Range[T]) IsEmpty() bool {
	return r.max <= r.min && (r.t != ClosedInterval || r.max != r.min)
}

type emptyRange[T Ordered] struct{}

// Cover always return false.
func (r emptyRange[T]) Cover(_ T) bool {
	return false
}

// IsEmpty always return true.
func (r emptyRange[T]) IsEmpty() bool {
	return true
}

// NewEmptyRange return an empty Range[T] object.
func NewEmptyRange[T Ordered]() IRange[T] {
	return new(emptyRange[T])
}

// NewRange returns a Range[T] object.
func NewRange[T Ordered](min, max T, t IntervalType) IRange[T] {
	return &Range[T]{
		max: max,
		min: min,
		t:   t,
	}
}

// NewOpenRange returns a Range[T] object with OpenInterval type.
func NewOpenRange[T Ordered](min, max T) IRange[T] {
	return NewRange(min, max, OpenInterval)
}

// NewClosedRange returns a Range[T] object with ClosedInterval type.
func NewClosedRange[T Ordered](min, max T) IRange[T] {
	return NewRange(min, max, ClosedInterval)
}
