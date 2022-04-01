package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRange(t *testing.T) {
	// int range
	t.Run("int range", func(t *testing.T) {
		ass := assert.New(t)

		// [1, 5]
		r := NewRange(1, 5, ClosedInterval)
		r2 := NewClosedRange(1, 5)

		ass.Equal(r, r2)

		ass.True(r.Cover(3))
		ass.False(r.Cover(0))
		ass.False(r.Cover(6))

		ass.True(r.Cover(1))
		ass.True(r.Cover(5))
	})

	// string range
	t.Run("string range", func(t *testing.T) {
		ass := assert.New(t)

		// ["a", "zz"]
		r := NewRange("a", "zz", ClosedInterval)
		r2 := NewClosedRange("a", "zz")

		ass.Equal(r, r2)

		ass.False(r.Cover(""))
		ass.True(r.Cover("c"))
		ass.True(r.Cover("z1"))
		ass.True(r.Cover("z"))
		ass.False(r.Cover("zz1"))

		ass.True(r.Cover("a"))
		ass.True(r.Cover("zz"))
	})

	// float range
	t.Run("float range", func(t *testing.T) {
		ass := assert.New(t)

		// [1.0, 5.0]
		r := NewRange(1.0, 5.0, ClosedInterval)
		r2 := NewClosedRange(1.0, 5.0)

		ass.Equal(r, r2)

		ass.True(r.Cover(3.0))
		ass.False(r.Cover(0.0))
		ass.False(r.Cover(6.0))

		ass.True(r.Cover(1.0))
		ass.True(r.Cover(5.0))
	})

	// empty range
	t.Run("empty range", func(t *testing.T) {
		ass := assert.New(t)
		r1 := NewEmptyRange[int]()

		ass.True(r1.IsEmpty())
		ass.False(r1.Cover(1))
		ass.False(r1.Cover(0))
		ass.False(r1.Cover(-1))
		ass.False(r1.Cover(100))

		r2 := NewEmptyRange[string]()
		ass.True(r2.IsEmpty())
		ass.False(r2.Cover("a"))
		ass.False(r2.Cover(""))
		ass.False(r2.Cover("z1"))
		ass.False(r2.Cover(string([]byte{0xff, 0xfe, 0x00})))
	})

	// closed range
	t.Run("closed range", func(t *testing.T) {
		ass := assert.New(t)

		// [1, 1]
		r := NewRange(1, 1, ClosedInterval)
		r2 := NewClosedRange(1, 1)

		ass.Equal(r, r2)
		ass.False(r.IsEmpty())

		ass.True(r.Cover(1))
		ass.False(r.Cover(0))
		ass.False(r.Cover(2))

		ass.False(r.Cover(-1))
		ass.False(r.Cover(100))
	})

	// open range
	t.Run("open range", func(t *testing.T) {
		ass := assert.New(t)

		// (1, 1)
		r := NewRange(1, 1, OpenInterval)
		r2 := NewOpenRange(1, 1)

		ass.Equal(r, r2)
		ass.True(r.IsEmpty())

		ass.False(r.Cover(1))
		ass.False(r.Cover(0))
		ass.False(r.Cover(2))
		ass.False(r.Cover(-1))
		ass.False(r.Cover(100))

		// (1, 10)
		r = NewRange(1, 10, OpenInterval)
		r2 = NewOpenRange(1, 10)

		ass.Equal(r, r2)

		ass.False(r.Cover(-1))
		ass.False(r.Cover(0))
		ass.False(r.Cover(1))
		ass.True(r.Cover(2))
		ass.True(r.Cover(9))
		ass.False(r.Cover(10))
		ass.False(r.Cover(11))
	})

	// half open range
	t.Run("half open range", func(t *testing.T) {
		t.Run("left closed right open", func(t *testing.T) {
			ass := assert.New(t)

			// [1, 1)
			r := NewRange(1, 1, LClosedROpen)

			ass.Equal(leftClosed, r.(*Range[int]).t&leftClosed)

			ass.True(r.IsEmpty())
			ass.False(r.Cover(0))
			ass.False(r.Cover(1))
			ass.False(r.Cover(2))

			// [1, 1)
			r = NewRange(1, 1, LClosedROpen)

			ass.True(r.IsEmpty())
			ass.False(r.Cover(0))
			ass.False(r.Cover(1))
			ass.False(r.Cover(2))

			// [1, 2)
			r = NewRange(1, 2, LClosedROpen)

			ass.False(r.IsEmpty())
			ass.False(r.Cover(0))
			ass.True(r.Cover(1))
			ass.False(r.Cover(2))
			ass.False(r.Cover(3))

			// [1, 10)
			r = NewRange(1, 10, LClosedROpen)

			ass.False(r.IsEmpty())
			ass.False(r.Cover(0))
			ass.True(r.Cover(1))
			ass.True(r.Cover(2))
			ass.True(r.Cover(3))
			ass.True(r.Cover(4))
			ass.True(r.Cover(5))
			ass.True(r.Cover(6))
			ass.True(r.Cover(7))
			ass.True(r.Cover(8))
			ass.True(r.Cover(9))
			ass.False(r.Cover(10))
			ass.False(r.Cover(11))
		})

		t.Run("left open right closed", func(t *testing.T) {

			ass := assert.New(t)

			// (1, 1]
			r := NewRange(1, 1, LOpenRClosed)

			ass.Equal(rightClosed, r.(*Range[int]).t&rightClosed)

			ass.True(r.IsEmpty())
			ass.False(r.Cover(0))
			ass.False(r.Cover(1))
			ass.False(r.Cover(2))

			// (1, 2]
			r = NewRange(1, 2, LOpenRClosed)

			ass.False(r.IsEmpty())
			ass.False(r.Cover(0))
			ass.False(r.Cover(1))
			ass.True(r.Cover(2))
			ass.False(r.Cover(3))

			// (1, 5]
			r = NewRange(1, 5, LOpenRClosed)

			ass.False(r.IsEmpty())

			ass.False(r.Cover(-1))
			ass.False(r.Cover(0))
			ass.False(r.Cover(1))
			ass.True(r.Cover(2))
			ass.True(r.Cover(3))
			ass.True(r.Cover(4))
			ass.True(r.Cover(5))
			ass.False(r.Cover(6))
		})

	})
}
