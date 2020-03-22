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

	BotConfig = FromEnv()
	if BotConfig != nil {
		return
	}

	log.Println("Can not get config from env, try config folder!")
	BotConfig, err = FromFolder("../")
	if err == nil {
		return
	}

	panic("Can not get config from all way!")
}

// Config the interface for common configs.
type Config struct {
	Token string
	RedisAddr string
	RedisPass string
	DebugMode bool
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

// FromEnv load config from environment
// and you may config them in docker compose file
func FromEnv() *Config {
	token, tokenExist := os.LookupEnv("TOKEN")
	redisAddr, addrExist := os.LookupEnv("REDIS_ADDR")
	redisPass, passExist := os.LookupEnv("REDIS_PASSWORD")
	debug, _ := os.LookupEnv("DEBUG")
	if tokenExist && addrExist && passExist {
		return &Config{
			Token:     token,
			RedisAddr: redisAddr,
			RedisPass: redisPass,
			DebugMode: debug=="true",
		}
	}
	return nil
}
