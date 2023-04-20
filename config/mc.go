package config

import (
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type mcConfig struct {
	MaxCount int
}

func (c *mcConfig) readConfig() {
	c.MaxCount = viper.GetInt("mc.max_count")
}

func (c *mcConfig) checkConfig() {
	if c.MaxCount < 0 || c.MaxCount > 10 {
		zap.L().Fatal("mc config: `MaxCount` must in [0, 10]", zap.Int("MaxCount", c.MaxCount))
	}
}
