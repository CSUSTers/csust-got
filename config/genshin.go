package config

import (
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type genShinConfig struct {
	ApiServer    string
	ErrAudioAddr string
}

func (c *genShinConfig) readConfig() {
	c.ApiServer = viper.GetString("genshin_voice.api_server")
	c.ErrAudioAddr = viper.GetString("genshin_voice.err_audio_addr")
}

func (c *genShinConfig) checkConfig() {
	if c.ApiServer == "" {
		zap.L().Warn(noGenShinApiMsg)
	}
}
