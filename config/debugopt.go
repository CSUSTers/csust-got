package config

import "github.com/spf13/viper"

type debugOptConfig struct {
	ShowThis bool
}

func (c *debugOptConfig) readConfig() {
	c.ShowThis = true
	if viper.IsSet("debugopt.show_this") {
		c.ShowThis = viper.GetBool("debugopt.show_this")
	}
}

func (c *debugOptConfig) checkConfig() {
}
