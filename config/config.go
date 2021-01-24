package config

import (
	"csust-got/prom"
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

// interface for module config
type config interface {
	readConfig()
	checkConfig()
}

// InitConfig - init bot config.
func InitConfig(configFile, envPrefix string) {
	BotConfig = NewBotConfig()
	initViper(configFile, envPrefix)
	readConfig()
	checkConfig()
}

// NewBotConfig - return new bot config with all zero value.
// In general, you don't need to NewBotConfig, global BotConfig should be used.
func NewBotConfig() *Config {
	config := new(Config)
	config.RateLimitConfig = new(rateLimitConfig)
	config.RedisConfig = new(redisConfig)
	config.RestrictConfig = new(restrictConfig)
	config.MessageConfig = new(messageConfig)
	config.WhiteListConfig = new(specialListConfig)
	config.BlackListConfig = new(specialListConfig)
	config.WhiteListConfig.SetName("white_list")
	config.BlackListConfig.SetName("black_list")
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
	BlackListConfig *specialListConfig
	WhiteListConfig *specialListConfig
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
			prom.Log(zap.WarnLevel.String())
			prom.Log(zap.WarnLevel.String())
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

	// other
	BotConfig.RedisConfig.readConfig()
	BotConfig.RestrictConfig.readConfig()
	BotConfig.RateLimitConfig.readConfig()
	BotConfig.MessageConfig.readConfig()
	BotConfig.WhiteListConfig.readConfig()
	BotConfig.BlackListConfig.readConfig()
}

// check some config value is reasonable, otherwise set to default value.
func checkConfig() {
	if BotConfig.Token == "" {
		zap.L().Panic(noTokenMsg)
		prom.Log(zap.PanicLevel.String())
	}
	if BotConfig.DebugMode {
		zap.L().Warn("DEBUG MODE IS ON")
		prom.Log(zap.WarnLevel.String())
	}
	if BotConfig.Worker <= 0 {
		BotConfig.Worker = 1
	}

	BotConfig.RedisConfig.checkConfig()
	BotConfig.RestrictConfig.checkConfig()
	BotConfig.RateLimitConfig.checkConfig()
	BotConfig.MessageConfig.checkConfig()
	BotConfig.BlackListConfig.checkConfig()
	BotConfig.WhiteListConfig.checkConfig()
}
