package util

// SliceContains return if xs contains x
func SliceContains[T comparable](xs []T, x T) bool {
	for _, t := range xs {
		if t == x {
			return true
		}
	}
	return false
}
