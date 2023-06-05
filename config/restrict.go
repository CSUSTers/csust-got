package config

import (
	"time"

	"github.com/spf13/viper"
)

type restrictConfig struct {
	KillSeconds          int
	FakeBanMaxAddSeconds int
}

func (c *restrictConfig) readConfig() {
	c.KillSeconds = viper.GetInt("restrict.kill_duration")
	c.FakeBanMaxAddSeconds = viper.GetInt("restrict.fake_ban_max_add")
}

func (c *restrictConfig) checkConfig() {
	if c.KillSeconds <= 0 {
		c.KillSeconds = 30
	}
	if c.FakeBanMaxAddSeconds <= 0 {
		c.FakeBanMaxAddSeconds = c.KillSeconds / 5
	}
}

type rateLimitConfig struct {
	MaxToken    int
	Limit       float64
	Cost        int
	StickerCost int
	CommandCost int

	// in milliseconds
	ExpireTime time.Duration
}

func (c *rateLimitConfig) readConfig() {
	c.MaxToken = viper.GetInt("rate_limit.max_token")
	c.Limit = viper.GetFloat64("rate_limit.limit")
	c.Cost = viper.GetInt("rate_limit.cost")
	c.StickerCost = viper.GetInt("rate_limit.cost_sticker")
	c.CommandCost = viper.GetInt("rate_limit.cost_command")

	expire := viper.GetInt64("rate_limit.expire_time")
	if expire <= 60*1000 {
		expire = 60 * 1000
	}
	c.ExpireTime = time.Duration(expire) * time.Millisecond
}

func (c *rateLimitConfig) checkConfig() {
	if c.MaxToken <= 0 {
		c.MaxToken = 1
	}
	if c.Limit <= 0 {
		c.Limit = 1
	}
	if c.Cost < 0 {
		c.Cost = 1
	}
	if c.StickerCost < 0 {
		c.StickerCost = 1
	}
	if c.CommandCost < 0 {
		c.CommandCost = 1
	}
}
