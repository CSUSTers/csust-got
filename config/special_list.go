package config

import "slices"

import "github.com/spf13/viper"

type specialListConfig struct {
	Name    string
	Enabled bool
	Chats   []int64
}

func (c *specialListConfig) readConfig() {
	c.Chats = make([]int64, 0)
	c.Enabled = viper.GetBool(c.Name + ".enabled")
	chats := viper.GetIntSlice(c.Name + ".chats")
	for _, v := range chats {
		c.Chats = append(c.Chats, int64(v))
	}
}

func (c *specialListConfig) checkConfig() {

}

func (c *specialListConfig) SetName(name string) {
	c.Name = name
}

func (c *specialListConfig) Check(chatID int64) bool {
	return slices.Contains(c.Chats, chatID)
}
