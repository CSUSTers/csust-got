package util

import (
	"time"
)

// EvalDuration evaluates a duration expression like "2d1m" to time.Duration.
func EvalDuration(s string) (time.Duration, error) {
	return time.ParseDuration(s)
}

// GetBanCD evaluate cd with ban time
func GetBanCD(d time.Duration) time.Duration {
	return time.Duration(d.Seconds()*d.Seconds()/2) * time.Second
}
