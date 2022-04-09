package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestI2A(t *testing.T) {
	t.Parallel()

	t.Run("default size", testIntUintToA)
	t.Run("8 bits ints", testI8U8ToA)
	t.Run("16 bits ints", testI16U16ToA)
	t.Run("32 bits ints", testI32U32ToA)
	t.Run("64 bits ints", testI64U64ToA)
}

func TestA2I(t *testing.T) {
	t.Parallel()

	t.Run("error string", func(t *testing.T) {
		t.Run("not number strings", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			es := []string{
				"",
				"!@#%^^&",
				"   123",
			}

			for _, s := range es {
				_, err := A2I[int8](s, 16)
				ass.Error(err)
				_, err = A2I[int16](s, 16)
				ass.Error(err)
				_, err = A2I[int32](s, 16)
				ass.Error(err)
				_, err = A2I[int64](s, 16)
				ass.Error(err)
				_, err = A2I[int](s, 16)
				ass.Error(err)

				_, err = A2I[uint8](s, 16)
				ass.Error(err)

				_, err = A2I[uint16](s, 16)
				ass.Error(err)

				_, err = A2I[uint32](s, 16)
				ass.Error(err)

				_, err = A2I[uint64](s, 16)
				ass.Error(err)

				_, err = A2I[uint](s, 16)
				ass.Error(err)
			}
		})

		t.Run("out of bitsize", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			n8 := []string{
				"ff",
				"7fff",
			}
			i8 := []int64{
				0xff,
				0x7fff,
			}

			n16 := []string{
				"ffffff",
				"fffffff",
			}
			i16 := []int64{
				0xffffff,
				0xfffffff,
			}

			n32 := []string{
				"ffffffffff",
				"ffffffffffff",
			}
			i32 := []int64{
				0xffffffffff,
				0xffffffffffff,
			}

			n64 := []string{
				"ffffffffffffffffffff",
				"fffffffffffffffffffffff",
			}

			for i, s := range n8 {
				_, err := A2I[int8](s, 16)
				ass.Error(err)

				n, err := A2I[int16](s, 16)
				ass.NoError(err)
				ass.EqualValues(i8[i], n)

				m, err := A2I[int32](s, 16)
				ass.NoError(err)
				ass.EqualValues(i8[i], m)

				o, err := A2I[int64](s, 16)
				ass.NoError(err)
				ass.EqualValues(i8[i], o)

				u, err := A2I[int](s, 16)
				ass.NoError(err)
				ass.EqualValues(i8[i], u)
			}

			for i, s := range n16 {
				_, err := A2I[int8](s, 16)
				ass.Error(err)

				_, err = A2I[int16](s, 16)
				ass.Error(err)

				n, err := A2I[int32](s, 16)
				ass.NoError(err)
				ass.EqualValues(i16[i], n)

				m, err := A2I[int64](s, 16)
				ass.NoError(err)
				ass.EqualValues(i16[i], m)

				o, err := A2I[int](s, 16)
				ass.NoError(err)
				ass.EqualValues(i16[i], o)
			}

			for i, s := range n32 {
				_, err := A2I[int8](s, 16)
				ass.Error(err)

				_, err = A2I[int16](s, 16)
				ass.Error(err)

				_, err = A2I[int32](s, 16)
				ass.Error(err)

				n, err := A2I[int64](s, 16)
				ass.NoError(err)
				ass.EqualValues(i32[i], n)
			}

			for _, s := range n64 {
				_, err := A2I[int8](s, 16)
				ass.Error(err)
				_, err = A2I[int16](s, 16)
				ass.Error(err)
				_, err = A2I[int32](s, 16)
				ass.Error(err)
				_, err = A2I[int64](s, 16)
				ass.Error(err)
				_, err = A2I[int](s, 16)
				ass.Error(err)
			}
		})
	})

	t.Run("wrong base", func(t *testing.T) {
		t.Parallel()
		ass := assert.New(t)

		_, err := A2I[int8]("0x1", 1)
		ass.Error(err)

		_, err = A2I[int8]("0x1", 64)
		ass.Error(err)
	})
}

