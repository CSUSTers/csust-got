package config

import (
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type tw2fx struct {
	EnabledUserList []string
}

type bv2av struct {
	EnabledUserList []string
}

type urlFilterConfig struct {
	Enabled bool
	Bv2av   bv2av
	Tw2fx   tw2fx
}

type contentFilterConfig struct {
	UrlFilterConfig urlFilterConfig
}

func (c *contentFilterConfig) readConfig() {
	c.UrlFilterConfig.Enabled = viper.GetBool("content_filter.url_filter.enabled")
	c.UrlFilterConfig.Tw2fx.EnabledUserList = viper.GetStringSlice("content_filter.url_filter.tw2fx.enable_user_list")
	c.UrlFilterConfig.Bv2av.EnabledUserList = viper.GetStringSlice("content_filter.url_filter.bv2av.enable_user_list")
}

func (c *contentFilterConfig) checkConfig() {
	if c.UrlFilterConfig.Enabled &&
		(c.UrlFilterConfig.Tw2fx.EnabledUserList == nil ||
			c.UrlFilterConfig.Bv2av.EnabledUserList == nil) {
		zap.L().Warn(noGithubMsg)
	}
}
