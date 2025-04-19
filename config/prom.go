package config

import (
	"github.com/spf13/viper"
)

type promConfig struct {
	Enabled bool
}

func (c *promConfig) readConfig() {
	c.Enabled = viper.GetBool("prometheus.enabled")
}

func (c *promConfig) checkConfig() {

}
