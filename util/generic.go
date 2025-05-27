package util

import "slices"

// SliceContains return if xs contains x
func SliceContains[T comparable](xs []T, x T) bool {
	return slices.Contains(xs, x)
}
