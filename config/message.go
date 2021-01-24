package config

import (
	"csust-got/prom"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"reflect"
)

var missMsg = "[this message has eat by bot]"

type messageConfig struct {
	Links            string
	RestrictBot      string
	FakeBanInCD      string
	HitokotoNotFound string
	NoSleep          string
	BootFailed       string
}

func (c *messageConfig) readConfig() {
	c.Links = viper.GetString("message.links")
	c.RestrictBot = viper.GetString("message.restrict_bot")
	c.FakeBanInCD = viper.GetString("message.fake_ban_in_cd")
	c.HitokotoNotFound = viper.GetString("message.hitokoto_not_found")
	c.NoSleep = viper.GetString("message.no_sleep")
	c.BootFailed = viper.GetString("message.boot_failed")
}

func (c *messageConfig) checkConfig() {
	v := reflect.ValueOf(c).Elem()
	for i := 0; i < v.NumField(); i++ {
		s := v.Field(i).String()
		if s == "" {
			zap.L().Warn("message config not set, use default value",
				zap.String("key", v.Type().Field(i).Name))
			prom.Log(zap.WarnLevel.String())
			v.Field(i).SetString(missMsg)
		}
	}
	*c = v.Interface().(messageConfig)
}
