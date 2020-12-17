package orm

import (
	"csust-got/config"

	"github.com/go-redis/redis/v7"
	"go.uber.org/zap"
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
		Addr:     config.BotConfig.RedisAddr,
		Password: config.BotConfig.RedisPass,
	})
}

// Ping can ping a redis client.
// return true if ping success.
func Ping(c *redis.Client) bool {
	_, err := c.Ping().Result()
	if err != nil {
		zap.L().Error(err.Error())
		return false
	}
	return true
}
