package heap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOrderedHeap(t *testing.T) {
	ass := assert.New(t)

	d := []int{43, 56, 33, 55, 23, 44, 12, 34, 45, 67, 89, 90, 33}
	NewOrderHeap(d, false)
	ass.True(SliceIsMinheap(d))
	t.Log(d)
}
