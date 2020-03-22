package orm

import (
	"csust-got/config"
	"github.com/go-redis/redis/v7"
	"log"
)

var client *redis.Client

func init() {
	client = NewClient()
}

func GetClient() *redis.Client {
	return client
}

func NewClient() *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:     config.BotConfig.RedisAddr,
		Password: config.BotConfig.RedisPass,
	})
}

func Ping(c *redis.Client) bool {
	_, err := c.Ping().Result()
	if err != nil {
		log.Println(err.Error())
		return false
	}
	return true
}
