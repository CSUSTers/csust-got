package config

import (
	"csust-got/prom"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type redisConfig struct {
	RedisAddr string
	RedisPass string
}

func (c *redisConfig) readConfig() {
	c.RedisAddr = viper.GetString("redis.addr")
	c.RedisPass = viper.GetString("redis.pass")
}

func (c *redisConfig) checkConfig() {
	if c.RedisAddr == "" {
		zap.L().Panic(noRedisMsg)
		prom.Log(zap.PanicLevel.String())
	}
}
