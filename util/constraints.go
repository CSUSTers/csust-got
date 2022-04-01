package util

// UnsignedInt is a collection of unsigned integer types.
type UnsignedInt interface {
	~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

// SignedInt is a collection of signed integer types.
type SignedInt interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64
}

// Interger is a collection of all integer types.
type Interger interface {
	UnsignedInt | SignedInt
}

// Float is a collection of float types.
type Float interface {
	~float32 | ~float64
}

// Complex is a collection of complex types.
type Complex interface {
	~complex64 | ~complex128
}

// Number is a collection of all number types.
type Number interface {
	Interger | Float | Complex
}

// Ordered is a collection of types can be ordered.
type Ordered interface {
	Interger | Float | ~string
}
