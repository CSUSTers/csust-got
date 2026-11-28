package util

import (
	"math/rand"
	"sync"
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
	mu        sync.Mutex
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

// RandBytesLen returns a random bytes with given.
// if length == 0, it will return empty bytes; if length < 0, it will return default length bytes.
func (r *randstr) RandBytesLen(length int) []byte {
	if length == 0 || len(r.seedRunes) == 0 {
		return []byte{}
	}
	if length < 0 {
		length = defaultRandStrLength
	}

	r.mu.Lock()
	if r.rnd == nil {
		r.rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	buf := make([]byte, length)
	for i := range buf {
		buf[i] = r.seedRunes[r.rnd.Intn(len(r.seedRunes))]
	}
	r.mu.Unlock()
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

// RandStrLen is a wrapper of RandBytesLen.
func (r *randstr) RandStrLen(length int) string {
	return string(r.RandBytesLen(length))
}
