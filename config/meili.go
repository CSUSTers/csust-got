package config

import (
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type meiliConfig struct {
	Enabled     bool
	HostAddr    string
	ApiKey      string
	IndexPrefix string
}

func (c *meiliConfig) readConfig() {
	c.Enabled = viper.GetBool("meili.enabled")
	c.HostAddr = viper.GetString("meili.address")
	c.IndexPrefix = viper.GetString("meili.index_prefix")
	c.ApiKey = viper.GetString("meili.api_key")
}

func (c *meiliConfig) checkConfig() {
	if (c.HostAddr == "" || c.ApiKey == "") && c.Enabled {
		zap.L().Warn(noMeiliMsg)
	}
}