func testIntUintToA(t *testing.T) {
	t.Parallel()
	t.Run("int", func(t *testing.T) {
		t.Parallel()

		t.Run("to dec", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Dec(0))
			ass.Equal("1", I2Dec(1))
			ass.Equal("-1", I2Dec(-1))
			ass.Equal("123", I2Dec(123))
		})

		t.Run("to hex", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Hex(0))
			ass.Equal("1", I2Hex(1))
			ass.Equal("-1", I2Hex(-1))
			ass.Equal("7b", I2Hex(123))
			ass.Equal("ffffffff", I2Hex(0xffffffff))
			ass.Equal("eeeeeeee", I2Hex(0xeeeeeeee))
			ass.Equal("7fffffffffffffff", I2Hex(0x7fffffffffffffff))
			ass.Equal("7eeeeeeeeeeeeeee", I2Hex(0x7eeeeeeeeeeeeeee))
			ass.Equal("-ffffffff", I2Hex(-0xffffffff))
			ass.Equal("-eeeeeeee", I2Hex(-0xeeeeeeee))
			ass.Equal("-7fffffffffffffff", I2Hex(-0x7fffffffffffffff))
			ass.Equal("-7eeeeeeeeeeeeeee", I2Hex(-0x7eeeeeeeeeeeeeee))
		})

		t.Run("to bin", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Bin(0))
			ass.Equal("1", I2Bin(1))
			ass.Equal("-1", I2Bin(-1))
			ass.Equal("1111011", I2Bin(123))
			ass.Equal("11111111111111111111111111111111", I2Bin(0xffffffff))
			ass.Equal("11101110111011101110111011101110", I2Bin(0xeeeeeeee))
			ass.Equal("111111111111111111111111111111111111111111111111111111111111111", I2Bin(0x7fffffffffffffff))
			ass.Equal("111111011101110111011101110111011101110111011101110111011101110", I2Bin(0x7eeeeeeeeeeeeeee))
			ass.Equal("-11111111111111111111111111111111", I2Bin(-0xffffffff))
			ass.Equal("-11101110111011101110111011101110", I2Bin(-0xeeeeeeee))
			ass.Equal("-111111111111111111111111111111111111111111111111111111111111111", I2Bin(-0x7fffffffffffffff))
			ass.Equal("-111111011101110111011101110111011101110111011101110111011101110", I2Bin(-0x7eeeeeeeeeeeeeee))
		})
	})

	t.Run("uint", func(t *testing.T) {
		t.Parallel()

		t.Run("to dec", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Dec[uint](0))
			ass.Equal("1", I2Dec[uint](1))
			ass.Equal("123", I2Dec[uint](123))
			ass.Equal("4294967295", I2Dec[uint](0xffffffff))
			ass.Equal("18446744073709551615", I2Dec[uint](0xffffffffffffffff))
		})

		t.Run("to hex", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Hex[uint](0))
			ass.Equal("1", I2Hex[uint](1))
			ass.Equal("7b", I2Hex[uint](123))
			ass.Equal("ffffffff", I2Hex[uint](0xffffffff))
			ass.Equal("eeeeeeee", I2Hex[uint](0xeeeeeeee))
			ass.Equal("7fffffffffffffff", I2Hex[uint](0x7fffffffffffffff))
			ass.Equal("7eeeeeeeeeeeeeee", I2Hex[uint](0x7eeeeeeeeeeeeeee))
			ass.Equal("ffffffffffffffff", I2Hex[uint](0xffffffffffffffff))
			ass.Equal("eeeeeeeeeeeeeeee", I2Hex[uint](0xeeeeeeeeeeeeeeee))
		})

		t.Run("to bin", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Bin[uint](0))
			ass.Equal("1", I2Bin[uint](1))
			ass.Equal("1111011", I2Bin[uint](123))
			ass.Equal("11111111111111111111111111111111", I2Bin[uint](0xffffffff))
			ass.Equal("11101110111011101110111011101110", I2Bin[uint](0xeeeeeeee))
			ass.Equal("111111111111111111111111111111111111111111111111111111111111111", I2Bin[uint](0x7fffffffffffffff))
			ass.Equal("111111011101110111011101110111011101110111011101110111011101110", I2Bin[uint](0x7eeeeeeeeeeeeeee))
			ass.Equal("1111111111111111111111111111111111111111111111111111111111111111", I2Bin[uint](0xffffffffffffffff))
			ass.Equal("1110111011101110111011101110111011101110111011101110111011101110", I2Bin[uint](0xeeeeeeeeeeeeeeee))
		})
	})
}

