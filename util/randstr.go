package util

import (
	"math/rand"
	"time"
)

const (
	chars                = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890"
	defaultRandStrLength = 8
)

var (
	rs = NewRandStr()
)

type randstr struct {
	seedRunes []byte
	rnd       *rand.Rand
	length    int
}

// NewRandStr returns a randstr generator with default seed and length.
func NewRandStr() *randstr {
	return NewRandStrWithSeed(chars)
}

// NewRandStrWithSeed returns a randstr generator with given seed.
func NewRandStrWithSeed(seed string) *randstr {
	return &randstr{
		seedRunes: []byte(seed),
		rnd:       rand.New(rand.NewSource(time.Now().UnixNano())),
		length:    defaultRandStrLength,
	}
}

// NewRandStrWithLength returns a randstr generator with given length.
func NewRandStrWithLength(length int) *randstr {
	return &randstr{
		seedRunes: []byte(chars),
		rnd:       rand.New(rand.NewSource(time.Now().UnixNano())),
		length:    length,
	}
}

// NewRandStrWithSeedLength returns a randstr generator with given seed and length.
func NewRandStrWithSeedLength(seed string, length int) *randstr {
	return &randstr{
		seedRunes: []byte(seed),
		rnd:       rand.New(rand.NewSource(time.Now().UnixNano())),
		length:    length,
	}
}

// RandBytesLen returns a random bytes with given.
// if length == 0, it will return empty bytes; if length < 0, it will return default length bytes.
func RandBytesLen(length int) []byte {
	return rs.RandBytesLen(length)
}

// RandBytes returns a random bytes with default length.
func RandBytes() []byte {
	return rs.RandBytes()
}

// RandStr returns a random string with default length.
func RandStr() string {
	return rs.RandStr()
}

// RandStrLen returns a random string with given length, as well as RandBytesLen.
func RandStrLen(length int) string {
	return string(rs.RandBytesLen(length))
}

// RandBytes returns a random bytes with given.
// if length == 0, it will return empty bytes; if length < 0, it will return default length bytes.
func (r *randstr) RandBytesLen(length int) []byte {
	rnd := r.rnd

	// if length == 0, it will return empty bytes;
	// if length < 0, it will return default length bytes.
	if length == 0 {
		return []byte{}
	} else if r.length < 0 {
		length = defaultRandStrLength
	}

	// if rnd == nil, it will use a new Rand.
	if r.rnd == nil {
		r.rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	buf := make([]byte, length)
	for i := range buf {
		buf[i] = r.seedRunes[rnd.Intn(len(r.seedRunes))]
	}
	return buf
}

// RandBytes returns a random bytes.
// if length == 0, it will return empty bytes; if length < 0, it will return default length bytes.
func (r *randstr) RandBytes() []byte {
	return r.RandBytesLen(r.length)
}

// RandStr is a wrapper of RandBytes.
func (r *randstr) RandStr() string {
	return string(r.RandBytes())
}

// NewRandStr is a wrapper of NewRandStr.
func (r *randstr) RandStrLen(length int) string {
	return r.RandStrLen(length)
}
