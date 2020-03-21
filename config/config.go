package config

import (
	"io/ioutil"
	"log"
	"os"
	"path"
)


var BotConfig *Config

func init() {
	var err error
	BotConfig, err = FromFolder("../")
	if err != nil {
		log.Panic(err)
	}
}

// Config the interface for common configs.
type Config struct {
	Token string
}

// FromFolder creates a config from a config folder.
func FromFolder(folder string) (*Config, error) {
	tokenName := path.Join(folder, ".token")
	tokenFile, err := os.Open(tokenName)
	if err != nil {
		return nil, err
	}
	tokenBytes, err := ioutil.ReadAll(tokenFile)
	if err != nil {
		return nil, err
	}
	conf := &Config{
		Token: string(tokenBytes),
	}
	return conf, nil
}
