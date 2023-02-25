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
		assert.Errorf(t, err, "EvalDuration(\"1\") should get error, but it's ok and result is %v", evalDuration)
	})
}

func TestReplaceSpace(t *testing.T) {
	t.Run("Empty String", func(t *testing.T) {
		out := ReplaceSpace("")
		assert.Equal(t, "", out)
	})

	t.Run("Not Replace", func(t *testing.T) {
		out := ReplaceSpace("abc_=123")
		assert.Equal(t, "abc_=123", out)
	})

	t.Run("Replace Newline", func(t *testing.T) {
		out := ReplaceSpace("abc\n123\n456")
		assert.Equal(t, `abc\n123\n456`, out)
	})

	t.Run("Replace Space", func(t *testing.T) {
		out := ReplaceSpace(`abc	123 456`)
		assert.Equal(t, `abc\t123 456`, out)
	})
}
