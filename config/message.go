package config

import (
	"github.com/spf13/viper"
	"reflect"
)

var missMsg = "[this message has eat by bot]"

type messageConfig struct {
	Links       string
	RestrictBot string
	FakeBanInCD string
}

func (c *messageConfig) readConfig() {
	c.Links = viper.GetString("message.links")
	c.RestrictBot = viper.GetString("message.restrict_bot")
	c.FakeBanInCD = viper.GetString("message.fake_ban_in_cd")
}

func (c *messageConfig) checkConfig() {
	v := reflect.ValueOf(c).Elem()
	for i := 0; i < v.NumField(); i++ {
		s := v.Field(i).String()
		if s == "" {
			v.Field(i).SetString(missMsg)
		}
	}
	*c = v.Interface().(messageConfig)
}
