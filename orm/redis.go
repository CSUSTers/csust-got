package orm

import (
	"csust-got/config"
	"csust-got/log"
	"go.uber.org/zap"

	"github.com/go-redis/redis/v7"
)

var client *redis.Client

func InitRedis() {
	client = NewClient()
}

// GetClient return global redis client
func GetClient() *redis.Client {
	return client
}

// NewClient new redis client
func NewClient() *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:     config.BotConfig.RedisConfig.RedisAddr,
		Password: config.BotConfig.RedisConfig.RedisPass,
	})
}

// Ping can ping a redis client.
// return true if ping success.
func Ping(c *redis.Client) bool {
	_, err := c.Ping().Result()
	if err != nil {
		log.Error("ping redis failed", zap.Error(err))
		return false
	}
	return true
}
