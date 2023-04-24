package config

import (
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type githubConfig struct {
	Enabled bool
	Token   string
	Repo    string
	Owner   string
	Branch  string
	Path    string
}

func (c *githubConfig) readConfig() {
	c.Enabled = viper.GetBool("github.enabled")
	c.Token = viper.GetString("github.token")
	c.Repo = viper.GetString("github.repo")
	c.Owner = viper.GetString("github.owner")
	c.Branch = viper.GetString("github.branch")
	c.Path = viper.GetString("github.path")
}

func (c *githubConfig) checkConfig() {
	if c.Token == "" && c.Enabled {
		zap.L().Warn(noGithubMsg)
	}
}
