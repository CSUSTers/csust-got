package config

import (
	"go.uber.org/zap"
)

type redisConfig struct {
	RedisAddr string `koanf:"addr"`
	RedisPass string `koanf:"pass"`
	KeyPrefix string `koanf:"key_prefix"`
}

func (c *redisConfig) checkConfig() {
	if c.RedisAddr == "" {
		zap.L().Panic(noRedisMsg)
	}
}
