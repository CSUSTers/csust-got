package config

import (
	"go.uber.org/zap"
)

type genShinConfig struct {
	ApiServer    string `koanf:"api_server"`
	ErrAudioAddr string `koanf:"err_audio_addr"`
}

func (c *genShinConfig) checkConfig() {
	if c.ApiServer == "" {
		zap.L().Warn(noGenShinApiMsg)
	}
}
