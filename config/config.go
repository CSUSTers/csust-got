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
	noMeiliMsg      = "meili configuration is not set! Please set config file config.yaml!"
	noGithubMsg     = "github configuration is not set! Please set config file config.yaml!"
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
	config := &Config{
		RateLimitConfig:     new(rateLimitConfig),
		RedisConfig:         new(redisConfig),
		RestrictConfig:      new(restrictConfig),
		MessageConfig:       new(messageConfig),
		WhiteListConfig:     new(specialListConfig),
		BlockListConfig:     new(specialListConfig),
		PromConfig:          new(promConfig),
		GenShinConfig:       new(genShinConfig),
		ChatConfig:          new(chatConfig),
		MeiliConfig:         new(meiliConfig),
		McConfig:            new(mcConfig),
		GithubConfig:        new(githubConfig),
		ContentFilterConfig: new(contentFilterConfig),
		DebugOptConfig:      new(debugOptConfig),
	}

	config.WhiteListConfig.SetName("white_list")
	config.BlockListConfig.SetName("black_list")

	return config
}

// Config the interface for common configs.
type Config struct {
	Bot *Bot

	URL          string
	Token        string
	Proxy        string
	Listen       string
	DebugMode    bool
	SkipDuration int64
	LogFileDir   string

	RedisConfig         *redisConfig
	RestrictConfig      *restrictConfig
	RateLimitConfig     *rateLimitConfig
	MessageConfig       *messageConfig
	BlockListConfig     *specialListConfig
	WhiteListConfig     *specialListConfig
	PromConfig          *promConfig
	GenShinConfig       *genShinConfig
	ChatConfig          *chatConfig
	MeiliConfig         *meiliConfig
	McConfig            *mcConfig
	GithubConfig        *githubConfig
	ContentFilterConfig *contentFilterConfig

	DebugOptConfig *debugOptConfig
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
	BotConfig.URL = viper.GetString("url")
	BotConfig.Token = viper.GetString("token")
	BotConfig.Proxy = viper.GetString("proxy")
	BotConfig.Listen = viper.GetString("listen")
	BotConfig.SkipDuration = viper.GetInt64("skip_duration")
	BotConfig.LogFileDir = viper.GetString("log_file_dir")

	// other
	BotConfig.RedisConfig.readConfig()
	BotConfig.RestrictConfig.readConfig()
	BotConfig.RateLimitConfig.readConfig()
	BotConfig.MessageConfig.readConfig()
	BotConfig.WhiteListConfig.readConfig()
	BotConfig.BlockListConfig.readConfig()
	BotConfig.PromConfig.readConfig()
	BotConfig.ChatConfig.readConfig()
	BotConfig.MeiliConfig.readConfig()
	BotConfig.McConfig.readConfig()
	BotConfig.GithubConfig.readConfig()
	BotConfig.ContentFilterConfig.readConfig()

	// genshin voice
	BotConfig.GenShinConfig.readConfig()

	// debug opt
	BotConfig.DebugOptConfig.readConfig()
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
	if BotConfig.LogFileDir == "" {
		BotConfig.LogFileDir = "logs"
	}
	BotConfig.LogFileDir = strings.TrimRight(BotConfig.LogFileDir, "/")

	BotConfig.RedisConfig.checkConfig()
	BotConfig.RestrictConfig.checkConfig()
	BotConfig.RateLimitConfig.checkConfig()
	BotConfig.MessageConfig.checkConfig()
	BotConfig.BlockListConfig.checkConfig()
	BotConfig.WhiteListConfig.checkConfig()
	BotConfig.PromConfig.checkConfig()
	BotConfig.GenShinConfig.checkConfig()
	BotConfig.ChatConfig.checkConfig()
	BotConfig.MeiliConfig.checkConfig()
	BotConfig.McConfig.checkConfig()
	BotConfig.GithubConfig.checkConfig()
	BotConfig.ContentFilterConfig.checkConfig()

	BotConfig.DebugOptConfig.checkConfig()
}
