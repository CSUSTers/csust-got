package config

import "github.com/spf13/viper"

type thirdAPI struct {
	BingMap string
}

func (c *thirdAPI) readConfig() {
	c.BingMap = viper.GetString("third_api.bing_map")
}

func (c *thirdAPI) checkConfig() {
	// TODO: nothing now
}
