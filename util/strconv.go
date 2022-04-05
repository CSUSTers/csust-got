package util

import (
	"reflect"
	"strconv"
	"unsafe"
)

// I2Dec converts an integer to decimal string.
func I2Dec[T Interger](i T) string {
	return I2A(i, 10)
}

// I2Hex converts an integer to hex string.
func I2Hex[T Interger](i T) string {
	return I2A(i, 16)
}

// I2Bin converts an integer to binary string.
func I2Bin[T Interger](i T) string {
	return I2A(i, 2)
}

// I2A converts an integer to string with given base.
func I2A[T Interger](i T, base int) string {
	switch reflect.TypeOf(i).Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(int64(i), base)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(uint64(i), base)
	default:
		panic("unreachable")
	}
	// return strconv.FormatInt(int64(i), base)
}

// A2I converts a string to integer.
// if `base` is set to `0`, the base will be automatically detected,
// and the string needs to prefix with '0b', '0x', '0o', etc.
func A2I[T Interger](a string, base int) (T, error) {
	bs := bitsize[T]()

	switch reflect.TypeOf(T(0)).Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(a, base, bs)
		return T(n), err
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(a, base, bs)
		return T(n), err
	default:
		panic("unreachable")
	}
	// n, err := strconv.ParseInt(a, base, bs)
	// return T(n), err
}

func bitsize[T Number]() int {
	return int(unsafe.Sizeof(T(0))) * 8
}
