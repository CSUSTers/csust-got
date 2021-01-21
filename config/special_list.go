package config

import "github.com/spf13/viper"

type specialListConfig struct {
	Name    string
	Enabled bool
	Chats   []int64
}

func (c *specialListConfig) readConfig() {
	c.Chats = make([]int64, 0)
	c.Enabled = viper.GetBool(c.Name + ".enabled")
}

func (c *specialListConfig) checkConfig() {

}

func (c *specialListConfig) SetName(name string) {
	c.Name = name
}

func (c *specialListConfig) Check(chatID int64) bool {
	for _, v := range c.Chats {
		if v == chatID {
			return true
		}
	}
	return false
}
