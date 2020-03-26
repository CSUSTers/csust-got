package context

import (
	"fmt"
	"github.com/golang/protobuf/ptypes/duration"
	"github.com/stretchr/testify/assert"
	"testing"
	"time"
)

var (
	context Context
)

func init() {
	context = Global(nil, nil)
}

func TestBasic(t *testing.T) {
	text := "2h"
	result, _ := context.EvalCEL(fmt.Sprintf("duration(\"%s\")", text))
	seconds := time.Duration(result.(*duration.Duration).GetSeconds())
	assert.Equal(t, seconds*time.Second, 2*time.Hour)
}
