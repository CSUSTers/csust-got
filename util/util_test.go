package util

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestEvalDate(t *testing.T) {
	duration, _ := EvalDuration("2h11m")
	assert.Equal(t, duration, 2*time.Hour+11*time.Minute)
	evalDuration, err := EvalDuration("1")
	fmt.Println(evalDuration, err)
}
