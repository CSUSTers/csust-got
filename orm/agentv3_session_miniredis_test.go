//go:build !session_realredis

package orm

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type sessionMiniRedis struct{ *miniredis.Miniredis }

func newSessionTestRedis(t testing.TB) sessionTestRedis { return sessionMiniRedis{miniredis.RunT(t)} }
func (sessionMiniRedis) configureClient(*redis.Client)  {}
