package util

type UnsignedInt interface {
	~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

type SignedInt interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64
}

type Interger interface {
	UnsignedInt | SignedInt
}

type Float interface {
	~float32 | ~float64
}

type Complex interface {
	~complex64 | ~complex128
}

type Number interface {
	Interger | Float | Complex
}

type Ordered interface {
	Interger | Float | ~string
}
