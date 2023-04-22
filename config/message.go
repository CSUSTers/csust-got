package config

import (
	"reflect"

	"go.uber.org/zap"
)

var missMsg = "[this message has eat by bot]"

type messageConfig struct {
	Links            string `koanf:"links"`
	RestrictBot      string `koanf:"restrict_bot"`
	FakeBanInCD      string `koanf:"fake_ban_in_cd"`
	HitokotoNotFound string `koanf:"hitokoto_not_found"`
	NoSleep          string `koanf:"no_sleep"`
	BootFailed       string `koanf:"boot_failed"`
	WelcomeMessage   string `koanf:"welcome"`
}

func (c *messageConfig) checkConfig() {
	v := reflect.ValueOf(c).Elem()
	for i := 0; i < v.NumField(); i++ {
		s := v.Field(i).String()
		if s == "" {
			zap.L().Warn("message config not set, use default value",
				zap.String("key", v.Type().Field(i).Name))
			v.Field(i).SetString(missMsg)
		}
	}
	*c = v.Interface().(messageConfig)
}
