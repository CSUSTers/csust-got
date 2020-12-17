package util

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestEvalDate(t *testing.T) {
	t.Run("Hour and Minute", func(t *testing.T) {
		duration, _ := EvalDuration("2h11m")
		assert.Equal(t, duration, 2*time.Hour+11*time.Minute)
	})

	t.Run("Plain Number Error", func(t *testing.T) {
		evalDuration, err := EvalDuration("1")

		if err == nil {
			t.Fatalf("Test case should get error, but (%v %v)", evalDuration, err)
		}
	})
}
