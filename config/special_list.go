package config

import "github.com/spf13/viper"

type blackListConfig struct {
	Enabled bool
	Users   []int
}

func (c *blackListConfig) readConfig() {
	c.Users = make([]int, 0)
	c.Enabled = viper.GetBool("black_list.enabled")
}

func (c *blackListConfig) checkConfig() {

}

type whiteListConfig struct {
	Enabled bool
	Users   []int
}

func (c *whiteListConfig) readConfig() {
	c.Users = make([]int, 0)
	c.Enabled = viper.GetBool("white_list.enabled")
}

func (c *whiteListConfig) checkConfig() {

}
