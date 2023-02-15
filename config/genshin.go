package config

import (
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type genShinConfig struct {
	ApiServer string
}

func (c *genShinConfig) readConfig() {
	c.ApiServer = viper.GetString("genshin_voice.api_server")
}

func (c *genShinConfig) checkConfig() {
	if c.ApiServer == "" {
		zap.L().Panic(noGenShinApiMsg)
	}
}
