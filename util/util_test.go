package util

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestEvalDate(t *testing.T) {
	zap.ReplaceGlobals(zaptest.NewLogger(t))
	t.Run("Hour and Minute", func(t *testing.T) {
		duration, _ := EvalDuration("2h11m")
		assert.Equal(t, duration, 2*time.Hour+11*time.Minute)
	})

	t.Run("Plain Number Error", func(t *testing.T) {
		evalDuration, err := EvalDuration("1")
		assert.Errorf(t, err, "EvalDuration(\"1\") should get error, but it's ok and result is %v", evalDuration)
	})
}