func testI8U8ToA(t *testing.T) {
	t.Parallel()
	t.Run("int8", func(t *testing.T) {
		t.Parallel()

		t.Run("to dec", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Dec[int8](0))
			ass.Equal("1", I2Dec[int8](1))
			ass.Equal("-1", I2Dec[int8](-1))
			ass.Equal("123", I2Dec[int8](123))
			ass.Equal("127", I2Dec[int8](127))
			ass.Equal("-127", I2Dec[int8](-127))
			ass.Equal("-128", I2Dec[int8](-128))
		})

		t.Run("to hex", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Hex[int8](0))
			ass.Equal("1", I2Hex[int8](1))
			ass.Equal("-1", I2Hex[int8](-1))
			ass.Equal("7b", I2Hex[int8](123))
			ass.Equal("7f", I2Hex[int8](0x7f))
			ass.Equal("7e", I2Hex[int8](0x7e))
			ass.Equal("-80", I2Hex[int8](-0x80))
			ass.Equal("-7f", I2Hex[int8](-0x7f))
			ass.Equal("-7e", I2Hex[int8](-0x7e))
		})

		t.Run("to bin", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Bin[int8](0))
			ass.Equal("1", I2Bin[int8](1))
			ass.Equal("-1", I2Bin[int8](-1))
			ass.Equal("1111011", I2Bin[int8](123))
		})
	})

	t.Run("uint8", func(t *testing.T) {
		t.Parallel()

		t.Run("to dec", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Dec[uint8](0))
			ass.Equal("1", I2Dec[uint8](1))
			ass.Equal("123", I2Dec[uint8](123))
			ass.Equal("127", I2Dec[uint8](0x7f))
			ass.Equal("255", I2Dec[uint8](0xff))
		})

		t.Run("to hex", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Hex[uint8](0))
			ass.Equal("1", I2Hex[uint8](1))
			ass.Equal("7b", I2Hex[uint8](123))
			ass.Equal("ff", I2Hex[uint8](0xff))
			ass.Equal("ee", I2Hex[uint8](0xee))
			ass.Equal("7f", I2Hex[uint8](0x7f))
			ass.Equal("7e", I2Hex[uint8](0x7e))
			ass.Equal("ff", I2Hex[uint8](0xff))
			ass.Equal("ee", I2Hex[uint8](0xee))
		})

		t.Run("to bin", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Bin[uint8](0))
			ass.Equal("1", I2Bin[uint8](1))
			ass.Equal("1111011", I2Bin[uint8](123))
		})
	})
}

func testI16U16ToA(t *testing.T) {
	t.Parallel()
	t.Run("int16", func(t *testing.T) {
		t.Parallel()

		t.Run("to dec", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Dec[int16](0))
			ass.Equal("1", I2Dec[int16](1))
			ass.Equal("-1", I2Dec[int16](-1))
			ass.Equal("123", I2Dec[int16](123))
			ass.Equal("32767", I2Dec[int16](0x7fff))
			ass.Equal("-32768", I2Dec[int16](-0x8000))
		})

		t.Run("to hex", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Hex[int16](0))
			ass.Equal("1", I2Hex[int16](1))
			ass.Equal("7b", I2Hex[int16](123))
			ass.Equal("7fff", I2Hex[int16](0x7fff))
			ass.Equal("7ffe", I2Hex[int16](0x7ffe))
			ass.Equal("-8000", I2Hex[int16](-0x8000))
			ass.Equal("-7fff", I2Hex[int16](-0x7fff))
			ass.Equal("-7ffe", I2Hex[int16](-0x7ffe))
		})

		t.Run("to bin", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Bin[int16](0))
			ass.Equal("1", I2Bin[int16](1))
			ass.Equal("1111011", I2Bin[int16](123))
			ass.Equal("111111111111111", I2Bin[int16](0x7fff))
			ass.Equal("-1000000000000000", I2Bin[int16](-0x8000))
		})
	})

	t.Run("uint16", func(t *testing.T) {
		t.Parallel()

		t.Run("to dec", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Dec[uint16](0))
			ass.Equal("1", I2Dec[uint16](1))
			ass.Equal("123", I2Dec[uint16](123))
			ass.Equal("32767", I2Dec[uint16](0x7fff))
			ass.Equal("65535", I2Dec[uint16](0xffff))
		})

		t.Run("to hex", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Hex[uint16](0))
			ass.Equal("1", I2Hex[uint16](1))
			ass.Equal("7b", I2Hex[uint16](123))
			ass.Equal("7fff", I2Hex[uint16](0x7fff))
			ass.Equal("ffff", I2Hex[uint16](0xffff))
			ass.Equal("7ffe", I2Hex[uint16](0x7ffe))
			ass.Equal("ffff", I2Hex[uint16](0xffff))
			ass.Equal("7ffe", I2Hex[uint16](0x7ffe))
		})

		t.Run("to bin", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Bin[uint16](0))
			ass.Equal("1", I2Bin[uint16](1))
			ass.Equal("1111011", I2Bin[uint16](123))
			ass.Equal("111111111111111", I2Bin[uint16](0x7fff))
			ass.Equal("1111111111111111", I2Bin[uint16](0xffff))
		})
	})
}

