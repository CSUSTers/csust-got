package config

import (
	"reflect"

	"github.com/spf13/viper"
	"go.uber.org/zap"
)

var missMsg = "[this message has eat by bot]"

type messageConfig struct {
	Links            string
	RestrictBot      string
	FakeBanInCD      string
	HitokotoNotFound string
	NoSleep          string
	BootFailed       string
	WelcomeMessage   string
}

func (c *messageConfig) readConfig() {
	c.Links = viper.GetString("message.links")
	c.RestrictBot = viper.GetString("message.restrict_bot")
	c.FakeBanInCD = viper.GetString("message.fake_ban_in_cd")
	c.HitokotoNotFound = viper.GetString("message.hitokoto_not_found")
	c.NoSleep = viper.GetString("message.no_sleep")
	c.BootFailed = viper.GetString("message.boot_failed")
	c.WelcomeMessage = viper.GetString("message.welcome")
}

func (c *messageConfig) checkConfig() {
	v := reflect.ValueOf(c).Elem()
	for i := range v.NumField() {
		s := v.Field(i).String()
		if s == "" {
			zap.L().Warn("message config not set, use default value",
				zap.String("key", v.Type().Field(i).Name))
			v.Field(i).SetString(missMsg)
		}
	}
	*c = v.Interface().(messageConfig)
}
