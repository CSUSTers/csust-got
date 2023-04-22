package config

import (
	"go.uber.org/zap"
)

type meiliConfig struct {
	Enabled     bool   `koanf:"enabled"`
	HostAddr    string `koanf:"address"`
	ApiKey      string `koanf:"api_key"`
	IndexPrefix string `koanf:"index_prefix"`
}

func (c *meiliConfig) checkConfig() {
	if (c.HostAddr == "" || c.ApiKey == "") && c.Enabled {
		zap.L().Warn(noMeiliMsg)
	}
}
