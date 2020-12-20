package config

import (
	"fmt"
	"github.com/go-redis/redis/v7"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"strings"
)

// BotConfig can get bot's config globally
var BotConfig *Config

var (
	noTokenMsg = "bot token is not set! Please set config file config.yaml or env BOT_TOKEN!"
	noRedisMsg = "redis address is not set! Please set config file config.yaml or env BOT_REDIS_ADDR!"
)

// InitConfig - init bot config
func InitConfig(configFile, envPrefix string) {
	BotConfig = new(Config)
	initViper(configFile, envPrefix)
	readConfig()
	checkConfig()
}

// Config the interface for common configs.
type Config struct {
	Token     string
	RedisAddr string
	RedisPass string
	DebugMode bool
	Bot       *tgbotapi.BotAPI
}

// BotID returns the BotID of this config.
func (c Config) BotID() int {
	return c.Bot.Self.ID
}

func initViper(configFile, envPrefix string) {
	if configFile != "" {
		viper.SetConfigFile(configFile)
		if err := viper.ReadInConfig(); err != nil {
			if _, ok := err.(viper.ConfigFileNotFoundError); ok {
				// Config file not found
				zap.S().Warnf("config file %s not found! err:%v", configFile, err)
			} else {
				// Config file was found but another error was produced
				zap.S().Warnf("config file %s has found, but another error was produced when reading config! err: %v", configFile, err)
			}
			zap.S().Warnf("%s is not avaliable... err: %v", configFile, err)
			return
		}
	}
	viper.SetEnvPrefix(envPrefix)
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	noTokenMsg = fmt.Sprintf("bot token is not set! Please set config file %s or env %s_TOKEN!", configFile, envPrefix)
	noRedisMsg = fmt.Sprintf("redis address is not set! Please set config file %s or env %s_REDIS_ADDR!", configFile, envPrefix)
}

func readConfig() {
	// base config
	BotConfig.DebugMode = viper.GetBool("debug")
	BotConfig.Token = viper.GetString("token")

	// redis config
	BotConfig.RedisAddr = viper.GetString("redis.addr")
	BotConfig.RedisPass = viper.GetString("redis.pass")
}

func checkConfig() {
	if BotConfig.Token == "" {
		zap.L().Panic(noTokenMsg)
	}
	if BotConfig.RedisAddr == "" {
		zap.L().Panic(noRedisMsg)
	}
	if BotConfig.DebugMode {
		zap.L().Warn("DEBUG MODE IS ON")
	}
}

// NewRedisClient can new a redis client
func (c Config) NewRedisClient() *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:     c.RedisAddr,
		Password: c.RedisPass,
	})
}
