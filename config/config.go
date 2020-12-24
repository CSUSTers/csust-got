package config

import (
	"fmt"
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

type config interface {
	readConfig()
	checkConfig()
}

// InitConfig - init bot config
func InitConfig(configFile, envPrefix string) {
	BotConfig = NewBotConfig()
	initViper(configFile, envPrefix)
	readConfig()
	checkConfig()
}

// NewBotConfig - return new bot config
func NewBotConfig() *Config {
	config := new(Config)
	config.RateLimitConfig = new(rateLimitConfig)
	config.RedisConfig = new(redisConfig)
	config.RestrictConfig = new(restrictConfig)
	config.MessageConfig = new(messageConfig)
	return config
}

// Config the interface for common configs.
type Config struct {
	Bot *tgbotapi.BotAPI

	Token     string
	DebugMode bool
	Worker    int

	RedisConfig     *redisConfig
	RestrictConfig  *restrictConfig
	RateLimitConfig *rateLimitConfig
	MessageConfig   *messageConfig
}

type redisConfig struct {
	RedisAddr string
	RedisPass string
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
				zap.L().Warn("config file not found!", zap.String("configFile", configFile), zap.Error(err))
			} else {
				zap.L().Warn("config file has found, but another error was produced when reading config!",
					zap.String("configFile", configFile), zap.Error(err))
			}
			zap.L().Warn("config file is not available...", zap.String("configFile", configFile), zap.Error(err))
			return
		}
	}
	viper.SetEnvPrefix(envPrefix)
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AllowEmptyEnv(true)
	viper.AutomaticEnv()

	noTokenMsg = fmt.Sprintf("bot token is not set! Please set config file %s or env %s_TOKEN!", configFile, envPrefix)
	noRedisMsg = fmt.Sprintf("redis address is not set! Please set config file %s or env %s_REDIS_ADDR!", configFile, envPrefix)
}

func readConfig() {
	// base config
	BotConfig.DebugMode = viper.GetBool("debug")
	BotConfig.Token = viper.GetString("token")
	BotConfig.Worker = viper.GetInt("worker")

	// redis config
	BotConfig.RedisConfig = &redisConfig{
		RedisAddr: viper.GetString("redis.addr"),
		RedisPass: viper.GetString("redis.pass"),
	}

	BotConfig.RestrictConfig.readConfig()
	BotConfig.RateLimitConfig.readConfig()
	BotConfig.MessageConfig.readConfig()
}

func checkConfig() {
	if BotConfig.Token == "" {
		zap.L().Panic(noTokenMsg)
	}
	if BotConfig.RedisConfig.RedisAddr == "" {
		zap.L().Panic(noRedisMsg)
	}
	if BotConfig.DebugMode {
		zap.L().Warn("DEBUG MODE IS ON")
	}
	if BotConfig.Worker <= 0 {
		BotConfig.Worker = 1
	}
	BotConfig.RestrictConfig.checkConfig()
	BotConfig.RateLimitConfig.checkConfig()
	BotConfig.MessageConfig.checkConfig()
}
