package heap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHeap(t *testing.T) {
	ass := assert.New(t)

	d := []int{43, 56, 33, 55, 23, 44, 12, 34, 45, 67, 89, 90, 33}
	h := TakeAsHeap(d, func(a, b int) bool {
		return a < b
	}, func(a, b int) bool {
		return a == b
	})
	h.Init()
	ass.True(h.IsHeap())
	t.Log(h.d)
}
