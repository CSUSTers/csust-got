package orm

import (
	"csust-got/config"
	"log"

	"github.com/go-redis/redis/v7"
)

var client *redis.Client

func init() {
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
		log.Println(err.Error())
		return false
	}
	return true
}