func testI32U32ToA(t *testing.T) {
	t.Parallel()
	t.Run("int32", func(t *testing.T) {
		t.Parallel()

		t.Run("to dec", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Dec[int32](0))
			ass.Equal("1", I2Dec[int32](1))
			ass.Equal("-1", I2Dec[int32](-1))
			ass.Equal("123", I2Dec[int32](123))
		})

		t.Run("to hex", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Hex[int32](0))
			ass.Equal("1", I2Hex[int32](1))
			ass.Equal("-1", I2Hex[int32](-1))
			ass.Equal("7b", I2Hex[int32](123))
			ass.Equal("fffffff", I2Hex[int32](0xfffffff))
			ass.Equal("eeeeeee", I2Hex[int32](0xeeeeeee))
			ass.Equal("7fffffff", I2Hex[int32](0x7fffffff))
			ass.Equal("7eeeeeee", I2Hex[int32](0x7eeeeeee))
			ass.Equal("-fffffff", I2Hex[int32](-0xfffffff))
			ass.Equal("-eeeeeee", I2Hex[int32](-0xeeeeeee))
			ass.Equal("-7ffffff", I2Hex[int32](-0x7ffffff))
			ass.Equal("-7eeeeee", I2Hex[int32](-0x7eeeeee))
		})

		t.Run("to bin", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Bin[int32](0))
			ass.Equal("1", I2Bin[int32](1))
			ass.Equal("-1", I2Bin[int32](-1))
			ass.Equal("1111011", I2Bin[int32](123))
			ass.Equal("1111111111111111111111111111", I2Bin[int32](0xfffffff))
			ass.Equal("1110111011101110111011101110", I2Bin[int32](0xeeeeeee))
			ass.Equal("1111111111111111111111111111111", I2Bin[int32](0x7fffffff))
			ass.Equal("1111110111011101110111011101110", I2Bin[int32](0x7eeeeeee))
			ass.Equal("-1111111111111111111111111111", I2Bin[int32](-0xfffffff))
			ass.Equal("-1110111011101110111011101110", I2Bin[int32](-0xeeeeeee))
			ass.Equal("-1111111111111111111111111111111", I2Bin[int32](-0x7fffffff))
			ass.Equal("-1111110111011101110111011101110", I2Bin[int32](-0x7eeeeeee))
		})
	})

	t.Run("uint32", func(t *testing.T) {
		t.Parallel()

		t.Run("to dec", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Dec[uint32](0))
			ass.Equal("1", I2Dec[uint32](1))
			ass.Equal("123", I2Dec[uint32](123))
			ass.Equal("4294967295", I2Dec[uint32](0xffffffff))
		})

		t.Run("to hex", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Hex[uint32](0))
			ass.Equal("1", I2Hex[uint32](1))
			ass.Equal("7b", I2Hex[uint32](123))
			ass.Equal("ffffffff", I2Hex[uint32](0xffffffff))
			ass.Equal("eeeeeeee", I2Hex[uint32](0xeeeeeeee))
		})

		t.Run("to bin", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Bin[uint32](0))
			ass.Equal("1", I2Bin[uint32](1))
			ass.Equal("1111011", I2Bin[uint32](123))
			ass.Equal("11111111111111111111111111111111", I2Bin[uint32](0xffffffff))
			ass.Equal("11101110111011101110111011101110", I2Bin[uint32](0xeeeeeeee))
		})
	})
}

