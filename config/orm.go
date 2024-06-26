package config

import (
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type redisConfig struct {
	RedisAddr string
	RedisPass string
	KeyPrefix string
}

func (c *redisConfig) readConfig() {
	c.RedisAddr = viper.GetString("redis.addr")
	c.RedisPass = viper.GetString("redis.pass")
	c.KeyPrefix = viper.GetString("redis.key_prefix")
}

func (c *redisConfig) checkConfig() {
	if c.RedisAddr == "" {
		zap.L().Panic(noRedisMsg)
	}
}
