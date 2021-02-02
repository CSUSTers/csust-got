package util

import (
	"math"
	"time"
)

var (
	cdMap = make(map[int64]int64)
)

// EvalDuration evaluates a duration expression like "2d1m" to time.Duration.
func EvalDuration(s string) (time.Duration, error) {
	return time.ParseDuration(s)
}

// GetBanCD evaluate cd with ban time
// cd = 0.8x^2/log(x) + 50x
func GetBanCD(d time.Duration) time.Duration {
	if cd, ok := cdMap[int64(d.Seconds())]; ok {
		return time.Duration(cd) * time.Second
	}
	sec := d.Seconds()
	cd := 0.8*sec*sec/math.Log10(sec) + 50*sec
	cdMap[int64(sec)] = int64(cd)
	return time.Duration(cd) * time.Second
}
