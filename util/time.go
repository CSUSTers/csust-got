package util

import (
	"time"
)

// EvalDuration evaluates a duration expression like "2d1m" to time.Duration.
func EvalDuration(s string) (time.Duration, error) {
	return time.ParseDuration(s)
}
