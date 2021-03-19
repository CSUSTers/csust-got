package config

import (
	"github.com/spf13/viper"
)

type promConfig struct {
	Enabled      bool
	Address      string
	MessageQuery string
	StickerQuery string
}

func (c *promConfig) readConfig() {
	c.Enabled = viper.GetBool("prometheus.enabled")
	c.Address = viper.GetString("prometheus.address")
	c.MessageQuery = viper.GetString("prometheus.message_query")
	c.StickerQuery = viper.GetString("prometheus.sticker_query")
}

func (c *promConfig) checkConfig() {

}
