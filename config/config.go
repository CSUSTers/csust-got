package config

import (
	"fmt"
	"strings"

	"github.com/go-redis/redis/v7"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

// BotConfig can get bot's config globally
var BotConfig *Config

var (
	noTokenMsg = "bot token is not set! Please set config file config.yaml or env BOT_TOKEN!"
	noRedisMsg = "redis address is not set! Please set config file config.yaml or env BOT_REDIS_ADDR!"
)

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
	return config
}

// Config the interface for common configs.
type Config struct {
	Bot *tgbotapi.BotAPI

	Token     string
	DebugMode bool
	Worker    int

	RedisConfig     *redisConfig
	RateLimitConfig *rateLimitConfig
}

type redisConfig struct {
	RedisAddr string
	RedisPass string
}

type rateLimitConfig struct {
	MaxToken    int
	Limit       float64
	Cost        int
	StickerCost int
	CommandCost int
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
	BotConfig.Worker = viper.GetInt("worker")

	// redis config
	BotConfig.RedisConfig = &redisConfig{
		RedisAddr: viper.GetString("redis.addr"),
		RedisPass: viper.GetString("redis.pass"),
	}

	// rate limit
	BotConfig.RateLimitConfig = &rateLimitConfig{
		MaxToken:    viper.GetInt("rate_limit.max_token"),
		Limit:       viper.GetFloat64("rate_limit.limit"),
		Cost:        viper.GetInt("rate_limit.cost"),
		StickerCost: viper.GetInt("rate_limit.cost_sticker"),
		CommandCost: viper.GetInt("rate_limit.cost_command"),
	}
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
	if BotConfig.RateLimitConfig.MaxToken <= 0 {
		BotConfig.RateLimitConfig.MaxToken = 1
	}
	if BotConfig.RateLimitConfig.Limit <= 0 {
		BotConfig.RateLimitConfig.Limit = 1
	}
	if BotConfig.RateLimitConfig.Cost < 0 {
		BotConfig.RateLimitConfig.Cost = 1
	}
	if BotConfig.RateLimitConfig.StickerCost < 0 {
		BotConfig.RateLimitConfig.StickerCost = 1
	}
	if BotConfig.RateLimitConfig.CommandCost < 0 {
		BotConfig.RateLimitConfig.CommandCost = 1
	}
}

// NewRedisClient can new a redis client
func (c Config) NewRedisClient() *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:     c.RedisConfig.RedisAddr,
		Password: c.RedisConfig.RedisPass,
	})
}