func testI64U64ToA(t *testing.T) {
	t.Parallel()
	t.Run("int64", func(t *testing.T) {
		t.Parallel()

		t.Run("to dec", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Dec[int64](0))
			ass.Equal("1", I2Dec[int64](1))
			ass.Equal("-1", I2Dec[int64](-1))
			ass.Equal("123", I2Dec[int64](123))
		})

		t.Run("to hex", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Hex[int64](0))
			ass.Equal("1", I2Hex[int64](1))
			ass.Equal("-1", I2Hex[int64](-1))
			ass.Equal("7b", I2Hex[int64](123))
			ass.Equal("ffffffff", I2Hex[int64](0xffffffff))
			ass.Equal("eeeeeeee", I2Hex[int64](0xeeeeeeee))
			ass.Equal("7fffffffffffffff", I2Hex[int64](0x7fffffffffffffff))
			ass.Equal("7eeeeeeeeeeeeeee", I2Hex[int64](0x7eeeeeeeeeeeeeee))
			ass.Equal("-ffffffff", I2Hex[int64](-0xffffffff))
			ass.Equal("-eeeeeeee", I2Hex[int64](-0xeeeeeeee))
			ass.Equal("-7fffffffffffffff", I2Hex[int64](-0x7fffffffffffffff))
			ass.Equal("-7eeeeeeeeeeeeeee", I2Hex[int64](-0x7eeeeeeeeeeeeeee))
		})

		t.Run("to bin", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Bin[int64](0))
			ass.Equal("1", I2Bin[int64](1))
			ass.Equal("-1", I2Bin[int64](-1))
			ass.Equal("1111011", I2Bin[int64](123))
			ass.Equal("11111111111111111111111111111111", I2Bin[int64](0xffffffff))
			ass.Equal("11101110111011101110111011101110", I2Bin[int64](0xeeeeeeee))
			ass.Equal("111111111111111111111111111111111111111111111111111111111111111", I2Bin[int64](0x7fffffffffffffff))
			ass.Equal("111111011101110111011101110111011101110111011101110111011101110", I2Bin[int64](0x7eeeeeeeeeeeeeee))
			ass.Equal("-11111111111111111111111111111111", I2Bin[int64](-0xffffffff))
			ass.Equal("-11101110111011101110111011101110", I2Bin[int64](-0xeeeeeeee))
			ass.Equal("-111111111111111111111111111111111111111111111111111111111111111", I2Bin[int64](-0x7fffffffffffffff))
			ass.Equal("-111111011101110111011101110111011101110111011101110111011101110", I2Bin[int64](-0x7eeeeeeeeeeeeeee))
		})
	})

	t.Run("uint64", func(t *testing.T) {
		t.Parallel()

		t.Run("to dec", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Dec[uint64](0))
			ass.Equal("1", I2Dec[uint64](1))
			ass.Equal("123", I2Dec[uint64](123))
			ass.Equal("4294967295", I2Dec[uint64](0xffffffff))
			ass.Equal("18446744073709551615", I2Dec[uint64](0xffffffffffffffff))
		})

		t.Run("to hex", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Hex[uint64](0))
			ass.Equal("1", I2Hex[uint64](1))
			ass.Equal("7b", I2Hex[uint64](123))
			ass.Equal("ffffffff", I2Hex[uint64](0xffffffff))
			ass.Equal("eeeeeeee", I2Hex[uint64](0xeeeeeeee))
			ass.Equal("7fffffffffffffff", I2Hex[uint64](0x7fffffffffffffff))
			ass.Equal("7eeeeeeeeeeeeeee", I2Hex[uint64](0x7eeeeeeeeeeeeeee))
			ass.Equal("ffffffffffffffff", I2Hex[uint64](0xffffffffffffffff))
			ass.Equal("eeeeeeeeeeeeeeee", I2Hex[uint64](0xeeeeeeeeeeeeeeee))
		})

		t.Run("to bin", func(t *testing.T) {
			t.Parallel()
			ass := assert.New(t)

			ass.Equal("0", I2Bin[uint64](0))
			ass.Equal("1", I2Bin[uint64](1))
			ass.Equal("1111011", I2Bin[uint64](123))
			ass.Equal("11111111111111111111111111111111", I2Bin[uint64](0xffffffff))
			ass.Equal("11101110111011101110111011101110", I2Bin[uint64](0xeeeeeeee))
			ass.Equal("111111111111111111111111111111111111111111111111111111111111111", I2Bin[uint64](0x7fffffffffffffff))
			ass.Equal("111111011101110111011101110111011101110111011101110111011101110", I2Bin[uint64](0x7eeeeeeeeeeeeeee))
			ass.Equal("1111111111111111111111111111111111111111111111111111111111111111", I2Bin[uint64](0xffffffffffffffff))
			ass.Equal("1110111011101110111011101110111011101110111011101110111011101110", I2Bin[uint64](0xeeeeeeeeeeeeeeee))
		})
	})
}
