package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
)

// BotConfig can get bot's config globally.
var BotConfig *Config

var (
	noTokenMsg      = "bot token is not set! Please set config file config.yaml or env BOT_TOKEN!"
	noRedisMsg      = "redis address is not set! Please set config file config.yaml or env BOT_REDIS_ADDR!"
	noGenShinApiMsg = "genShinApi address is not set! Please set config file config.yaml!"
)

// interface for module config
// type config interface {
// 	readConfig()
// 	checkConfig()
// }

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
	config.BlockListConfig = new(specialListConfig)
	config.PromConfig = new(promConfig)
	config.WhiteListConfig.SetName("white_list")
	config.BlockListConfig.SetName("black_list")
	config.GenShinConfig = new(genShinConfig)
	return config
}

// Config the interface for common configs.
type Config struct {
	Bot *Bot

	Token        string
	Proxy        string
	Listen       string
	DebugMode    bool
	SkipDuration int64

	RedisConfig     *redisConfig
	RestrictConfig  *restrictConfig
	RateLimitConfig *rateLimitConfig
	MessageConfig   *messageConfig
	BlockListConfig *specialListConfig
	WhiteListConfig *specialListConfig
	PromConfig      *promConfig
	GenShinConfig   *genShinConfig
}

// GetBot returns Bot.
func GetBot() *Bot {
	return BotConfig.Bot
}

func initViper(configFile, envPrefix string) {
	if configFile != "" {
		viper.SetConfigFile(configFile)
		if err := viper.ReadInConfig(); err != nil {
			zap.L().Warn("an error was produced when reading config!", zap.String("configFile", configFile), zap.Error(err))
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
	BotConfig.Proxy = viper.GetString("proxy")
	BotConfig.Listen = viper.GetString("listen")
	BotConfig.SkipDuration = viper.GetInt64("skip_duration")

	// other
	BotConfig.RedisConfig.readConfig()
	BotConfig.RestrictConfig.readConfig()
	BotConfig.RateLimitConfig.readConfig()
	BotConfig.MessageConfig.readConfig()
	BotConfig.WhiteListConfig.readConfig()
	BotConfig.BlockListConfig.readConfig()
	BotConfig.PromConfig.readConfig()

	// genshin voice
	BotConfig.GenShinConfig.readConfig()
}

// check some config value is reasonable, otherwise set to default value.
func checkConfig() {
	if BotConfig.Token == "" {
		zap.L().Panic(noTokenMsg)
	}
	if BotConfig.DebugMode {
		zap.L().Warn("DEBUG MODE IS ON")
	}
	if BotConfig.SkipDuration < 0 {
		BotConfig.SkipDuration = 0
	}

	BotConfig.RedisConfig.checkConfig()
	BotConfig.RestrictConfig.checkConfig()
	BotConfig.RateLimitConfig.checkConfig()
	BotConfig.MessageConfig.checkConfig()
	BotConfig.BlockListConfig.checkConfig()
	BotConfig.WhiteListConfig.checkConfig()
	BotConfig.PromConfig.checkConfig()
	BotConfig.GenShinConfig.checkConfig()
}
